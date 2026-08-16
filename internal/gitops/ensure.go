package gitops

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/satmihir/gobag/internal/manifest"
	"github.com/satmihir/gobag/internal/plan"
)

// Outcome is what an Ensure call did, or declined to do. Install reports every
// outcome in ORIENTATION.md, so the set is deliberately small and honest:
// three of the eight values mean "gobag changed nothing on purpose".
type Outcome string

const (
	OutcomeCloned         Outcome = "cloned"
	OutcomeFastForwarded  Outcome = "fast-forwarded"
	OutcomeAlreadyAtRef   Outcome = "already-at-ref"
	OutcomeLeftDiverged   Outcome = "left-diverged"
	OutcomeLeftDirty      Outcome = "left-dirty"
	OutcomeRemoteMismatch Outcome = "remote-mismatch"
	OutcomeUnreachable    Outcome = "unreachable"
	OutcomeCreated        Outcome = "worktree-created"
)

// Result is one repository's or worktree's install outcome.
type Result struct {
	Dest    string
	Outcome Outcome
	// Ref is the resolved HEAD after the operation, empty when nothing landed.
	Ref string
	// Detail is a single plain-language line for ORIENTATION.md.
	Detail string
}

// EnsureRepo brings the checkout at root/s.Dest to the pinned ref, converging
// from any starting state without ever destroying work.
//
// Missing target: clone and check out the ref. Existing target: fetch and
// fast-forward, but only when the tree is clean and the move is a genuine
// fast-forward. A different remote, a dirty tree, or a diverged history all
// leave the directory byte-for-byte as found, described by the Outcome. There
// is no reset, no force, and no delete on any path through this function.
//
// An unreachable remote is a Result, not an error: one dead remote must not
// abort an install. Errors are reserved for situations gobag cannot describe
// with an Outcome — an unusable destination, a non-repository sitting in the
// way, or a pinned commit the remote has never heard of.
func EnsureRepo(root string, s manifest.Source) (Result, error) {
	res := Result{Dest: s.Dest}
	dest, err := targetPath(root, s.Dest)
	if err != nil {
		return res, err
	}
	if s.Ref == "" {
		return res, fmt.Errorf("restoring %s: no pinned ref in the manifest", s.Dest)
	}

	present, err := occupied(dest)
	if err != nil {
		return res, err
	}
	if !present {
		return cloneRepo(dest, s)
	}

	if !isRepo(dest) {
		return res, fmt.Errorf("restoring %s: %s already exists and is not a git repository", s.Dest, dest)
	}

	name, url, err := remoteURL(dest)
	if err != nil {
		return res, fmt.Errorf("restoring %s: %w", s.Dest, err)
	}
	if !sameRemote(url, s.Remote) {
		res.Ref, _ = head(dest)
		res.Outcome = OutcomeRemoteMismatch
		res.Detail = fmt.Sprintf("left untouched: this checkout points at %s, not %s", orNone(url), s.Remote)
		return res, nil
	}

	return advance(dest, s.Dest, s.Ref, s.Branch, fetcher(dest, s.Dest, name))
}

// cloneRepo populates an empty or missing destination.
func cloneRepo(dest string, s manifest.Source) (Result, error) {
	res := Result{Dest: s.Dest}
	if s.Remote == "" {
		return res, fmt.Errorf("restoring %s: no remote in the manifest, so there is nothing to clone", s.Dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return res, fmt.Errorf("preparing %s: %w", s.Dest, err)
	}

	if _, err := network("", "clone", "--quiet", s.Remote, dest); err != nil {
		// Nothing was created that survives a failed clone, so the install can
		// carry on and report this repository as missing.
		res.Outcome = OutcomeUnreachable
		res.Detail = fmt.Sprintf("not restored: %s could not be cloned (%s)", s.Remote, reason(err))
		return res, nil
	}

	// -B rewrites a branch pointer, which is only ever acceptable on a
	// directory gobag created seconds ago and nobody has touched.
	if s.Branch != "" {
		if _, err := local(dest, "checkout", "--quiet", "-B", s.Branch, s.Ref); err != nil {
			return res, fmt.Errorf("restoring %s at %s: %w", s.Dest, short(s.Ref), err)
		}
		// Tracking is a convenience; a clone with no matching upstream branch
		// is still a correct restore.
		_, _ = local(dest, "branch", "--quiet", "--set-upstream-to=origin/"+s.Branch, s.Branch)
	} else if _, err := local(dest, "checkout", "--quiet", "--detach", s.Ref); err != nil {
		return res, fmt.Errorf("restoring %s at %s: %w", s.Dest, short(s.Ref), err)
	}

	res.Ref, _ = head(dest)
	res.Outcome = OutcomeCloned
	res.Detail = fmt.Sprintf("cloned from %s at %s%s", s.Remote, short(res.Ref), onBranch(s.Branch))
	return res, nil
}

// advance moves an existing clean checkout forward to ref, or explains why it
// will not. fetch is injected because a repository and a linked worktree fetch
// through different directories.
func advance(dir, dest, ref, branch string, fetch func() error) (Result, error) {
	res := Result{Dest: dest}

	cur, err := head(dir)
	if err != nil {
		return res, fmt.Errorf("reading HEAD of %s: %w", dest, err)
	}
	res.Ref = cur

	if cur == ref {
		// Reported even when the tree is dirty: the pin is satisfied, and the
		// user's edits on top of it are not gobag's business.
		res.Outcome = OutcomeAlreadyAtRef
		res.Detail = fmt.Sprintf("already at %s%s", short(ref), onBranch(branch))
		return res, nil
	}

	d, err := dirty(dir)
	if err != nil {
		return res, err
	}
	if d {
		res.Outcome = OutcomeLeftDirty
		res.Detail = fmt.Sprintf("left untouched at %s: uncommitted changes, wanted %s", short(cur), short(ref))
		return res, nil
	}

	if _, err := resolve(dir, ref); err != nil {
		if ferr := fetch(); ferr != nil {
			res.Outcome = OutcomeUnreachable
			res.Detail = fmt.Sprintf("left at %s: could not fetch %s (%s)", short(cur), short(ref), reason(ferr))
			return res, nil
		}
		if _, err := resolve(dir, ref); err != nil {
			return res, fmt.Errorf("restoring %s: commit %s is not on the remote; push it and install again", dest, short(ref))
		}
	}

	if _, err := local(dir, "merge-base", "--is-ancestor", cur, ref); err != nil {
		res.Outcome = OutcomeLeftDiverged
		res.Detail = fmt.Sprintf("left untouched at %s: history does not fast-forward to %s", short(cur), short(ref))
		if _, aerr := local(dir, "merge-base", "--is-ancestor", ref, cur); aerr == nil {
			res.Detail = fmt.Sprintf("left untouched at %s: already ahead of the pinned %s", short(cur), short(ref))
		}
		return res, nil
	}

	// The move is a true fast-forward, but only when the checkout is arranged
	// the way the manifest expects. Anything else — a detached HEAD where a
	// branch was pinned, or a different branch checked out — means the user
	// has rearranged this repository, so gobag leaves it alone.
	at := branchOf(dir)
	switch {
	case branch == "" && at == "":
		if _, err := local(dir, "checkout", "--quiet", "--detach", ref); err != nil {
			return leftDirty(res, cur, ref, err), nil
		}
	case branch != "" && at == branch:
		if _, err := local(dir, "merge", "--quiet", "--ff-only", ref); err != nil {
			return leftDirty(res, cur, ref, err), nil
		}
	default:
		res.Outcome = OutcomeLeftDiverged
		res.Detail = fmt.Sprintf("left untouched at %s: checked out on %s, the bag pinned %s",
			short(cur), orDetached(at), orDetached(branch))
		return res, nil
	}

	res.Ref, _ = head(dir)
	res.Outcome = OutcomeFastForwarded
	res.Detail = fmt.Sprintf("fast-forwarded%s from %s to %s", onBranch(branch), short(cur), short(res.Ref))
	return res, nil
}

// leftDirty covers git refusing to move because the working tree holds
// something it will not overwrite — an untracked file in the way, most often.
// git already declined, so the checkout is untouched.
func leftDirty(res Result, cur, ref string, err error) Result {
	res.Outcome = OutcomeLeftDirty
	res.Detail = fmt.Sprintf("left untouched at %s: git would not move to %s (%s)", short(cur), short(ref), reason(err))
	return res
}

// EnsureWorktree recreates a linked worktree of the repository at
// root/parentDest, under the same never-destroy contract as EnsureRepo. An
// existing worktree is advanced only when it is clean and fast-forwardable.
func EnsureWorktree(root, parentDest string, w manifest.Worktree) (Result, error) {
	res := Result{Dest: w.Dest}
	parent, err := targetPath(root, parentDest)
	if err != nil {
		return res, err
	}
	dest, err := targetPath(root, w.Dest)
	if err != nil {
		return res, err
	}
	if w.Ref == "" {
		return res, fmt.Errorf("restoring worktree %s: no pinned ref in the manifest", w.Dest)
	}
	if !isRepo(parent) {
		return res, fmt.Errorf("restoring worktree %s: parent %s is not a git repository", w.Dest, parentDest)
	}

	// Drops administrative entries for worktrees whose directories are gone.
	// It removes no files and is what lets a deleted worktree be recreated.
	_, _ = local(parent, "worktree", "prune")

	// A worktree carries no objects of its own; anything missing has to be
	// fetched through the parent repository.
	name, _, err := remoteURL(parent)
	if err != nil {
		return res, fmt.Errorf("restoring worktree %s: %w", w.Dest, err)
	}
	fetch := fetcher(parent, parentDest, name)

	present, err := occupied(dest)
	if err != nil {
		return res, err
	}
	if present {
		if !isRepo(dest) {
			return res, fmt.Errorf("restoring worktree %s: %s already exists and is not a git checkout", w.Dest, dest)
		}
		return advance(dest, w.Dest, w.Ref, w.Branch, fetch)
	}

	if _, err := resolve(parent, w.Ref); err != nil {
		if ferr := fetch(); ferr != nil {
			res.Outcome = OutcomeUnreachable
			res.Detail = fmt.Sprintf("not restored: commit %s is missing and %s could not be fetched (%s)",
				short(w.Ref), parentDest, reason(ferr))
			return res, nil
		}
		if _, err := resolve(parent, w.Ref); err != nil {
			return res, fmt.Errorf("restoring worktree %s: commit %s is not on the remote; push it and install again",
				w.Dest, short(w.Ref))
		}
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return res, fmt.Errorf("preparing %s: %w", w.Dest, err)
	}

	detached := true
	if w.Branch != "" {
		switch sha, err := resolve(parent, "refs/heads/"+w.Branch); {
		case err != nil:
			// Branch does not exist yet: create it at the pinned commit.
			if _, aerr := local(parent, "worktree", "add", "--quiet", "-b", w.Branch, dest, w.Ref); aerr == nil {
				detached = false
			}
		case sha == w.Ref:
			// Branch exists at exactly the pinned commit: reuse it rather than
			// inventing a second name. Fails harmlessly if it is checked out
			// somewhere else, in which case the detached fallback applies.
			if _, aerr := local(parent, "worktree", "add", "--quiet", dest, w.Branch); aerr == nil {
				detached = false
			}
		}
		// A branch of that name pointing elsewhere is left exactly where it
		// is; moving it would discard whatever the user put there.
	}
	if detached {
		if _, err := local(parent, "worktree", "add", "--quiet", "--detach", dest, w.Ref); err != nil {
			return res, fmt.Errorf("creating worktree %s: %w", w.Dest, err)
		}
	}

	res.Ref, _ = head(dest)
	res.Outcome = OutcomeCreated
	if detached {
		res.Detail = fmt.Sprintf("worktree created at %s, detached", short(res.Ref))
	} else {
		res.Detail = fmt.Sprintf("worktree created at %s on branch %s", short(res.Ref), w.Branch)
	}
	return res, nil
}

// targetPath resolves an archive-relative destination under the install root.
// A manifest is untrusted input, so the destination is validated before it is
// ever joined onto a real path.
func targetPath(root, dest string) (string, error) {
	if err := plan.ValidateDest(dest); err != nil {
		return "", fmt.Errorf("destination %q cannot be restored: %w", dest, err)
	}
	return filepath.Join(root, filepath.FromSlash(dest)), nil
}

// fetcher returns a fetch closure that updates remote-tracking refs by remote
// name, so later reachability checks see what the fetch brought in.
func fetcher(dir, dest, remote string) func() error {
	return func() error {
		if remote == "" {
			return fmt.Errorf("%s has no remote to fetch from", dest)
		}
		_, err := network(dir, "fetch", "--quiet", "--tags", remote)
		return err
	}
}

// occupied reports whether dest holds anything. An empty directory counts as
// missing, because git will happily clone into one.
func occupied(dest string) (bool, error) {
	fi, err := os.Lstat(dest)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading %s: %w", dest, err)
	}
	if !fi.IsDir() {
		return true, nil // the caller reports what is actually in the way
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", dest, err)
	}
	return len(entries) > 0, nil
}

func reason(err error) string {
	if timedOut(err) {
		return fmt.Sprintf("timed out after %s", NetworkTimeout)
	}
	return firstLine(err.Error())
}

func onBranch(branch string) string {
	if branch == "" {
		return ""
	}
	return " on " + branch
}

func orDetached(branch string) string {
	if branch == "" {
		return "a detached HEAD"
	}
	return branch
}

func orNone(url string) string {
	if url == "" {
		return "no remote"
	}
	return url
}
