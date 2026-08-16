package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// VerifyResult is one entry's checksum outcome.
type VerifyResult struct {
	Path string
	OK   bool
	// Want is the digest recorded in CHECKSUMS, Got the digest computed while
	// streaming. One of them is empty when an entry is missing or unlisted.
	Want string
	Got  string
	Note string
}

// Verify streams the whole archive, recomputing every digest and comparing it
// against the CHECKSUMS trailer. Results are sorted by path. A non-empty slice
// with every OK true means the archive is intact.
func Verify(src io.Reader, passphrase string) ([]VerifyResult, error) {
	r, err := NewReader(src, passphrase)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	computed := map[string]string{}
	var recorded map[string]string

	for {
		hdr, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading archive: %w", err)
		}
		if hdr.Typeflag != tarTypeReg {
			continue
		}

		if hdr.Name == ChecksumsName {
			recorded, err = parseChecksums(r)
			if err != nil {
				return nil, err
			}
			continue
		}

		h := sha256.New()
		if _, err := io.Copy(h, r); err != nil {
			return nil, fmt.Errorf("reading %s: %w", hdr.Name, err)
		}
		computed[hdr.Name] = hex.EncodeToString(h.Sum(nil))
	}

	if recorded == nil {
		return nil, fmt.Errorf("archive has no %s entry", ChecksumsName)
	}
	return compare(recorded, computed), nil
}

func compare(recorded, computed map[string]string) []VerifyResult {
	seen := map[string]bool{}
	var out []VerifyResult

	for path, want := range recorded {
		seen[path] = true
		got, present := computed[path]
		switch {
		case !present:
			out = append(out, VerifyResult{Path: path, Want: want, Note: "listed in CHECKSUMS but missing from the archive"})
		default:
			out = append(out, VerifyResult{Path: path, OK: want == got, Want: want, Got: got})
		}
	}
	for path, got := range computed {
		if !seen[path] {
			out = append(out, VerifyResult{Path: path, Got: got, Note: "present in the archive but not listed in CHECKSUMS"})
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func parseChecksums(r io.Reader) (map[string]string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", ChecksumsName, err)
	}

	sums := map[string]string{}
	for i, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if line == "" {
			continue
		}
		digest, path, ok := strings.Cut(line, "  ")
		if !ok || len(digest) != sha256HexLen {
			return nil, fmt.Errorf("%s line %d is malformed", ChecksumsName, i+1)
		}
		sums[path] = digest
	}
	return sums, nil
}

const sha256HexLen = sha256.Size * 2
