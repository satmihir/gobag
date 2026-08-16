package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/satmihir/gobag/internal/archive"
)

// openArchive prompts for a passphrase only when the archive actually needs
// one. Encryption has to be settled before the stream is opened for real,
// because an age stream cannot be rewound once reading starts.
func openArchive(path string, stderr io.Writer) (*os.File, string, error) {
	encrypted, err := archive.IsEncryptedFile(path)
	if err != nil {
		return nil, "", wrapUser(err)
	}

	var passphrase string
	if encrypted {
		passphrase, err = readPassphrase("passphrase: ", false, stderr)
		if err != nil {
			return nil, "", err
		}
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, "", wrapUser(fmt.Errorf("opening archive: %w", err))
	}
	return f, passphrase, nil
}

// archiveErr marks the archive package's sentinel failures as user-caused, so
// a wrong passphrase exits 1 rather than looking like an internal defect.
func archiveErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, archive.ErrPassphraseRequired),
		errors.Is(err, archive.ErrWrongPassphrase),
		errors.Is(err, archive.ErrNotEncrypted),
		errors.Is(err, archive.ErrNotAnArchive):
		return wrapUser(err)
	}
	return err
}

// humanSize formats a byte count for terse output.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 3 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}
