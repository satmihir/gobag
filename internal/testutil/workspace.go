// Package testutil builds throwaway workspaces for gobag's tests: real git
// repositories with real (local) remotes, a fake Claude Code state directory,
// and a planted secret. Fixtures are constructed at run time — never checked
// in — so the repository carries no nested .git directories.
package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/satmihir/gobag/internal/claudestate"
)

// Workspace is a fixture: a workspace root, the bare repositories standing in
// for GitHub remotes, and a fake Claude Code config directory.
type Workspace struct {
	t *testing.T

	// Root is the workspace root: the directory a walk-mode pack would scan.
	Root string
	// RemotesDir holds the bare repositories used as "remotes".
	RemotesDir string
	// ClaudeDir is a fake CLAUDE_CONFIG_DIR (the ~/.claude equivalent).
	ClaudeDir string
	// Repos is keyed by repository name.
	Repos map[string]*Repo
}

// Repo describes one repository inside the fixture workspace.
type Repo struct {
	Name string
	// Path is the absolute working-tree path.
	Path string
	// RemoteURL is a file:// URL pointing at the bare repository.
	RemoteURL string
	// Branch is the checked-out branch.
	Branch string
}

// Head returns the current HEAD sha of the repository.
func (r *Repo) Head(t *testing.T) string {
	t.Helper()
	return Git(t, r.Path, "rev-parse", "HEAD")
}

// NewWorkspace builds the standard fixture:
//
//	<root>/repos/frontend        repo on main, remote frontend.git
//	<root>/repos/backend         repo on main, remote backend.git
//	<root>/repos/frontend-wip    linked worktree of frontend, branch wip
//	<root>/context.md            a context document
//	<root>/notes/deploy.env      contains a fake AWS key for scan tests
//	<claude>/projects/<encoded>/ memory files keyed to the root
//
// The returned workspace lives under t.TempDir() and is removed automatically.
func NewWorkspace(t *testing.T) *Workspace {
	t.Helper()
	RequireGit(t)

	base := t.TempDir()
	// macOS temp directories are symlinks (/var -> /private/var). Resolve now
	// so paths recorded by gobag match paths the test compares against.
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}

	ws := &Workspace{
		t:          t,
		Root:       filepath.Join(base, "ws"),
		RemotesDir: filepath.Join(base, "remotes"),
		ClaudeDir:  filepath.Join(base, "claude"),
		Repos:      map[string]*Repo{},
	}
	mkdirAll(t, ws.Root, ws.RemotesDir, ws.ClaudeDir)

	ws.addRepo("frontend", "src/app.js", "export const app = () => null;\n")
	ws.addRepo("backend", "server.go", "package main\n\nfunc main() {}\n")
	ws.addWorktree("frontend", "frontend-wip", "wip")

	writeFile(t, filepath.Join(ws.Root, "context.md"),
		"# Working context\n\nMigrating the frontend to the new API.\n")
	writeFile(t, filepath.Join(ws.Root, "notes", "deploy.env"),
		"AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE\nREGION=us-west-2\n")

	ws.writeClaudeState()
	return ws
}

// addRepo creates a bare "remote" plus a working clone under repos/<name>.
func (w *Workspace) addRepo(name, file, content string) {
	t := w.t
	t.Helper()

	bare := filepath.Join(w.RemotesDir, name+".git")
	Git(t, w.RemotesDir, "init", "--bare", "-q", bare)

	path := filepath.Join(w.Root, "repos", name)
	mkdirAll(t, path)
	Git(t, path, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(path, file), content)
	writeFile(t, filepath.Join(path, "README.md"), "# "+name+"\n")
	Git(t, path, "add", ".")
	Git(t, path, "commit", "-q", "-m", "initial commit")

	remoteURL := "file://" + bare
	Git(t, path, "remote", "add", "origin", remoteURL)
	Git(t, path, "push", "-q", "-u", "origin", "main")

	// git init --bare points HEAD at refs/heads/master, which nothing here
	// creates. A real remote's HEAD names its default branch, and code that
	// clones or runs ls-remote HEAD depends on that, so point it at main.
	Git(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")

	w.Repos[name] = &Repo{Name: name, Path: path, RemoteURL: remoteURL, Branch: "main"}
}

// addWorktree adds a linked worktree of parent at repos/<name> on a new branch.
func (w *Workspace) addWorktree(parent, name, branch string) {
	t := w.t
	t.Helper()

	p, ok := w.Repos[parent]
	if !ok {
		t.Fatalf("addWorktree: unknown parent repo %q", parent)
	}
	path := filepath.Join(w.Root, "repos", name)
	Git(t, p.Path, "worktree", "add", "-q", "-b", branch, path)
	w.Repos[name] = &Repo{Name: name, Path: path, RemoteURL: p.RemoteURL, Branch: branch}
}

// writeClaudeState creates memory files keyed to the workspace root, mirroring
// the ~/.claude/projects/<encoded-cwd>/ layout.
func (w *Workspace) writeClaudeState() {
	t := w.t
	t.Helper()

	dir := filepath.Join(w.ClaudeDir, "projects", claudestate.EncodeProjectDir(w.Root), "memory")
	writeFile(t, filepath.Join(dir, "team-conventions.md"),
		"---\nname: team-conventions\n---\n\nSquash merges only.\n")
	writeFile(t, filepath.Join(dir, "deploy-runbook.md"),
		fmt.Sprintf("---\nname: deploy-runbook\n---\n\nDeploy scripts live in %s/repos/backend.\n", w.Root))
	writeFile(t, filepath.Join(dir, "..", "MEMORY.md"),
		"- [Team conventions](memory/team-conventions.md) — squash only\n")
}

// ProjectDir returns the fake Claude project-state directory for the workspace
// root, the directory an install must re-key when the root changes.
func (w *Workspace) ProjectDir() string {
	return filepath.Join(w.ClaudeDir, "projects", claudestate.EncodeProjectDir(w.Root))
}

// UseClaudeDir points claudestate at this fixture's config directory for the
// duration of the test.
func (w *Workspace) UseClaudeDir() {
	w.t.Setenv("CLAUDE_CONFIG_DIR", w.ClaudeDir)
}

// Dirty leaves an uncommitted modification in the named repository.
func (w *Workspace) Dirty(name string) {
	w.t.Helper()
	r := w.repo(name)
	writeFile(w.t, filepath.Join(r.Path, "README.md"), "# "+name+"\n\nwork in progress\n")
}

// CommitUnpushed adds a commit that exists only locally, simulating work the
// user forgot to push before packing.
func (w *Workspace) CommitUnpushed(name string) string {
	w.t.Helper()
	r := w.repo(name)
	writeFile(w.t, filepath.Join(r.Path, "local-only.txt"), "not pushed\n")
	Git(w.t, r.Path, "add", ".")
	Git(w.t, r.Path, "commit", "-q", "-m", "local only commit")
	return r.Head(w.t)
}

// DropRemote removes the origin remote, simulating a local-only repository
// that cannot travel as a reference.
func (w *Workspace) DropRemote(name string) {
	w.t.Helper()
	r := w.repo(name)
	Git(w.t, r.Path, "remote", "remove", "origin")
	r.RemoteURL = ""
}

// AdvanceRemote pushes n commits to the repository's remote without touching
// the local clone, simulating the world moving on while the bag was packed.
func (w *Workspace) AdvanceRemote(name string, n int) {
	t := w.t
	t.Helper()
	r := w.repo(name)
	if r.RemoteURL == "" {
		t.Fatalf("AdvanceRemote: repo %q has no remote", name)
	}

	scratch := filepath.Join(t.TempDir(), "advance-"+name)
	Git(t, filepath.Dir(scratch), "clone", "-q", r.RemoteURL, scratch)
	for i := 0; i < n; i++ {
		writeFile(t, filepath.Join(scratch, fmt.Sprintf("upstream-%d.txt", i)), "upstream work\n")
		Git(t, scratch, "add", ".")
		Git(t, scratch, "commit", "-q", "-m", fmt.Sprintf("upstream commit %d", i+1))
	}
	Git(t, scratch, "push", "-q", "origin", "main")
}

func (w *Workspace) repo(name string) *Repo {
	w.t.Helper()
	r, ok := w.Repos[name]
	if !ok {
		w.t.Fatalf("unknown repo %q in fixture", name)
	}
	return r
}

func mkdirAll(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
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
