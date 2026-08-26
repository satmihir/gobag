package gitops

import "path/filepath"

// SoleCopy reports whether a file's current content exists in no commit
// anywhere, which makes the archive its only copy once the packing workspace
// is gone.
//
// This is the quiet failure the pack-time warning exists to prevent: a long
// design document, never committed because it was "just notes", carried into a
// bag from a workspace that is about to be destroyed. gobag saves it — and
// then the bag is the single point of failure for work nobody realized was
// unversioned.
//
// True when the file is outside any repository, untracked within one, or
// tracked but modified — in every case the bytes being packed are in no
// commit. Uncertainty resolves toward true: over-warning costs a line of
// output, under-warning costs the document.
func SoleCopy(path string) bool {
	dir := filepath.Dir(path)

	if _, err := local(dir, "rev-parse", "--show-toplevel"); err != nil {
		return true // not in a repository at all
	}
	if _, err := local(dir, "ls-files", "--error-unmatch", "--", path); err != nil {
		return true // untracked
	}
	// Exit status is the answer here: non-zero means the working copy differs
	// from HEAD, so the packed bytes are not the committed bytes.
	if _, err := local(dir, "diff", "--quiet", "HEAD", "--", path); err != nil {
		return true
	}
	return false
}
