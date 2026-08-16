// Package archive implements the .gobag container: a tar stream, compressed
// with zstd, optionally encrypted with age.
//
// The three stages are chained streaming writers — plaintext is never spilled
// to a temporary file, which is a hard constraint from CLAUDE.md. Encryption
// is detected on read by sniffing the age header, so a .gobag file carries the
// same extension whether or not it is encrypted and the user handles exactly
// one filename.
//
// Every payload entry is hashed while it streams; the accumulated digests are
// written as a trailing CHECKSUMS entry. Because age streams are not seekable,
// every read is a full streaming pass — do not add seek-based shortcuts.
package archive

import (
	"archive/tar"
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"filippo.io/age"
	"github.com/klauspost/compress/zstd"
)

// ChecksumsName is the trailing entry listing "<sha256>  <path>" for every
// payload entry, sorted by path.
const ChecksumsName = "CHECKSUMS"

// ageHeader is the magic prefix of an age v1 binary stream.
const ageHeader = "age-encryption.org/v1"

// ScryptWorkFactor is age's scrypt log2 work factor. age's default of 18 costs
// roughly a second per operation, which is the point: it is what makes a
// passphrase-protected archive expensive to attack offline. Tests lower it.
var ScryptWorkFactor = 18

// Writer streams entries into a .gobag container.
type Writer struct {
	tw   *tar.Writer
	zw   *zstd.Encoder
	agew io.WriteCloser // nil when the archive is plaintext

	sums   map[string]string
	closed bool
}

// NewWriter builds the writer chain over dst. An empty passphrase produces a
// plaintext archive; callers must have obtained explicit --plaintext consent
// before passing one, since encryption is the default everywhere else.
func NewWriter(dst io.Writer, passphrase string) (*Writer, error) {
	w := &Writer{sums: map[string]string{}}

	out := dst
	if passphrase != "" {
		recipient, err := age.NewScryptRecipient(passphrase)
		if err != nil {
			return nil, fmt.Errorf("preparing encryption: %w", err)
		}
		recipient.SetWorkFactor(ScryptWorkFactor)
		// age requires a scrypt recipient to be the only recipient.
		agew, err := age.Encrypt(dst, recipient)
		if err != nil {
			return nil, fmt.Errorf("starting encrypted stream: %w", err)
		}
		w.agew = agew
		out = agew
	}

	zw, err := zstd.NewWriter(out)
	if err != nil {
		return nil, fmt.Errorf("starting compression: %w", err)
	}
	w.zw = zw
	w.tw = tar.NewWriter(zw)
	return w, nil
}

// AddFile streams one entry into the archive, hashing it on the way through.
// dest must already be validated as archive-relative by the caller.
func (w *Writer) AddFile(dest string, mode os.FileMode, size int64, r io.Reader) error {
	if w.closed {
		return fmt.Errorf("archive is closed")
	}
	if dest == ChecksumsName {
		return fmt.Errorf("%q is reserved", ChecksumsName)
	}
	if _, dup := w.sums[dest]; dup {
		return fmt.Errorf("duplicate archive entry %q", dest)
	}

	hdr := &tar.Header{
		Name:     dest,
		Mode:     int64(mode.Perm()),
		Size:     size,
		Typeflag: tar.TypeReg,
		// A fixed mtime keeps archives of identical content byte-comparable;
		// provenance lives in the manifest, not in tar metadata.
		ModTime: fixedModTime,
	}
	if err := w.tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("writing header for %s: %w", dest, err)
	}

	h := sha256.New()
	n, err := io.Copy(w.tw, io.TeeReader(r, h))
	if err != nil {
		return fmt.Errorf("writing %s: %w", dest, err)
	}
	if n != size {
		return fmt.Errorf("writing %s: declared %d bytes, wrote %d", dest, size, n)
	}
	w.sums[dest] = hex.EncodeToString(h.Sum(nil))
	return nil
}

// AddBytes is a convenience wrapper for small in-memory entries.
func (w *Writer) AddBytes(dest string, mode os.FileMode, b []byte) error {
	return w.AddFile(dest, mode, int64(len(b)), strings.NewReader(string(b)))
}

// Close writes the CHECKSUMS trailer and closes the chain innermost-first.
// The error from the outermost (encryption) stage matters most: dropping it
// would produce a silently truncated archive.
func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true

	if err := w.writeChecksums(); err != nil {
		return err
	}
	if err := w.tw.Close(); err != nil {
		return fmt.Errorf("finishing tar stream: %w", err)
	}
	if err := w.zw.Close(); err != nil {
		return fmt.Errorf("finishing compression: %w", err)
	}
	if w.agew != nil {
		if err := w.agew.Close(); err != nil {
			return fmt.Errorf("finishing encrypted stream: %w", err)
		}
	}
	return nil
}

func (w *Writer) writeChecksums() error {
	paths := make([]string, 0, len(w.sums))
	for p := range w.sums {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var b strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&b, "%s  %s\n", w.sums[p], p)
	}
	body := []byte(b.String())

	hdr := &tar.Header{
		Name:     ChecksumsName,
		Mode:     0o644,
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
		ModTime:  fixedModTime,
	}
	if err := w.tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("writing checksums header: %w", err)
	}
	if _, err := w.tw.Write(body); err != nil {
		return fmt.Errorf("writing checksums: %w", err)
	}
	return nil
}

// Reader streams entries out of a .gobag container.
type Reader struct {
	tr     *tar.Reader
	zr     *zstd.Decoder
	closer func()
}

// NewReader opens src, detecting encryption from the age header. A passphrase
// is required if and only if the stream is encrypted; supplying one for a
// plaintext archive is an error worth reporting rather than ignoring, since it
// usually means the user believes the file is protected when it is not.
func NewReader(src io.Reader, passphrase string) (*Reader, error) {
	br := bufio.NewReaderSize(src, 4096)
	encrypted, err := isEncrypted(br)
	if err != nil {
		return nil, err
	}

	var stream io.Reader = br
	switch {
	case encrypted && passphrase == "":
		return nil, ErrPassphraseRequired
	case encrypted:
		id, err := age.NewScryptIdentity(passphrase)
		if err != nil {
			return nil, fmt.Errorf("preparing decryption: %w", err)
		}
		dec, err := age.Decrypt(br, id)
		if err != nil {
			// age does not distinguish a wrong passphrase from a corrupt
			// header; the common cause by far is the wrong passphrase.
			return nil, fmt.Errorf("%w: %v", ErrWrongPassphrase, err)
		}
		stream = dec
	case passphrase != "":
		return nil, ErrNotEncrypted
	}

	zr, err := zstd.NewReader(stream)
	if err != nil {
		return nil, fmt.Errorf("starting decompression: %w", err)
	}
	return &Reader{tr: tar.NewReader(zr), zr: zr, closer: zr.Close}, nil
}

// Next advances to the next entry. It returns io.EOF at the end of the stream.
func (r *Reader) Next() (*tar.Header, error) { return r.tr.Next() }

// Read reads from the current entry.
func (r *Reader) Read(p []byte) (int, error) { return r.tr.Read(p) }

// Close releases the decompressor.
func (r *Reader) Close() error {
	if r.closer != nil {
		r.closer()
	}
	return nil
}

// IsEncryptedFile reports whether path is an encrypted archive, so callers can
// decide whether to prompt for a passphrase before opening the stream for
// real. age streams are not seekable, so a caller cannot discover this
// mid-read and recover.
func IsEncryptedFile(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("opening archive: %w", err)
	}
	defer f.Close()
	return isEncrypted(bufio.NewReaderSize(f, 4096))
}

// isEncrypted peeks for the age magic without consuming it.
func isEncrypted(br *bufio.Reader) (bool, error) {
	peek, err := br.Peek(len(ageHeader))
	switch {
	case err == io.EOF || err == bufio.ErrBufferFull:
		return false, ErrNotAnArchive
	case err != nil:
		return false, fmt.Errorf("reading archive header: %w", err)
	}
	return string(peek) == ageHeader, nil
}
