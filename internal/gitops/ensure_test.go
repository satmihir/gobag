package gitops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/satmihir/gobag/internal/manifest"
	"github.com/satmihir/gobag/internal/testutil"
)

// installed builds the manifest source a bag would carry for one fixture repo.
func installed(t *testing.T, ws *testutil.Workspace, name, branch string) manifest.Source {
	t.Helper()
	return manifest.Source{
		Dest:   "repos/" + name,
		Remote: ws.Repos[name].RemoteURL,
		Ref:    ws.Repos[name].Head(t),
		Branch: branch,
	}
}

func mustEnsureRepo(t *testing.T, root string, s manifest.Source) Result {
	t.Helper()
	r, err := EnsureRepo(root, s)
	if err != nil {
		t.Fatalf("EnsureRepo(%s): %v", s.Dest, err)
	}
	return r
}

func mustEnsureWorktree(t *testing.T, root, parent string, w manifest.Worktree) Result {
	t.Helper()
	r, err := EnsureWorktree(root, parent, w)
	if err != nil {
		t.Fatalf("EnsureWorktree(%s): %v", w.Dest, err)
	}
	return r
}

// TestEnsureRepoIsIdempotent is the load-bearing test for this package: the
// second run of every Ensure path must change nothing at all.
func TestEnsureRepoIsIdempotent(t *testing.T) {
	tests := []struct {
		name   string
		branch string
	}{
		{name: "on a branch", branch: "main"},
		{name: "detached", branch: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ws := fixture(t)
			root := installRoot(t)
			s := installed(t, ws, "frontend", tc.branch)
			dir := filepath.Join(root, "repos", "frontend")

			first := mustEnsureRepo(t, root, s)
			if first.Outcome != OutcomeCloned {
				t.Fatalf("first run outcome = %q (%s)", first.Outcome, first.Detail)
			}
			if first.Ref != s.Ref {
				t.Fatalf("cloned at %s, want %s", first.Ref, s.Ref)
			}
			if got := branchOf(dir); got != tc.branch {
				t.Fatalf("checked out branch %q, want %q", got, tc.branch)
			}
			if first.Detail == "" {
				t.Fatal("no detail line for orientation")
			}

			before := treeHash(t, dir)

			second := mustEnsureRepo(t, root, s)
			if second.Outcome != OutcomeAlreadyAtRef {
				t.Fatalf("second run outcome = %q (%s), want %q", second.Outcome, second.Detail, OutcomeAlreadyAtRef)
			}
			if second.Ref != s.Ref {
				t.Fatalf("second run ref = %s, want %s", second.Ref, s.Ref)
			}
			if after := treeHash(t, dir); after != before {
				t.Fatal("second run changed the working tree")
			}
			if got := branchOf(dir); got != tc.branch {
				t.Fatalf("second run moved off branch %q to %q", tc.branch, got)
			}
		})
	}
}

func TestEnsureRepoFastForwardsThenNoOps(t *testing.T) {
	ws := fixture(t)
	root := installRoot(t)
	dir := filepath.Join(root, "repos", "frontend")

	s := installed(t, ws, "frontend", "main")
	pinned := s.Ref
	mustEnsureRepo(t, root, s)

	// The world moves on, and a later bag pins the newer commit.
	ws.AdvanceRemote("frontend", 3)
	s.Ref = remoteHead(t, ws, "frontend", "main")
	if s.Ref == pinned {
		t.Fatal("fixture did not advance the remote")
	}

	moved := mustEnsureRepo(t, root, s)
	if moved.Outcome != OutcomeFastForwarded {
		t.Fatalf("outcome = %q (%s), want %q", moved.Outcome, moved.Detail, OutcomeFastForwarded)
	}
	if moved.Ref != s.Ref {
		t.Fatalf("ref = %s, want %s", moved.Ref, s.Ref)
	}
	if got := branchOf(dir); got != "main" {
		t.Fatalf("fast-forward left the checkout on %q", got)
	}

	before := treeHash(t, dir)
	again := mustEnsureRepo(t, root, s)
	if again.Outcome != OutcomeAlreadyAtRef {
		t.Fatalf("second run outcome = %q (%s)", again.Outcome, again.Detail)
	}
	if after := treeHash(t, dir); after != before {
		t.Fatal("second run changed the working tree")
	}
}

// TestEnsureRepoRefusesToTouchWork covers every path where gobag declines to
// act. In each case the checkout must come out byte-identical.
func TestEnsureRepoRefusesToTouchWork(t *testing.T) {
	tests := []struct {
		name string
		// arrange runs after the initial clone and returns the ref the second
		// EnsureRepo should be asked for, and the remote it should claim.
		arrange func(t *testing.T, ws *testutil.Workspace, dir string, s *manifest.Source)
		want    Outcome
		detail  string
	}{
		{
			name: "uncommitted changes",
			arrange: func(t *testing.T, ws *testutil.Workspace, dir string, s *manifest.Source) {
				ws.AdvanceRemote("frontend", 2)
				s.Ref = remoteHead(t, ws, "frontend", "main")
				writeInto(t, filepath.Join(dir, "README.md"), "# frontend\n\nlocal edits\n")
			},
			want:   OutcomeLeftDirty,
			detail: "uncommitted changes",
		},
		{
			name: "history has diverged",
			arrange: func(t *testing.T, ws *testutil.Workspace, dir string, s *manifest.Source) {
				ws.AdvanceRemote("frontend", 2)
				s.Ref = remoteHead(t, ws, "frontend", "main")
				writeInto(t, filepath.Join(dir, "local.txt"), "mine\n")
				testutil.Git(t, dir, "add", ".")
				testutil.Git(t, dir, "commit", "-q", "-m", "local work")
			},
			want:   OutcomeLeftDiverged,
			detail: "does not fast-forward",
		},
		{
			name: "checkout points at a different remote",
			arrange: func(t *testing.T, ws *testutil.Workspace, _ string, s *manifest.Source) {
				s.Remote = ws.Repos["backend"].RemoteURL
				s.Ref = ws.Repos["backend"].Head(t)
			},
			want:   OutcomeRemoteMismatch,
			detail: "left untouched",
		},
		{
			name: "remote vanished",
			arrange: func(t *testing.T, ws *testutil.Workspace, _ string, s *manifest.Source) {
				ws.AdvanceRemote("frontend", 2)
				s.Ref = remoteHead(t, ws, "frontend", "main")
				if err := os.RemoveAll(filepath.Join(ws.RemotesDir, "frontend.git")); err != nil {
					t.Fatal(err)
				}
			},
			want:   OutcomeUnreachable,
			detail: "could not fetch",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ws := fixture(t)
			root := installRoot(t)
			dir := filepath.Join(root, "repos", "frontend")

			s := installed(t, ws, "frontend", "main")
			mustEnsureRepo(t, root, s)

			tc.arrange(t, ws, dir, &s)
			startHead, err := head(dir)
			if err != nil {
				t.Fatal(err)
			}
			before := treeHash(t, dir)

			got := mustEnsureRepo(t, root, s)
			if got.Outcome != tc.want {
				t.Fatalf("outcome = %q (%s), want %q", got.Outcome, got.Detail, tc.want)
			}
			if !strings.Contains(got.Detail, tc.detail) {
				t.Errorf("detail %q does not mention %q", got.Detail, tc.detail)
			}
			if after := treeHash(t, dir); after != before {
				t.Fatal("the checkout was modified despite the refusal")
			}
			if now, _ := head(dir); now != startHead {
				t.Fatalf("HEAD moved from %s to %s", startHead, now)
			}
			// Refusing is not failing: a second identical call must behave the
			// same way, so install stays convergent.
			repeat := mustEnsureRepo(t, root, s)
			if repeat.Outcome != tc.want {
				t.Fatalf("repeat outcome = %q, want %q", repeat.Outcome, tc.want)
			}
		})
	}
}

func TestEnsureRepoUnreachableRemoteIsNotAFailure(t *testing.T) {
	ws := fixture(t)
	root := installRoot(t)

	s := installed(t, ws, "frontend", "main")
	s.Remote = "file://" + filepath.Join(ws.RemotesDir, "never-existed.git")

	got, err := EnsureRepo(root, s)
	if err != nil {
		t.Fatalf("an unreachable remote must not abort the install: %v", err)
	}
	if got.Outcome != OutcomeUnreachable {
		t.Fatalf("outcome = %q (%s), want %q", got.Outcome, got.Detail, OutcomeUnreachable)
	}
	if entries, err := os.ReadDir(filepath.Join(root, "repos", "frontend")); err == nil && len(entries) > 0 {
		t.Fatalf("a failed clone left %d entries behind", len(entries))
	}
}

func TestEnsureRepoErrors(t *testing.T) {
	ws := fixture(t)
	root := installRoot(t)

	t.Run("destination escapes the root", func(t *testing.T) {
		s := installed(t, ws, "frontend", "main")
		s.Dest = "../outside"
		if _, err := EnsureRepo(root, s); err == nil {
			t.Fatal("expected an error for an escaping destination")
		}
	})

	t.Run("something else is already there", func(t *testing.T) {
		dir := filepath.Join(root, "repos", "occupied")
		writeInto(t, filepath.Join(dir, "notes.txt"), "not a repo\n")

		s := installed(t, ws, "frontend", "main")
		s.Dest = "repos/occupied"
		_, err := EnsureRepo(root, s)
		if err == nil || !strings.Contains(err.Error(), "not a git repository") {
			t.Fatalf("err = %v, want a plain-language complaint about the directory", err)
		}
	})

	t.Run("manifest has no ref", func(t *testing.T) {
		s := installed(t, ws, "frontend", "main")
		s.Dest = "repos/refless"
		s.Ref = ""
		if _, err := EnsureRepo(root, s); err == nil {
			t.Fatal("expected an error for a source with no pinned ref")
		}
	})
}

func TestEnsureWorktreeIsIdempotent(t *testing.T) {
	tests := []struct {
		name   string
		branch string
	}{
		{name: "on a branch", branch: "wip"},
		{name: "detached", branch: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ws := fixture(t)
			root := installRoot(t)

			parent := installed(t, ws, "frontend", "main")
			mustEnsureRepo(t, root, parent)

			w := manifest.Worktree{
				Dest:   "repos/frontend-wip",
				Ref:    ws.Repos["frontend-wip"].Head(t),
				Branch: tc.branch,
			}
			dir := filepath.Join(root, "repos", "frontend-wip")

			first := mustEnsureWorktree(t, root, parent.Dest, w)
			if first.Outcome != OutcomeCreated {
				t.Fatalf("first run outcome = %q (%s)", first.Outcome, first.Detail)
			}
			if first.Ref != w.Ref {
				t.Fatalf("worktree at %s, want %s", first.Ref, w.Ref)
			}
			if got := branchOf(dir); got != tc.branch {
				t.Fatalf("worktree branch = %q, want %q", got, tc.branch)
			}
			// A linked worktree, not a second clone.
			if fi, err := os.Stat(filepath.Join(dir, ".git")); err != nil || fi.IsDir() {
				t.Fatalf(".git is not a worktree pointer file: %v", err)
			}

			before := treeHash(t, dir)
			second := mustEnsureWorktree(t, root, parent.Dest, w)
			if second.Outcome != OutcomeAlreadyAtRef {
				t.Fatalf("second run outcome = %q (%s), want %q", second.Outcome, second.Detail, OutcomeAlreadyAtRef)
			}
			if after := treeHash(t, dir); after != before {
				t.Fatal("second run changed the worktree")
			}
		})
	}
}

func TestEnsureWorktreeRecreatesAfterDeletion(t *testing.T) {
	ws := fixture(t)
	root := installRoot(t)

	parent := installed(t, ws, "frontend", "main")
	mustEnsureRepo(t, root, parent)

	w := manifest.Worktree{Dest: "repos/frontend-wip", Ref: ws.Repos["frontend-wip"].Head(t), Branch: "wip"}
	mustEnsureWorktree(t, root, parent.Dest, w)

	dir := filepath.Join(root, "repos", "frontend-wip")
	before := treeHash(t, dir)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	// git still has the worktree registered; converging means pruning that
	// stale registration rather than giving up.
	again := mustEnsureWorktree(t, root, parent.Dest, w)
	if again.Outcome != OutcomeCreated {
		t.Fatalf("outcome = %q (%s), want %q", again.Outcome, again.Detail, OutcomeCreated)
	}
	if after := treeHash(t, dir); after != before {
		t.Fatal("the recreated worktree differs from the original")
	}
}

func TestEnsureWorktreeLeavesWorkAlone(t *testing.T) {
	ws := fixture(t)
	root := installRoot(t)

	parent := installed(t, ws, "frontend", "main")
	mustEnsureRepo(t, root, parent)

	w := manifest.Worktree{Dest: "repos/frontend-wip", Ref: ws.Repos["frontend-wip"].Head(t)}
	mustEnsureWorktree(t, root, parent.Dest, w)

	dir := filepath.Join(root, "repos", "frontend-wip")
	writeInto(t, filepath.Join(dir, "README.md"), "# frontend\n\nmid-thought\n")

	ws.AdvanceRemote("frontend", 2)
	w.Ref = remoteHead(t, ws, "frontend", "main")

	before := treeHash(t, dir)
	got := mustEnsureWorktree(t, root, parent.Dest, w)
	if got.Outcome != OutcomeLeftDirty {
		t.Fatalf("outcome = %q (%s), want %q", got.Outcome, got.Detail, OutcomeLeftDirty)
	}
	if after := treeHash(t, dir); after != before {
		t.Fatal("the worktree was modified despite the refusal")
	}
}

func TestEnsureWorktreeErrors(t *testing.T) {
	ws := fixture(t)
	root := installRoot(t)

	w := manifest.Worktree{Dest: "repos/frontend-wip", Ref: ws.Repos["frontend-wip"].Head(t), Branch: "wip"}
	_, err := EnsureWorktree(root, "repos/frontend", w)
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("err = %v, want a complaint about the missing parent", err)
	}
}

func writeInto(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
