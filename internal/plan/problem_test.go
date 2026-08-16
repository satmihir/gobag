package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture lays down a small tree and returns a plan pointing at it, valid
// until a test case bends one field.
func fixture(t *testing.T) (string, *Plan) {
	t.Helper()
	dir := t.TempDir()

	write := func(rel string) string {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	repo := filepath.Join(dir, "frontend")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(dir, "frontend-wip")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}

	p := &Plan{
		PlanVersion: Version,
		Name:        "fixture",
		Sources: []Source{{
			Path:   repo,
			Dest:   "repos/frontend",
			Remote: "git@github.com:org/frontend.git",
			Ref:    "3f9c2ab00000000000000000000000000000abcd",
			Branch: "main",
			Worktrees: []Worktree{{
				Path:   wt,
				Dest:   "repos/frontend-wip",
				Ref:    "3f9c2ab00000000000000000000000000000abcd",
				Branch: "wip",
			}},
		}},
		Context: []Entry{{Path: write("HANDOFF.md"), Dest: "context/HANDOFF.md"}},
		Skills:  []Entry{{Path: write(".claude/skills/checkpoint/SKILL.md"), Dest: "skills/checkpoint"}},
		State: State{
			Memory: []Entry{{Path: write("memory/notes.md"), Dest: "state/memory/notes.md"}},
		},
	}
	return dir, p
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		bend func(t *testing.T, dir string, p *Plan)
		// wantErr and wantWarn are substrings expected in exactly one problem
		// of that severity; empty means none of that severity is expected.
		wantErr  string
		wantWarn string
		wantDest string
	}{
		{
			name: "valid plan",
			bend: func(*testing.T, string, *Plan) {},
		},
		{
			name:     "dest escapes the archive root",
			bend:     func(_ *testing.T, _ string, p *Plan) { p.Sources[0].Dest = "../../etc" },
			wantErr:  "escapes the archive root",
			wantDest: "../../etc",
		},
		{
			name:     "absolute dest",
			bend:     func(_ *testing.T, _ string, p *Plan) { p.Context[0].Dest = "/etc/passwd" },
			wantErr:  "must be relative",
			wantDest: "/etc/passwd",
		},
		{
			name:     "backslash dest",
			bend:     func(_ *testing.T, _ string, p *Plan) { p.Skills[0].Dest = `skills\checkpoint` },
			wantErr:  "forward slashes",
			wantDest: `skills\checkpoint`,
		},
		{
			name:     "local path is missing",
			bend:     func(_ *testing.T, dir string, p *Plan) { p.Context[0].Path = filepath.Join(dir, "gone.md") },
			wantErr:  "missing on disk",
			wantDest: "context/HANDOFF.md",
		},
		{
			name:     "repository has no remote",
			bend:     func(_ *testing.T, _ string, p *Plan) { p.Sources[0].Remote = "" },
			wantErr:  "cannot travel as a reference",
			wantDest: "repos/frontend",
		},
		{
			name:     "repository has no ref",
			bend:     func(_ *testing.T, _ string, p *Plan) { p.Sources[0].Ref = "" },
			wantErr:  "no pinned ref",
			wantDest: "repos/frontend",
		},
		{
			name:     "worktree has no ref",
			bend:     func(_ *testing.T, _ string, p *Plan) { p.Sources[0].Worktrees[0].Ref = "" },
			wantErr:  "no pinned ref",
			wantDest: "repos/frontend-wip",
		},
		{
			name:     "two entries claim one dest",
			bend:     func(_ *testing.T, _ string, p *Plan) { p.Skills[0].Dest = "context/HANDOFF.md" },
			wantErr:  "already claimed by the context entry",
			wantDest: "context/HANDOFF.md",
		},
		{
			name: "oversized context document",
			bend: func(t *testing.T, dir string, p *Plan) {
				big := filepath.Join(dir, "huge.md")
				f, err := os.Create(big)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				if err := f.Truncate(MaxContextBytes + 1); err != nil {
					t.Fatal(err)
				}
				p.Context[0].Path = big
			},
			wantWarn: "probably a log or a dump",
			wantDest: "context/HANDOFF.md",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, p := fixture(t)
			tc.bend(t, dir, p)

			errs, warns := Problems(Validate(p))

			if tc.wantErr == "" && len(errs) != 0 {
				t.Fatalf("expected no errors, got %v", errs)
			}
			if tc.wantWarn == "" && len(warns) != 0 {
				t.Fatalf("expected no warnings, got %v", warns)
			}
			if tc.wantErr != "" {
				requireProblem(t, errs, tc.wantErr, tc.wantDest)
			}
			if tc.wantWarn != "" {
				requireProblem(t, warns, tc.wantWarn, tc.wantDest)
			}
		})
	}
}

func requireProblem(t *testing.T, ps []Problem, substr, dest string) {
	t.Helper()
	for _, p := range ps {
		if strings.Contains(p.Message, substr) && p.Dest == dest {
			return
		}
	}
	t.Fatalf("no problem mentioning %q at %q; got %v", substr, dest, ps)
}

func TestValidateReportsEveryProblemNotJustTheFirst(t *testing.T) {
	_, p := fixture(t)
	p.Sources[0].Remote = ""
	p.Sources[0].Ref = ""
	p.Context[0].Path = "/nope/missing.md"

	errs, _ := Problems(Validate(p))
	if len(errs) != 3 {
		t.Fatalf("expected 3 errors, got %d: %v", len(errs), errs)
	}
}

func TestProblemsPartitionsAndPreservesOrder(t *testing.T) {
	in := []Problem{
		{SeverityWarning, "a", "first warning"},
		{SeverityError, "b", "first error"},
		{SeverityWarning, "c", "second warning"},
		{SeverityError, "d", "second error"},
	}
	errs, warns := Problems(in)

	if len(errs) != 2 || errs[0].Message != "first error" || errs[1].Message != "second error" {
		t.Fatalf("errors partitioned wrong: %v", errs)
	}
	if len(warns) != 2 || warns[0].Message != "first warning" || warns[1].Message != "second warning" {
		t.Fatalf("warnings partitioned wrong: %v", warns)
	}
	if got, want := errs[0].String(), "error: b: first error"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if got, want := (Problem{Severity: SeverityWarning, Message: "plan-wide"}).String(), "warning: plan-wide"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestValidateIsPure(t *testing.T) {
	// Validate must not need git or the network: a plan whose repository is a
	// plain directory with an unreachable remote still validates cleanly.
	_, p := fixture(t)
	p.Sources[0].Remote = "https://example.invalid/nope.git"
	if problems := Validate(p); len(problems) != 0 {
		t.Fatalf("expected no problems from a git-free check, got %v", problems)
	}
}
