package archive

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"

	"github.com/satmihir/gobag/internal/manifest"
)

// tarTypeReg aliases the regular-file flag for readability at call sites.
const tarTypeReg = tar.TypeReg

// Entry is one file listed inside an archive.
type Entry struct {
	Path string
	Size int64
}

// Contents is what inspect reports: the manifest plus the entry listing.
type Contents struct {
	Manifest *manifest.Manifest
	Entries  []Entry
}

// Inspect streams the archive and reports its contents without writing
// anything to disk. The manifest is the first entry, but the listing still
// requires a full pass because age streams cannot be seeked.
func Inspect(src io.Reader, passphrase string) (*Contents, error) {
	r, err := NewReader(src, passphrase)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	c := &Contents{}
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

		if hdr.Name == manifest.Name && c.Manifest == nil {
			m, err := manifest.Decode(r)
			if err != nil {
				return nil, err
			}
			c.Manifest = m
		}
		c.Entries = append(c.Entries, Entry{Path: hdr.Name, Size: hdr.Size})
	}
	return c, nil
}
