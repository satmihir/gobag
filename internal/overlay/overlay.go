// Package overlay writes restored files onto a target that may already hold
// the user's own work.
//
// It exists to make one hard constraint from CLAUDE.md concrete and testable:
// install must converge, never duplicate, and never destroy. A file that
// differs from the archived version is left exactly as the user left it, and
// the archived version lands beside it with a .from-gobag suffix for the user
// (or the restored agent) to reconcile deliberately.
package overlay

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Suffix marks a file that could not be written because the user had a
// different version at that path.
const Suffix = ".from-gobag"

// Outcome is what happened to one file.
type Outcome string

const (
	// OutcomeWritten means the path was free and the file was created.
	OutcomeWritten Outcome = "written"
	// OutcomeIdentical means the file was already present and byte-identical.
	OutcomeIdentical Outcome = "identical"
	// OutcomeConflict means the user's version was kept and the archived
	// version was written alongside it.
	OutcomeConflict Outcome = "conflict"
)

// Result reports one file's outcome. SidecarPath is set only on conflict.
type Result struct {
	Path        string // path relative to the root it was written under
	Outcome     Outcome
	SidecarPath string
}

// Conflicted reports whether the user's copy was preserved instead.
func (r Result) Conflicted() bool { return r.Outcome == OutcomeConflict }

// Write places content at root/rel under the overlay rule. rel must already be
// validated as non-escaping by the caller; Write re-checks anyway, because the
// path may originate in an agent-authored plan.
func Write(root, rel string, mode os.FileMode, r io.Reader) (Result, error) {
	dest, err := SafeJoin(root, rel)
	if err != nil {
		return Result{}, err
	}
	res := Result{Path: rel}

	content, err := io.ReadAll(r)
	if err != nil {
		return res, fmt.Errorf("reading %s: %w", rel, err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return res, fmt.Errorf("creating directory for %s: %w", rel, err)
	}

	existing, err := os.ReadFile(dest)
	switch {
	case os.IsNotExist(err):
		if err := os.WriteFile(dest, content, mode.Perm()); err != nil {
			return res, fmt.Errorf("writing %s: %w", rel, err)
		}
		res.Outcome = OutcomeWritten
		return res, nil

	case err != nil:
		return res, fmt.Errorf("reading existing %s: %w", rel, err)

	case bytes.Equal(existing, content):
		// Already converged. Rewriting would only churn mtimes.
		res.Outcome = OutcomeIdentical
		return res, nil
	}

	// The user has their own version. Theirs wins; ours lands beside it.
	sidecar := dest + Suffix
	if err := os.WriteFile(sidecar, content, mode.Perm()); err != nil {
		return res, fmt.Errorf("writing %s: %w", rel+Suffix, err)
	}
	res.Outcome = OutcomeConflict
	res.SidecarPath = rel + Suffix
	return res, nil
}

// SafeJoin resolves rel against root, refusing anything that would escape it.
// This is the last line of defense against a malicious or malformed archive
// entry, so it rejects absolute paths, traversal, and symlinked parents that
// point outside the root.
func SafeJoin(root, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("entry %q is an absolute path", rel)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("entry %q escapes the workspace root", rel)
	}

	dest := filepath.Join(root, clean)
	// Resolve symlinks on the deepest existing ancestor: a symlinked directory
	// inside the root could otherwise redirect the write outside it.
	anchor := dest
	for {
		if _, err := os.Lstat(anchor); err == nil {
			break
		}
		parent := filepath.Dir(anchor)
		if parent == anchor {
			break
		}
		anchor = parent
	}
	resolvedAnchor, err := filepath.EvalSymlinks(anchor)
	if err != nil {
		// Nothing resolvable yet; the lexical check above already holds.
		return dest, nil
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return dest, nil
	}
	if resolvedAnchor != resolvedRoot &&
		!strings.HasPrefix(resolvedAnchor, resolvedRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("entry %q resolves outside the workspace root", rel)
	}
	return dest, nil
}

// Digest is a small helper for callers that want to compare file content
// without reading it twice.
func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum)
}
