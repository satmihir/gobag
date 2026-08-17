package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/satmihir/gobag/internal/gitops"
	"github.com/satmihir/gobag/internal/testutil"
)

// useMachineRegistry points the per-machine registry at a temp file so tests
// never touch the developer's own.
func useMachineRegistry(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "machine.json")
	t.Setenv("GOBAG_MACHINE_FILE", path)
	return path
}

// tinyExternalThreshold makes every fixture repository count as "too large to
// clone", so the external path can be exercised without a 30GB fixture.
func tinyExternalThreshold(t *testing.T) {
	t.Helper()
	prev := gitops.ExternalThreshold
	gitops.ExternalThreshold = 1 // every repo has more than one byte of objects
	t.Cleanup(func() { gitops.ExternalThreshold = prev })
}

// The monorepo case end to end: pack marks it external, install refuses to
// clone it and says so, and link attaches it to a clone already on the box.
func TestExternalRepoIsLinkedNotCloned(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to git")
	}
	useMachineRegistry(t)
	tinyExternalThreshold(t)

	ws := testutil.NewWorkspace(t)
	archivePath := filepath.Join(t.TempDir(), "big.gobag")

	code, out := cli(t, "pack", ws.Root, "-o", archivePath, "-plaintext")
	if code != 0 {
		t.Fatalf("pack exited %d:\n%s", code, out)
	}
	if !strings.Contains(out, "external reference") {
		t.Errorf("pack should announce the external decision:\n%s", out)
	}
	if !strings.Contains(out, "gobag link") {
		t.Errorf("pack should name the command that finishes the job:\n%s", out)
	}

	// Restore on a machine with no registry entry: nothing is cloned for the
	// external repos, and the install still succeeds.
	target := filepath.Join(t.TempDir(), "restored")
	code, out = cli(t, "install", archivePath, "-root", target)
	if code != 0 {
		t.Fatalf("install exited %d:\n%s", code, out)
	}
	if !strings.Contains(out, string(gitops.OutcomeUnlinked)) {
		t.Errorf("install should report the external repo as not-linked:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(target, "repos", "frontend", ".git")); err == nil {
		t.Fatal("an external repository must never be cloned by install")
	}

	orientation := readFile(t, filepath.Join(target, "ORIENTATION.md"))
	if !strings.Contains(orientation, "gobag link repos/frontend") {
		t.Errorf("orientation should carry the exact link command:\n%s", orientation)
	}

	// Now point it at the clone this machine already has.
	code, out = cli(t, "link", "repos/frontend", ws.Repos["frontend"].Path, "-root", target)
	if code != 0 {
		t.Fatalf("link exited %d:\n%s", code, out)
	}
	if !strings.Contains(out, string(gitops.OutcomeLinked)) {
		t.Errorf("link should report success:\n%s", out)
	}

	// The workspace has a checkout at the pinned ref...
	linked := filepath.Join(target, "repos", "frontend")
	if got, want := testutil.Git(t, linked, "rev-parse", "HEAD"), ws.Repos["frontend"].Head(t); got != want {
		t.Errorf("linked checkout is at %s, want the pinned %s", got, want)
	}
	// ...sharing the existing clone's object store rather than a copy of it.
	dotGit := filepath.Join(linked, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		t.Fatalf("linked checkout has no .git: %v", err)
	}
	if info.IsDir() {
		t.Error("linked repo has its own object store — it was cloned, not linked")
	}
	if list := testutil.Git(t, ws.Repos["frontend"].Path, "worktree", "list"); !strings.Contains(list, linked) {
		t.Errorf("the existing clone does not know about the new worktree:\n%s", list)
	}
}

// Once a clone is recorded, later restores on this machine link it unprompted.
func TestRegistryLinksOnLaterInstalls(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to git")
	}
	useMachineRegistry(t)
	tinyExternalThreshold(t)

	ws := testutil.NewWorkspace(t)
	archivePath := filepath.Join(t.TempDir(), "big.gobag")
	if code, out := cli(t, "pack", ws.Root, "-o", archivePath, "-plaintext"); code != 0 {
		t.Fatalf("pack exited %d:\n%s", code, out)
	}

	first := filepath.Join(t.TempDir(), "first")
	if code, out := cli(t, "install", archivePath, "-root", first); code != 0 {
		t.Fatalf("install exited %d:\n%s", code, out)
	}
	if code, out := cli(t, "link", "repos/frontend", ws.Repos["frontend"].Path, "-root", first); code != 0 {
		t.Fatalf("link exited %d:\n%s", code, out)
	}

	// A completely separate restore, no linking step this time.
	second := filepath.Join(t.TempDir(), "second")
	code, out := cli(t, "install", archivePath, "-root", second)
	if code != 0 {
		t.Fatalf("second install exited %d:\n%s", code, out)
	}
	if !strings.Contains(out, string(gitops.OutcomeLinked)) {
		t.Errorf("the remembered clone should have been linked automatically:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(second, "repos", "frontend", "src", "app.js")); err != nil {
		t.Errorf("automatically linked checkout is missing content: %v", err)
	}
}

// Pointing gobag at the wrong repository is the mistake worth catching, since
// everything downstream would otherwise succeed and be built on the wrong code.
func TestLinkRejectsMismatchedClone(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to git")
	}
	useMachineRegistry(t)
	tinyExternalThreshold(t)

	ws := testutil.NewWorkspace(t)
	archivePath := filepath.Join(t.TempDir(), "big.gobag")
	if code, out := cli(t, "pack", ws.Root, "-o", archivePath, "-plaintext"); code != 0 {
		t.Fatalf("pack exited %d:\n%s", code, out)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if code, out := cli(t, "install", archivePath, "-root", target); code != 0 {
		t.Fatalf("install exited %d:\n%s", code, out)
	}

	// backend is a real repository, but not the one repos/frontend names.
	code, out := cli(t, "link", "repos/frontend", ws.Repos["backend"].Path, "-root", target)
	if code != 1 {
		t.Fatalf("linking the wrong repository should exit 1, got %d:\n%s", code, out)
	}
	if !strings.Contains(out, "expects") {
		t.Errorf("the error should name the mismatch:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(target, "repos", "frontend", ".git")); err == nil {
		t.Error("a rejected link must leave nothing behind")
	}
}

func TestLinkUsageErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to git")
	}
	useMachineRegistry(t)

	// Not a restored workspace at all.
	if code, _ := cli(t, "link", "repos/x", t.TempDir(), "-root", t.TempDir()); code != 1 {
		t.Error("linking outside a restored workspace should exit 1")
	}
	// Wrong arity.
	if code, _ := cli(t, "link", "repos/x"); code != 1 {
		t.Error("link with one argument should exit 1")
	}
}

// A normal-sized repository must be unaffected: still cloned, never linked.
func TestSmallRepoStaysCloned(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to git")
	}
	useMachineRegistry(t) // threshold left at its real value

	ws := testutil.NewWorkspace(t)
	archivePath := filepath.Join(t.TempDir(), "small.gobag")
	if code, out := cli(t, "pack", ws.Root, "-o", archivePath, "-plaintext"); code != 0 {
		t.Fatalf("pack exited %d:\n%s", code, out)
	}
	target := filepath.Join(t.TempDir(), "restored")
	code, out := cli(t, "install", archivePath, "-root", target)
	if code != 0 {
		t.Fatalf("install exited %d:\n%s", code, out)
	}
	if strings.Contains(out, string(gitops.OutcomeUnlinked)) {
		t.Errorf("a small repository should be cloned, not treated as external:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(target, "repos", "frontend", ".git")); err != nil {
		t.Errorf("small repository was not cloned: %v", err)
	}
}
