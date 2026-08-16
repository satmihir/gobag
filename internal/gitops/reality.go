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
	// RemoteHead is the commit the pinned branch (or the remote's HEAD, for a
	// detached pin) points at right now. Empty when the remote did not answer.
	RemoteHead string
	// Ahead is how many commits RemoteHead has that PinnedRef does not. It is
	// zero whenever the count cannot be established locally.
	Ahead int
	// Unreachable means the remote could not be consulted; the rest of the
	// struct carries nothing new.
	Unreachable bool
}

// RemoteReality asks the remote what has happened since the bag was packed.
// The remote being down is an expected outcome, not an error: install runs on
// whatever network the traveller landed on.
func RemoteReality(root string, s manifest.Source) (Reality, error) {
	r := Reality{Dest: s.Dest, PinnedRef: s.Ref}
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
