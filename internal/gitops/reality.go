package gitops

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/satmihir/gobag/internal/manifest"
)

// Reality is the gap between what a bag pinned and what the remote holds now.
// It is what turns "restored at 3f9c2ab" into "main advanced 14 commits while
// this bag was in transit".
type Reality struct {
	Dest      string
	PinnedRef string
	// PinnedBranch is the branch the archive pinned, empty for a detached pin.
	PinnedBranch string
	// RemoteHead is the commit the pinned branch (or the remote's HEAD, for a
	// detached pin) points at right now. Empty when the remote did not answer.
	RemoteHead string
	// Ahead is how many commits RemoteHead has that PinnedRef does not. It is
	// zero whenever the count cannot be established locally.
	Ahead int
	// Unreachable means the remote could not be consulted; the rest of the
	// struct carries nothing new.
	Unreachable bool

	// BaseBranch is the remote's default branch, set only when the pin is on
	// some other branch. A checkpoint taken mid-pull-request pins a feature
	// branch, and "the tip has not moved" is then true and useless: the
	// question that matters is how far the base moved underneath it.
	BaseBranch string
	// BaseAhead is commits the pinned ref has that BaseBranch lacks.
	BaseAhead int
	// BaseBehind is commits BaseBranch has that the pinned ref lacks. This is
	// the number that silently invalidates a handoff.
	BaseBehind int
}

// RemoteReality asks the remote what has happened since the bag was packed.
// The remote being down is an expected outcome, not an error: install runs on
// whatever network the traveller landed on.
func RemoteReality(root string, s manifest.Source) (Reality, error) {
	r := Reality{Dest: s.Dest, PinnedRef: s.Ref, PinnedBranch: s.Branch}
	dest, err := targetPath(root, s.Dest)
	if err != nil {
		return r, err
	}
	if s.Remote == "" {
		r.Unreachable = true
		return r, nil
	}

	// Run from inside the checkout when there is one, so any repository-local
	// url rewriting or credential configuration applies.
	dir := ""
	inRepo := isRepo(dest)
	if inRepo {
		dir = dest
	}

	want := "HEAD"
	if s.Branch != "" {
		want = "refs/heads/" + s.Branch
	}
	out, err := network(dir, "ls-remote", "--quiet", s.Remote, want)
	if err != nil {
		r.Unreachable = true
		return r, nil
	}
	r.RemoteHead = firstField(out)

	// Base drift is computed even when the tip has not moved — that is exactly
	// the case where it is the only thing worth saying.
	if inRepo {
		r.BaseBranch, r.BaseAhead, r.BaseBehind = baseDrift(dir, s)
	}

	if r.RemoteHead == "" || r.RemoteHead == s.Ref || !inRepo {
		return r, nil
	}

	// Counting requires both commits locally. A fetch failure here only costs
	// the count, not the reality check.
	if _, err := resolve(dir, r.RemoteHead); err != nil {
		name, _, nerr := remoteURL(dir)
		if nerr != nil {
			return r, nil
		}
		if err := fetcher(dir, s.Dest, name)(); err != nil {
			return r, nil
		}
		if _, err := resolve(dir, r.RemoteHead); err != nil {
			return r, nil
		}
	}
	if _, err := resolve(dir, s.Ref); err != nil {
		return r, nil
	}

	count, err := local(dir, "rev-list", "--count", s.Ref+".."+r.RemoteHead)
	if err != nil {
		return r, fmt.Errorf("counting commits ahead of %s in %s: %w", short(s.Ref), s.Dest, err)
	}
	n, err := strconv.Atoi(count)
	if err != nil {
		return r, fmt.Errorf("counting commits ahead of %s in %s: unexpected git output %q", short(s.Ref), s.Dest, count)
	}
	r.Ahead = n
	return r, nil
}

// firstField returns the sha column of the first ls-remote line.
func firstField(out string) string {
	for _, l := range lines(out) {
		if f := strings.Fields(l); len(f) > 0 {
			return f[0]
		}
	}
	return ""
}

// baseDrift measures the pinned ref against the remote's default branch.
//
// It answers the question a mid-pull-request checkpoint actually raises: not
// "did my branch move" but "how much has main moved under me while this bag
// was in transit". Returns an empty branch name when there is nothing to say —
// no default branch, or the pin is already on it.
func baseDrift(dir string, s manifest.Source) (branch string, ahead, behind int) {
	if s.Branch == "" {
		// A detached pin has no branch to compare; the tip diff already covers
		// everything that can be said about it.
		return "", 0, 0
	}

	base, baseSha := remoteDefaultBranch(dir, s.Remote)
	if base == "" || base == s.Branch || baseSha == "" {
		return "", 0, 0
	}

	// Both commits must be present locally to be counted. A fetch failure
	// costs the count, never the restore.
	if _, err := resolve(dir, baseSha); err != nil {
		name, _, err := remoteURL(dir)
		if err != nil {
			return "", 0, 0
		}
		if err := fetcher(dir, s.Dest, name)(); err != nil {
			return "", 0, 0
		}
		if _, err := resolve(dir, baseSha); err != nil {
			return "", 0, 0
		}
	}
	if _, err := resolve(dir, s.Ref); err != nil {
		return "", 0, 0
	}

	out, err := local(dir, "rev-list", "--left-right", "--count", s.Ref+"..."+baseSha)
	if err != nil {
		return "", 0, 0
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return "", 0, 0
	}
	a, err1 := strconv.Atoi(fields[0])
	b, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil {
		return "", 0, 0
	}
	return base, a, b
}

// remoteDefaultBranch asks the remote which branch its HEAD points at, and
// what that branch is at right now.
func remoteDefaultBranch(dir, remote string) (branch, sha string) {
	out, err := network(dir, "ls-remote", "--symref", remote, "HEAD")
	if err != nil {
		return "", ""
	}
	for _, line := range lines(out) {
		if rest, ok := strings.CutPrefix(line, "ref:"); ok {
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				branch = strings.TrimPrefix(fields[0], "refs/heads/")
			}
			continue
		}
		if fields := strings.Fields(line); len(fields) >= 2 && fields[1] == "HEAD" {
			sha = fields[0]
		}
	}
	return branch, sha
}
