package gitops

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/satmihir/gobag/internal/plan"
	"github.com/satmihir/gobag/internal/testutil"
)

// fixture builds the standard workspace and shortens the network timeout.
// Every test in this package drives a real git binary against file:// remotes,
// which is slow enough to sit behind -short.
func fixture(t *testing.T) *testutil.Workspace {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping git integration test in short mode")
	}
	testutil.RequireGit(t)

	prev := NetworkTimeout
	NetworkTimeout = 20 * time.Second
	t.Cleanup(func() { NetworkTimeout = prev })

	return testutil.NewWorkspace(t)
}

// installRoot returns an empty directory to restore into, with symlinks
// resolved so paths compare equal to what git reports.
func installRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	return dir
}

// treeHash fingerprints a working tree: every path, mode, and byte, excluding
// .git so that index timestamps and reflogs do not masquerade as changes.
func treeHash(t *testing.T, dir string) string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			entries = append(entries, "d "+rel)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		entries = append(entries, fmt.Sprintf("f %s %o %x", rel, info.Mode().Perm(), sha256.Sum256(b)))
		return nil
	})
	if err != nil {
		t.Fatalf("hashing %s: %v", dir, err)
	}
	sort.Strings(entries)
	return strings.Join(entries, "\n")
}

// remoteHead reads the current tip of branch on a repository's remote.
func remoteHead(t *testing.T, ws *testutil.Workspace, repo, branch string) string {
	t.Helper()
	out := testutil.Git(t, ws.Root, "ls-remote", ws.Repos[repo].RemoteURL, "refs/heads/"+branch)
	fields := strings.Fields(out)
	if len(fields) == 0 {
		t.Fatalf("no %s on the remote of %s", branch, repo)
	}
	return fields[0]
}

// sourceFor finds a discovered source by destination.
func sourceFor(t *testing.T, p *plan.Plan, dest string) plan.Source {
	t.Helper()
	for _, s := range p.Sources {
		if s.Dest == dest {
			return s
		}
	}
	t.Fatalf("no source at %q; got %v", dest, dests(p))
	return plan.Source{}
}

func dests(p *plan.Plan) []string {
	var out []string
	for _, s := range p.Sources {
		out = append(out, s.Dest)
	}
	return out
}

// findProblem asserts that exactly one problem of the given severity mentions
// substr at dest.
func findProblem(t *testing.T, ps []plan.Problem, sev plan.Severity, dest, substr string) plan.Problem {
	t.Helper()
	var hits []plan.Problem
	for _, p := range ps {
		if p.Severity == sev && p.Dest == dest && strings.Contains(p.Message, substr) {
			hits = append(hits, p)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("want exactly one %s at %q mentioning %q, got %d of them in %v", sev, dest, substr, len(hits), ps)
	}
	return hits[0]
}

func noProblem(t *testing.T, ps []plan.Problem, sev plan.Severity, dest, substr string) {
	t.Helper()
	for _, p := range ps {
		if p.Severity == sev && p.Dest == dest && strings.Contains(p.Message, substr) {
			t.Fatalf("unexpected %s at %q: %s", sev, dest, p.Message)
		}
	}
}

func TestSameRemote(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"git@github.com:org/x.git", "git@github.com:org/x", true},
		{"https://h/org/x.git/", "https://h/org/x", true},
		{"https://h/org/x", "https://h/org/y", false},
		// A different transport to the same repository still counts as a
		// mismatch: the safe response is to touch nothing.
		{"git@github.com:org/x.git", "https://github.com/org/x.git", false},
		{"", "https://h/org/x", false},
	}
	for _, tc := range tests {
		if got := sameRemote(tc.a, tc.b); got != tc.want {
			t.Errorf("sameRemote(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
