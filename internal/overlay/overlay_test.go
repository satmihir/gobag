package overlay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWrite(t *testing.T) {
	const archived = "archived content\n"

	for _, tc := range []struct {
		name        string
		existing    string // "" means the file is absent
		wantOutcome Outcome
		wantOnDisk  string // what the user's path must hold afterwards
		wantSidecar bool
	}{
		{
			name:        "absent path is written",
			wantOutcome: OutcomeWritten,
			wantOnDisk:  archived,
		},
		{
			name:        "identical content is a no-op",
			existing:    archived,
			wantOutcome: OutcomeIdentical,
			wantOnDisk:  archived,
		},
		{
			name:        "different content keeps the user's copy",
			existing:    "the user's own edits\n",
			wantOutcome: OutcomeConflict,
			wantOnDisk:  "the user's own edits\n",
			wantSidecar: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			rel := "context/HANDOFF.md"
			if tc.existing != "" {
				mustWrite(t, filepath.Join(root, rel), tc.existing)
			}

			res, err := Write(root, rel, 0o644, strings.NewReader(archived))
			if err != nil {
				t.Fatalf("Write: %v", err)
			}
			if res.Outcome != tc.wantOutcome {
				t.Errorf("outcome = %q, want %q", res.Outcome, tc.wantOutcome)
			}
			if got := mustRead(t, filepath.Join(root, rel)); got != tc.wantOnDisk {
				t.Errorf("on disk = %q, want %q", got, tc.wantOnDisk)
			}

			sidecar := filepath.Join(root, rel+Suffix)
			_, err = os.Stat(sidecar)
			if tc.wantSidecar {
				if err != nil {
					t.Fatalf("expected sidecar at %s: %v", sidecar, err)
				}
				if got := mustRead(t, sidecar); got != archived {
					t.Errorf("sidecar = %q, want %q", got, archived)
				}
				if res.SidecarPath != rel+Suffix {
					t.Errorf("SidecarPath = %q, want %q", res.SidecarPath, rel+Suffix)
				}
			} else if err == nil {
				t.Error("unexpected sidecar written")
			}
		})
	}
}

// Writing the same archive twice must converge: the second pass reports
// identical, not conflict, and creates no further sidecars.
func TestWriteIsIdempotent(t *testing.T) {
	root := t.TempDir()
	const rel = "context/HANDOFF.md"

	first, err := Write(root, rel, 0o644, strings.NewReader("x\n"))
	if err != nil || first.Outcome != OutcomeWritten {
		t.Fatalf("first write: %v (%s)", err, first.Outcome)
	}
	second, err := Write(root, rel, 0o644, strings.NewReader("x\n"))
	if err != nil || second.Outcome != OutcomeIdentical {
		t.Fatalf("second write: %v (%s)", err, second.Outcome)
	}
	if _, err := os.Stat(filepath.Join(root, rel+Suffix)); err == nil {
		t.Error("idempotent rewrite should not create a sidecar")
	}
}

func TestSafeJoinRejectsEscapes(t *testing.T) {
	root := t.TempDir()

	for _, rel := range []string{
		"../outside.md",
		"a/../../outside.md",
		"/etc/passwd",
		"..",
	} {
		if _, err := SafeJoin(root, rel); err == nil {
			t.Errorf("SafeJoin(%q) should have been rejected", rel)
		}
	}

	for _, rel := range []string{
		"context/HANDOFF.md",
		"a/b/c.txt",
		"./nested/file.md",
	} {
		if _, err := SafeJoin(root, rel); err != nil {
			t.Errorf("SafeJoin(%q) unexpectedly rejected: %v", rel, err)
		}
	}
}

// A symlinked directory inside the root must not redirect a write outside it.
func TestSafeJoinRejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	mustMkdir(t, root, outside)

	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := SafeJoin(root, "escape/evil.md"); err == nil {
		t.Error("write through a symlinked directory should be rejected")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func mustMkdir(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
}
