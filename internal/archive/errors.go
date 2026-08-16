package archive

import (
	"errors"
	"time"
)

// Sentinel errors the CLI turns into plain-language, exit-code-1 messages.
var (
	// ErrPassphraseRequired means the archive is encrypted and no passphrase
	// was supplied.
	ErrPassphraseRequired = errors.New("archive is encrypted — a passphrase is required")
	// ErrWrongPassphrase means decryption failed, almost always because the
	// passphrase was wrong.
	ErrWrongPassphrase = errors.New("could not decrypt the archive — wrong passphrase?")
	// ErrNotEncrypted means a passphrase was supplied for a plaintext archive.
	// Worth surfacing: the user may believe the file is protected.
	ErrNotEncrypted = errors.New("archive is not encrypted — no passphrase needed")
	// ErrNotAnArchive means the file is too short or malformed to be a .gobag.
	ErrNotAnArchive = errors.New("not a gobag archive")
)

// fixedModTime stamps every tar entry so that archives of identical content
// compare byte-for-byte. Real provenance lives in the manifest.
var fixedModTime = time.Unix(0, 0).UTC()
