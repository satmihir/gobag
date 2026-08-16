package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/satmihir/gobag/internal/archive"
	"github.com/satmihir/gobag/internal/claudestate"
	"github.com/satmihir/gobag/internal/plan"
	"github.com/satmihir/gobag/internal/testutil"
)

// cli runs a gobag command the way a user would, returning the exit code and
// combined output.
func cli(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := run(args, &out, &out)
	return code, out.String()
}

// packFixture builds a fixture workspace and packs it, returning the workspace
// and the archive path.
func packFixture(t *testing.T) (*testutil.Workspace, string) {
	t.Helper()
	ws := testutil.NewWorkspace(t)
	archivePath := filepath.Join(t.TempDir(), "teammate.gobag")

	code, out := cli(t, "pack", ws.Root, "-o", archivePath, "-plaintext")
	if code != 0 {
		t.Fatalf("pack exited %d:\n%s", code, out)
	}
	return ws, archivePath
}

// The round trip is the whole product: pack a workspace on one machine,
// restore it somewhere else, and land oriented.
func TestPackInstallRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to git")
	}
	ws, archivePath := packFixture(t)

	// The world moves while the bag is packed.
	ws.AdvanceRemote("frontend", 14)

	target := filepath.Join(t.TempDir(), "restored")
	code, out := cli(t, "install", archivePath, "-root", target)
	if code != 0 {
		t.Fatalf("install exited %d:\n%s", code, out)
	}

	// Repositories were materialized from their remotes at the pinned refs.
	for name, want := range map[string]string{
		"repos/frontend": ws.Repos["frontend"].Head(t),
		"repos/backend":  ws.Repos["backend"].Head(t),
	} {
		dir := filepath.Join(target, name)
		if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
			t.Fatalf("%s was not restored: %v", name, err)
		}
		if got := testutil.Git(t, dir, "rev-parse", "HEAD"); got != want {
			t.Errorf("%s is at %s, want the pinned ref %s", name, got, want)
		}
	}

	// The linked worktree was recreated, not cloned as a separate repository.
	wt := filepath.Join(target, "repos", "frontend-wip")
	if info, err := os.Stat(filepath.Join(wt, ".git")); err != nil {
		t.Fatalf("worktree not restored: %v", err)
	} else if info.IsDir() {
		t.Error("worktree .git should be a file pointing at its parent repository")
	}

	// Context travelled and orientation was generated.
	if _, err := os.Stat(filepath.Join(target, "context", "context.md")); err != nil {
		t.Errorf("context document did not travel: %v", err)
	}
	orientation := readFile(t, filepath.Join(target, "ORIENTATION.md"))
	for _, want := range []string{
		"# Orientation",
		"## Restored",
		"## Since you were packed",
		"repos/frontend",
		"advanced 14 commits", // the reality diff — the reason this tool exists
	} {
		if !strings.Contains(orientation, want) {
			t.Errorf("ORIENTATION.md is missing %q\n---\n%s", want, orientation)
		}
	}
}

// Installing twice must converge: no duplicates, no destruction, no churn.
func TestInstallIsIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to git")
	}
	_, archivePath := packFixture(t)
	target := filepath.Join(t.TempDir(), "restored")

	if code, out := cli(t, "install", archivePath, "-root", target); code != 0 {
		t.Fatalf("first install exited %d:\n%s", code, out)
	}
	first := readFile(t, filepath.Join(target, "context", "context.md"))

	code, out := cli(t, "install", archivePath, "-root", target)
	if code != 0 {
		t.Fatalf("second install exited %d:\n%s", code, out)
	}
	if !strings.Contains(out, "already-at-ref") {
		t.Errorf("second install should report repositories already at their refs:\n%s", out)
	}
	if second := readFile(t, filepath.Join(target, "context", "context.md")); second != first {
		t.Error("second install changed an unmodified file")
	}
	// Converging must not scatter sidecars for files it wrote itself.
	if _, err := os.Stat(filepath.Join(target, "context", "context.md.from-gobag")); err == nil {
		t.Error("idempotent install created a conflict sidecar")
	}
}

// Restoring over the user's own work must keep their version and say so.
func TestInstallPreservesUserEdits(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to git")
	}
	_, archivePath := packFixture(t)
	target := filepath.Join(t.TempDir(), "restored")

	const mine = "my own notes, please do not clobber\n"
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(target, "context", "context.md"), mine)

	if code, out := cli(t, "install", archivePath, "-root", target); code != 0 {
		t.Fatalf("install exited %d:\n%s", code, out)
	}

	if got := readFile(t, filepath.Join(target, "context", "context.md")); got != mine {
		t.Errorf("user's file was overwritten: %q", got)
	}
	if _, err := os.Stat(filepath.Join(target, "context", "context.md.from-gobag")); err != nil {
		t.Errorf("archived version was not preserved alongside: %v", err)
	}
	if o := readFile(t, filepath.Join(target, "ORIENTATION.md")); !strings.Contains(o, "Conflicts") {
		t.Errorf("orientation did not report the conflict:\n%s", o)
	}
}

// An unpushed ref is the classic way a restore disappoints someone: pack warns
// about it, and if the user packs anyway, install must recover the rest of the
// workspace rather than aborting on the one source it cannot reach.
func TestUnpushedRefWarnsThenDegradesGracefully(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to git")
	}
	ws := testutil.NewWorkspace(t)
	ws.CommitUnpushed("backend")

	archivePath := filepath.Join(t.TempDir(), "unpushed.gobag")
	code, out := cli(t, "pack", ws.Root, "-o", archivePath, "-plaintext")
	if code != 0 {
		t.Fatalf("pack exited %d:\n%s", code, out)
	}
	if !strings.Contains(out, "push") {
		t.Errorf("pack should warn that the ref was never pushed:\n%s", out)
	}

	target := filepath.Join(t.TempDir(), "restored")
	code, out = cli(t, "install", archivePath, "-root", target)
	if code != 0 {
		t.Fatalf("install should survive one unrestorable source, exited %d:\n%s", code, out)
	}

	// The healthy repository still landed...
	if _, err := os.Stat(filepath.Join(target, "repos", "frontend", ".git")); err != nil {
		t.Errorf("healthy repository was not restored: %v", err)
	}
	// ...and orientation says plainly what did not.
	orientation := readFile(t, filepath.Join(target, "ORIENTATION.md"))
	if !strings.Contains(orientation, "repos/backend") ||
		!strings.Contains(orientation, "could not be restored") {
		t.Errorf("orientation should report the unrestorable source:\n%s", orientation)
	}
}

func TestPackWarnsOnDirtyTree(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to git")
	}
	ws := testutil.NewWorkspace(t)
	ws.Dirty("backend")

	out := filepath.Join(t.TempDir(), "w.gobag")
	code, output := cli(t, "pack", ws.Root, "-o", out, "-plaintext")
	if code != 0 {
		t.Fatalf("pack exited %d:\n%s", code, output)
	}
	if !strings.Contains(output, "warning") || !strings.Contains(output, "uncommitted") {
		t.Errorf("expected a dirty-tree warning:\n%s", output)
	}
}

// Plan mode is the skill-driven path: the session names the files it wants
// carried, so this is where the secret scan earns its keep.
func TestPackScansPlanContext(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to git")
	}
	ws := testutil.NewWorkspace(t)
	front := ws.Repos["frontend"]

	p := &plan.Plan{
		PlanVersion: plan.Version,
		Name:        "teammate",
		Sources: []plan.Source{{
			Path:   front.Path,
			Dest:   "repos/frontend",
			Remote: front.RemoteURL,
			Ref:    front.Head(t),
			Branch: front.Branch,
		}},
		Context: []plan.Entry{
			{Path: filepath.Join(ws.Root, "context.md"), Dest: "context/HANDOFF.md"},
			{Path: filepath.Join(ws.Root, "notes", "deploy.env"), Dest: "context/deploy.env"},
		},
	}

	planPath := filepath.Join(t.TempDir(), "plan.json")
	f, err := os.Create(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Encode(f); err != nil {
		t.Fatal(err)
	}
	f.Close()

	out := filepath.Join(t.TempDir(), "planned.gobag")
	code, output := cli(t, "pack", "-plan", planPath, "-o", out, "-plaintext")
	if code != 0 {
		t.Fatalf("pack exited %d:\n%s", code, output)
	}
	// The credential must be reported, redacted, before the archive is sealed —
	// and packing must proceed anyway. Warnings never block a checkpoint.
	if !strings.Contains(output, "AKIA") {
		t.Errorf("secret scan did not report the planted key:\n%s", output)
	}
	if strings.Contains(output, "AKIAIOSFODNN7EXAMPLE") {
		t.Error("secret scan leaked the full credential into its own report")
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("pack should not have been blocked by the warning: %v", err)
	}

	// The handoff named by the plan is what restore is pointed at.
	target := filepath.Join(t.TempDir(), "restored")
	if code, out := cli(t, "install", out, "-root", target); code != 0 {
		t.Fatalf("install exited %d:\n%s", code, out)
	}
	if o := readFile(t, filepath.Join(target, "ORIENTATION.md")); !strings.Contains(o, "context/HANDOFF.md") {
		t.Errorf("orientation should point at the handoff:\n%s", o)
	}
}

// Encryption is the default, and a wrong passphrase must fail cleanly.
func TestEncryptedRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to git")
	}
	ws := testutil.NewWorkspace(t)
	archivePath := filepath.Join(t.TempDir(), "secret.gobag")

	t.Setenv("GOBAG_PASSPHRASE", "correct horse battery staple")
	if code, out := cli(t, "pack", ws.Root, "-o", archivePath); code != 0 {
		t.Fatalf("pack exited %d:\n%s", code, out)
	}

	raw, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, []byte("age-encryption.org/v1")) {
		t.Fatal("archive is not encrypted by default")
	}
	if bytes.Contains(raw, []byte("AKIAIOSFODNN7EXAMPLE")) {
		t.Error("planted secret is readable in the encrypted archive")
	}

	if code, out := cli(t, "verify", archivePath); code != 0 {
		t.Fatalf("verify exited %d:\n%s", code, out)
	}

	t.Setenv("GOBAG_PASSPHRASE", "wrong")
	code, out := cli(t, "verify", archivePath)
	if code != 1 {
		t.Fatalf("wrong passphrase should exit 1, got %d:\n%s", code, out)
	}
	if !strings.Contains(out, "passphrase") {
		t.Errorf("error should mention the passphrase:\n%s", out)
	}
}

func TestInspectListsSources(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to git")
	}
	_, archivePath := packFixture(t)

	code, out := cli(t, "inspect", archivePath)
	if code != 0 {
		t.Fatalf("inspect exited %d:\n%s", code, out)
	}
	for _, want := range []string{"repos/frontend", "repos/backend", "sources:"} {
		if !strings.Contains(out, want) {
			t.Errorf("inspect output missing %q:\n%s", want, out)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no arguments", nil},
		{"unknown command", []string{"frobnicate"}},
		{"pack without a target", []string{"pack"}},
		{"pack with both forms", []string{"pack", "-plan", "p.json", "./root"}},
		{"install without an archive", []string{"install"}},
		{"verify with two archives", []string{"verify", "a.gobag", "b.gobag"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code, _ := cli(t, tc.args...); code != 1 {
				t.Errorf("expected exit 1 for a usage error, got %d", code)
			}
		})
	}
}

// Flags must work on either side of the positional argument; Go's flag package
// stops at the first non-flag argument, which would silently drop -plaintext.
func TestFlagsAfterPositional(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to git")
	}
	ws := testutil.NewWorkspace(t)
	out := filepath.Join(t.TempDir(), "trailing.gobag")

	code, output := cli(t, "pack", ws.Root, "-plaintext", "-o", out)
	if code != 0 {
		t.Fatalf("pack exited %d:\n%s", code, output)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.HasPrefix(raw, []byte("age-encryption.org/v1")) {
		t.Error("-plaintext after the positional argument was ignored")
	}
}

// An archive is untrusted input: a crafted entry must not be able to write
// outside the install root.
func TestInstallRejectsTraversalArchive(t *testing.T) {
	dir := t.TempDir()
	hostile := filepath.Join(dir, "hostile.gobag")
	f, err := os.Create(hostile)
	if err != nil {
		t.Fatal(err)
	}
	w, err := archive.NewWriter(f, "")
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(w.AddBytes("MANIFEST.json", 0o644,
		[]byte(`{"plan_version":1,"name":"evil","created":"2026-01-01T00:00:00Z"}`)))
	must(w.AddBytes("../escaped.txt", 0o644, []byte("pwned\n")))
	must(w.Close())
	must(f.Close())

	target := filepath.Join(dir, "victim", "ws")
	code, out := cli(t, "install", hostile, "-root", target)
	if code != 1 {
		t.Errorf("hostile archive should exit 1, got %d:\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "victim", "escaped.txt")); err == nil {
		t.Fatal("traversal succeeded: file written outside the install root")
	}
}

// The skill-driven path must carry the source root, or memory restored to a
// different machine keeps asserting paths from the old one.
func TestPlanModeRewritesMemoryPaths(t *testing.T) {
	dir := t.TempDir()
	oldRoot := filepath.Join(dir, "old", "ws")

	memSrc := filepath.Join(dir, "memsrc")
	writeFile(t, filepath.Join(memSrc, "runbook.md"),
		"Deploy lives in "+oldRoot+"/repos/backend.\n")

	p := &plan.Plan{
		PlanVersion: plan.Version,
		Name:        "t",
		SourceRoot:  oldRoot,
		State: plan.State{
			Memory: []plan.Entry{{Path: memSrc, Dest: "state/memory"}},
		},
	}
	planPath := filepath.Join(dir, "plan.json")
	pf, err := os.Create(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Encode(pf); err != nil {
		t.Fatal(err)
	}
	pf.Close()

	out := filepath.Join(dir, "t.gobag")
	if code, o := cli(t, "pack", "-plan", planPath, "-o", out, "-plaintext"); code != 0 {
		t.Fatalf("pack exited %d:\n%s", code, o)
	}

	claudeDir := filepath.Join(dir, "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	newRoot := filepath.Join(dir, "new", "ws")
	if code, o := cli(t, "install", out, "-root", newRoot, "-link-memory"); code != 0 {
		t.Fatalf("install exited %d:\n%s", code, o)
	}

	installed := filepath.Join(claudeDir, "projects",
		claudestate.EncodeProjectDir(mustEvalSymlinks(t, newRoot)), "runbook.md")
	got := readFile(t, installed)
	if strings.Contains(got, oldRoot) {
		t.Errorf("old workspace root survived in installed memory: %q", got)
	}
	if !strings.Contains(got, "repos/backend") {
		t.Errorf("memory content mangled: %q", got)
	}
}

func mustEvalSymlinks(t *testing.T, p string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("resolving %s: %v", p, err)
	}
	return resolved
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
