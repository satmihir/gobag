package gitops

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/satmihir/gobag/internal/manifest"
)

// ExternalThreshold is the object-store size past which a repository is
// treated as external: too large to clone once per restore, and therefore
// expected to already exist on any machine that would restore it. A variable
// so tests and users can move it.
var ExternalThreshold int64 = 1 << 30 // 1 GiB

// RepoSizeBytes reports the size of a repository's object store. It asks git
// rather than walking the tree, which on a repository large enough to matter
// is the difference between milliseconds and minutes.
func RepoSizeBytes(dir string) (int64, error) {
	out, err := local(dir, "count-objects", "-v")
	if err != nil {
		return 0, fmt.Errorf("measuring %s: %w", dir, err)
	}

	// "size" and "size-pack" are reported in KiB: loose objects and packed
	// objects respectively. Their sum is what the repository actually costs.
	var kib int64
	for _, line := range lines(out) {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "size", "size-pack":
			n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err != nil {
				continue
			}
			kib += n
		}
	}
	return kib * 1024, nil
}

// LocateExternal checks whether clonePath is usable as the local copy of s:
// it must be a git repository whose remote is the one the manifest names.
// Pointing gobag at the wrong repository is the mistake worth catching here,
// because everything downstream would otherwise succeed and be wrong.
func LocateExternal(clonePath string, s manifest.Source) error {
	if clonePath == "" {
		return fmt.Errorf("no path given")
	}
	if fi, err := os.Stat(clonePath); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", clonePath)
	}
	if !isRepo(clonePath) {
		return fmt.Errorf("%s is not a git repository", clonePath)
	}

	_, url, err := remoteURL(clonePath)
	if err != nil {
		return fmt.Errorf("reading remotes of %s: %w", clonePath, err)
	}
	if !sameRemote(url, s.Remote) {
		return fmt.Errorf("%s points at %s, but the archive expects %s",
			clonePath, orNone(url), s.Remote)
	}
	return nil
}

// EnsureExternal attaches an external repository to the workspace as a linked
// worktree of an existing local clone.
//
// A worktree is the right instrument here: the object store stays single and
// shared, so a thirty-gigabyte repository costs nothing extra, while the
// workspace still gets its own checkout at the pinned ref. The checkout is
// deliberately detached — the branch the archive names is very likely checked
// out in the user's main clone already, and git refuses to have one branch in
// two worktrees. Detaching gets the right content without touching what the
// user is standing on.
func EnsureExternal(root string, s manifest.Source, clonePath string) (Result, error) {
	res := Result{Dest: s.Dest}
	dest, err := targetPath(root, s.Dest)
	if err != nil {
		return res, err
	}
	if err := LocateExternal(clonePath, s); err != nil {
		return res, fmt.Errorf("linking %s: %w", s.Dest, err)
	}

	// An existing checkout at the destination is converged, not replaced.
	present, err := occupied(dest)
	if err != nil {
		return res, err
	}
	if present {
		if !isRepo(dest) {
			return res, fmt.Errorf("linking %s: %s already exists and is not a git checkout", s.Dest, dest)
		}
		return advance(dest, s.Dest, s.Ref, "", fetcher(clonePath, s.Dest, remoteNameOf(clonePath)))
	}

	// Drops administrative entries for worktrees whose directories are gone,
	// so a removed link can be recreated. Removes no files.
	_, _ = local(clonePath, "worktree", "prune")

	if _, err := resolve(clonePath, s.Ref); err != nil {
		// The pinned commit is not in the local clone yet. Fetching is a
		// write to a repository gobag does not own, but it is additive: it
		// creates no commits, moves no branches, and touches no working tree.
		if ferr := fetcher(clonePath, s.Dest, remoteNameOf(clonePath))(); ferr != nil {
			res.Outcome = OutcomeUnreachable
			res.Detail = fmt.Sprintf("not linked: %s is missing from %s and could not be fetched (%s)",
				short(s.Ref), clonePath, reason(ferr))
			return res, nil
		}
		if _, err := resolve(clonePath, s.Ref); err != nil {
			return res, fmt.Errorf("linking %s: commit %s is not in %s even after fetching; "+
				"push it, or point gobag at a clone that has it", s.Dest, short(s.Ref), clonePath)
		}
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return res, fmt.Errorf("preparing %s: %w", s.Dest, err)
	}
	if _, err := local(clonePath, "worktree", "add", "--quiet", "--detach", dest, s.Ref); err != nil {
		return res, fmt.Errorf("linking %s: %w", s.Dest, err)
	}

	res.Ref, _ = head(dest)
	res.Outcome = OutcomeLinked
	res.Detail = fmt.Sprintf("linked as a worktree of %s at %s, detached%s — objects are shared, nothing was cloned",
		clonePath, short(res.Ref), branchNote(s.Branch))
	return res, nil
}

// UnlinkedResult describes an external repository no local clone was found
// for. It is a reportable outcome, never a failed install: every other
// repository still lands, and the user finishes the job with `gobag link`.
func UnlinkedResult(s manifest.Source) Result {
	detail := "not linked: no local clone is recorded for this remote on this machine"
	if s.SizeBytes > 0 {
		detail += fmt.Sprintf(" (about %s)", humanBytes(s.SizeBytes))
	}
	return Result{Dest: s.Dest, Outcome: OutcomeUnlinked, Detail: detail}
}

func remoteNameOf(dir string) string {
	name, _, err := remoteURL(dir)
	if err != nil {
		return ""
	}
	return name
}

func branchNote(branch string) string {
	if branch == "" {
		return ""
	}
	return fmt.Sprintf(" (the archive pinned branch %s)", branch)
}

// humanBytes formats a size for a sentence a person will read.
func humanBytes(n int64) string {
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
