package claudestate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/satmihir/gobag/internal/overlay"
)

func TestEncodeProjectDir(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/Users/u/ws/proj", "-Users-u-ws-proj"},
		{"/Users/u/ws/proj/", "-Users-u-ws-proj"},
		{"/", "-"},
		{"/a", "-a"},
		// Dots and underscores become dashes too — verified against Claude
		// Code's own behavior: /tmp/dot.ted_dir -> -tmp-dot-ted-dir.
		{"/tmp/dot.ted_dir/sub", "-tmp-dot-ted-dir-sub"},
		{"/var/folders/_p/x", "-var-folders--p-x"},
		{"/a/b.c", "-a-b-c"},
	} {
		if got := EncodeProjectDir(tc.in); got != tc.want {
			t.Errorf("EncodeProjectDir(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRekey(t *testing.T) {
	const oldRoot = "/old/ws/proj"
	newRoot := t.TempDir()

	src := t.TempDir()
	dst := t.TempDir()
	write(t, filepath.Join(src, "MEMORY.md"), "- [Runbook](memory/runbook.md)\n")
	write(t, filepath.Join(src, "memory", "runbook.md"),
		"Deploy scripts live in "+oldRoot+"/repos/backend.\n")
	write(t, filepath.Join(src, "memory", "blob.bin"), "raw "+oldRoot+" bytes")

	res, err := Rekey(src, dst, oldRoot, newRoot)
	if err != nil {
		t.Fatalf("Rekey: %v", err)
	}
	if len(res.Files) != 3 {
		t.Fatalf("copied %d files, want 3", len(res.Files))
	}

	// Markdown gets the substitution.
	runbook := read(t, filepath.Join(dst, "memory", "runbook.md"))
	if strings.Contains(runbook, oldRoot) {
		t.Errorf("old root survived in markdown: %q", runbook)
	}
	if !strings.Contains(runbook, newRoot) {
		t.Errorf("new root missing from markdown: %q", runbook)
	}
	if len(res.Rewritten) != 1 || res.Rewritten[0] != "memory/runbook.md" {
		t.Errorf("Rewritten = %v, want [memory/runbook.md]", res.Rewritten)
	}

	// Non-markdown travels untouched: blind substitution in an unknown format
	// is how data gets corrupted.
	if blob := read(t, filepath.Join(dst, "memory", "blob.bin")); !strings.Contains(blob, oldRoot) {
		t.Errorf("non-markdown file was rewritten: %q", blob)
	}
}

func TestRekeyIsIdempotentAndNonDestructive(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "note.md"), "archived\n")

	first, err := Rekey(src, dst, "", "")
	if err != nil {
		t.Fatalf("first Rekey: %v", err)
	}
	if first.Files[0].Outcome != overlay.OutcomeWritten {
		t.Fatalf("first pass outcome = %q", first.Files[0].Outcome)
	}

	second, err := Rekey(src, dst, "", "")
	if err != nil {
		t.Fatalf("second Rekey: %v", err)
	}
	if second.Files[0].Outcome != overlay.OutcomeIdentical {
		t.Errorf("second pass outcome = %q, want identical", second.Files[0].Outcome)
	}

	// A memory file the user has since edited must survive untouched.
	write(t, filepath.Join(dst, "note.md"), "the user rewrote this\n")
	third, err := Rekey(src, dst, "", "")
	if err != nil {
		t.Fatalf("third Rekey: %v", err)
	}
	if len(third.Conflicts()) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(third.Conflicts()))
	}
	if got := read(t, filepath.Join(dst, "note.md")); got != "the user rewrote this\n" {
		t.Errorf("user's memory file was overwritten: %q", got)
	}
	if got := read(t, filepath.Join(dst, "note.md"+overlay.Suffix)); got != "archived\n" {
		t.Errorf("sidecar content = %q", got)
	}
}

func TestProjectsRootHonorsConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if got, want := ProjectsRoot(), filepath.Join(dir, "projects"); got != want {
		t.Errorf("ProjectsRoot() = %q, want %q", got, want)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
