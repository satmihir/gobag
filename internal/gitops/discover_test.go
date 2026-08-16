package gitops

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/satmihir/gobag/internal/plan"
	"github.com/satmihir/gobag/internal/testutil"
)

func TestDiscoverFixtureWorkspace(t *testing.T) {
	ws := fixture(t)

	p, problems, err := Discover(ws.Root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if p.PlanVersion != plan.Version {
		t.Errorf("plan_version = %d, want %d", p.PlanVersion, plan.Version)
	}
	if p.Name != filepath.Base(ws.Root) {
		t.Errorf("name = %q, want %q", p.Name, filepath.Base(ws.Root))
	}
	if len(p.Sources) != 2 {
		t.Fatalf("want 2 sources, got %v", dests(p))
	}

	// Both repositories travel as references, at their exact HEADs.
	for _, name := range []string{"backend", "frontend"} {
		repo := ws.Repos[name]
		s := sourceFor(t, p, "repos/"+name)
		if s.Path != repo.Path {
			t.Errorf("%s path = %q, want %q", name, s.Path, repo.Path)
		}
		if s.Remote != repo.RemoteURL {
			t.Errorf("%s remote = %q, want %q", name, s.Remote, repo.RemoteURL)
		}
		if want := repo.Head(t); s.Ref != want {
			t.Errorf("%s ref = %q, want %q", name, s.Ref, want)
		}
		if len(s.Ref) != 40 {
			t.Errorf("%s ref %q is not a full sha", name, s.Ref)
		}
		if s.Branch != "main" {
			t.Errorf("%s branch = %q, want main", name, s.Branch)
		}
	}

	// The linked worktree is bound to its parent, not listed on its own.
	frontend := sourceFor(t, p, "repos/frontend")
	if len(frontend.Worktrees) != 1 {
		t.Fatalf("want 1 worktree under frontend, got %+v", frontend.Worktrees)
	}
	wt := frontend.Worktrees[0]
	wip := ws.Repos["frontend-wip"]
	if wt.Dest != "repos/frontend-wip" {
		t.Errorf("worktree dest = %q", wt.Dest)
	}
	if wt.Path != wip.Path {
		t.Errorf("worktree path = %q, want %q", wt.Path, wip.Path)
	}
	if wt.Branch != "wip" {
		t.Errorf("worktree branch = %q, want wip", wt.Branch)
	}
	if want := wip.Head(t); wt.Ref != want {
		t.Errorf("worktree ref = %q, want %q", wt.Ref, want)
	}
	backend := sourceFor(t, p, "repos/backend")
	if len(backend.Worktrees) != 0 {
		t.Errorf("backend picked up worktrees: %+v", backend.Worktrees)
	}

	// The context document at the root travels; nothing else is invented.
	if len(p.Context) != 1 {
		t.Fatalf("want 1 context entry, got %+v", p.Context)
	}
	if got, want := p.Context[0].Dest, "context/context.md"; got != want {
		t.Errorf("context dest = %q, want %q", got, want)
	}
	if got, want := p.Context[0].Path, filepath.Join(ws.Root, "context.md"); got != want {
		t.Errorf("context path = %q, want %q", got, want)
	}
	if len(p.Skills) != 0 {
		t.Errorf("no skills directory exists, yet got %+v", p.Skills)
	}

	// A clean fixture is a clean discovery.
	if errs, warns := plan.Problems(problems); len(errs) != 0 || len(warns) != 0 {
		t.Fatalf("clean workspace produced problems: %v %v", errs, warns)
	}

	// Discovery is a read: it must produce a plan that also passes the pure
	// validator, and every destination must be safe.
	if ps := plan.Validate(p); len(ps) != 0 {
		t.Fatalf("discovered plan fails Validate: %v", ps)
	}
}

func TestDiscoverProblems(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, ws *testutil.Workspace)
		check func(t *testing.T, ws *testutil.Workspace, p *plan.Plan, ps []plan.Problem)
	}{
		{
			name:  "uncommitted changes",
			setup: func(_ *testing.T, ws *testutil.Workspace) { ws.Dirty("backend") },
			check: func(t *testing.T, _ *testutil.Workspace, p *plan.Plan, ps []plan.Problem) {
				findProblem(t, ps, plan.SeverityWarning, "repos/backend", "uncommitted changes will not travel")
				noProblem(t, ps, plan.SeverityWarning, "repos/frontend", "uncommitted")
				// The repository is still packable; the edit simply stays behind.
				if s := sourceFor(t, p, "repos/backend"); s.Remote == "" {
					t.Error("dirty repository lost its remote")
				}
			},
		},
		{
			name: "commit that was never pushed",
			setup: func(_ *testing.T, ws *testutil.Workspace) {
				ws.CommitUnpushed("backend")
			},
			check: func(t *testing.T, ws *testutil.Workspace, p *plan.Plan, ps []plan.Problem) {
				findProblem(t, ps, plan.SeverityWarning, "repos/backend", "the restore will fail until you push it")
				if got, want := sourceFor(t, p, "repos/backend").Ref, ws.Repos["backend"].Head(t); got != want {
					t.Errorf("ref = %q, want the unpushed head %q", got, want)
				}
			},
		},
		{
			name:  "no remote at all",
			setup: func(_ *testing.T, ws *testutil.Workspace) { ws.DropRemote("backend") },
			check: func(t *testing.T, _ *testutil.Workspace, p *plan.Plan, ps []plan.Problem) {
				findProblem(t, ps, plan.SeverityError, "repos/backend", "cannot travel as a reference")
				// A remote-less repository is still reported, so the user can
				// see exactly which one blocked the pack.
				if s := sourceFor(t, p, "repos/backend"); s.Remote != "" {
					t.Errorf("remote = %q, want empty", s.Remote)
				}
				// And the pure validator agrees, without touching git.
				errs, _ := plan.Problems(plan.Validate(p))
				if len(errs) != 1 {
					t.Fatalf("want 1 validation error, got %v", errs)
				}
			},
		},
		{
			name: "worktree whose parent lives outside the root",
			setup: func(t *testing.T, ws *testutil.Workspace) {
				outside := filepath.Join(filepath.Dir(ws.Root), "outside")
				testutil.Git(t, filepath.Dir(ws.Root), "clone", "-q", "-b", "main", ws.Repos["backend"].RemoteURL, outside)
				testutil.Git(t, outside, "worktree", "add", "-q", "-b", "spike",
					filepath.Join(ws.Root, "repos", "spike"))
			},
			check: func(t *testing.T, ws *testutil.Workspace, p *plan.Plan, ps []plan.Problem) {
				findProblem(t, ps, plan.SeverityWarning, "repos/spike", "outside the packed root")
				// It still travels, as a clone of the same remote rather than
				// as a linked worktree of a repository that is not in the bag.
				s := sourceFor(t, p, "repos/spike")
				if s.Remote != ws.Repos["backend"].RemoteURL {
					t.Errorf("spike remote = %q, want %q", s.Remote, ws.Repos["backend"].RemoteURL)
				}
				if s.Branch != "spike" {
					t.Errorf("spike branch = %q, want spike", s.Branch)
				}
				for _, other := range p.Sources {
					for _, w := range other.Worktrees {
						if w.Dest == "repos/spike" {
							t.Errorf("spike was bound to %s despite its parent being outside", other.Dest)
						}
					}
				}
			},
		},
		{
			name: "project-local skills",
			setup: func(t *testing.T, ws *testutil.Workspace) {
				dir := filepath.Join(ws.Root, ".claude", "skills", "checkpoint")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# checkpoint\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, _ *testutil.Workspace, p *plan.Plan, _ []plan.Problem) {
				if len(p.Skills) != 1 || p.Skills[0].Dest != "skills/checkpoint" {
					t.Fatalf("skills = %+v", p.Skills)
				}
			},
		},
		{
			name: "handoff document at the root",
			setup: func(t *testing.T, ws *testutil.Workspace) {
				if err := os.WriteFile(filepath.Join(ws.Root, "HANDOFF.md"), []byte("# handoff\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, _ *testutil.Workspace, p *plan.Plan, _ []plan.Problem) {
				var got []string
				for _, e := range p.Context {
					got = append(got, e.Dest)
				}
				if len(got) != 2 || got[0] != "context/context.md" || got[1] != "context/HANDOFF.md" {
					t.Fatalf("context = %v", got)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ws := fixture(t)
			tc.setup(t, ws)

			p, problems, err := Discover(ws.Root)
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			tc.check(t, ws, p, problems)
		})
	}
}

func TestDiscoverIsDeterministic(t *testing.T) {
	ws := fixture(t)
	ws.Dirty("frontend")

	first, fp, err := Discover(ws.Root)
	if err != nil {
		t.Fatal(err)
	}
	second, sp, err := Discover(ws.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(fp) != len(sp) {
		t.Fatalf("problem counts differ: %d vs %d", len(fp), len(sp))
	}
	for i := range fp {
		if fp[i] != sp[i] {
			t.Fatalf("problem %d differs: %v vs %v", i, fp[i], sp[i])
		}
	}
	if len(first.Sources) != len(second.Sources) {
		t.Fatalf("source counts differ")
	}
	for i := range first.Sources {
		if first.Sources[i].Dest != second.Sources[i].Dest {
			t.Fatalf("source order differs at %d: %q vs %q", i, first.Sources[i].Dest, second.Sources[i].Dest)
		}
	}
}
