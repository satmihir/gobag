package gitops

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/satmihir/gobag/internal/plan"
	"github.com/satmihir/gobag/internal/testutil"
)

func TestVerifyPlan(t *testing.T) {
	const missingSha = "0123456789abcdef0123456789abcdef01234567"

	tests := []struct {
		name string
		// bend mutates the discovered plan, or the workspace, or both.
		bend func(t *testing.T, ws *testutil.Workspace, p *plan.Plan)
		// want is the single problem expected, by severity, dest, and a
		// substring of the message. A zero want means "no problems at all".
		wantSeverity plan.Severity
		wantDest     string
		wantMessage  string
		clean        bool
	}{
		{
			name:  "a clean workspace verifies silently",
			bend:  func(*testing.T, *testutil.Workspace, *plan.Plan) {},
			clean: true,
		},
		{
			name: "path is not a git repository",
			bend: func(_ *testing.T, ws *testutil.Workspace, p *plan.Plan) {
				setSource(p, "repos/backend", func(s *plan.Source) { s.Path = filepath.Join(ws.Root, "notes") })
			},
			wantSeverity: plan.SeverityError,
			wantDest:     "repos/backend",
			wantMessage:  "is not a git repository",
		},
		{
			name: "ref does not resolve locally",
			bend: func(_ *testing.T, _ *testutil.Workspace, p *plan.Plan) {
				setSource(p, "repos/backend", func(s *plan.Source) { s.Ref = missingSha })
			},
			wantSeverity: plan.SeverityError,
			wantDest:     "repos/backend",
			wantMessage:  "does not exist in",
		},
		{
			name: "remote is unreachable",
			bend: func(_ *testing.T, ws *testutil.Workspace, p *plan.Plan) {
				gone := "file://" + filepath.Join(ws.RemotesDir, "never-existed.git")
				setSource(p, "repos/backend", func(s *plan.Source) { s.Remote = gone })
			},
			wantSeverity: plan.SeverityError,
			wantDest:     "repos/backend",
			wantMessage:  "is unreachable",
		},
		{
			name: "no remote recorded",
			bend: func(_ *testing.T, _ *testutil.Workspace, p *plan.Plan) {
				setSource(p, "repos/backend", func(s *plan.Source) { s.Remote = "" })
			},
			wantSeverity: plan.SeverityError,
			wantDest:     "repos/backend",
			wantMessage:  "cannot travel as a reference",
		},
		{
			name: "worktree ref does not resolve in its parent",
			bend: func(_ *testing.T, _ *testutil.Workspace, p *plan.Plan) {
				setSource(p, "repos/frontend", func(s *plan.Source) { s.Worktrees[0].Ref = missingSha })
			},
			wantSeverity: plan.SeverityError,
			wantDest:     "repos/frontend-wip",
			wantMessage:  "does not exist in",
		},
		{
			name: "uncommitted changes are only a warning",
			bend: func(_ *testing.T, ws *testutil.Workspace, _ *plan.Plan) {
				ws.Dirty("backend")
			},
			wantSeverity: plan.SeverityWarning,
			wantDest:     "repos/backend",
			wantMessage:  "uncommitted changes will not travel",
		},
		{
			name: "unpushed ref is a warning that names the consequence",
			bend: func(_ *testing.T, ws *testutil.Workspace, p *plan.Plan) {
				sha := ws.CommitUnpushed("backend")
				setSource(p, "repos/backend", func(s *plan.Source) { s.Ref = sha })
			},
			wantSeverity: plan.SeverityWarning,
			wantDest:     "repos/backend",
			wantMessage:  "the restore will fail until you push it",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ws := fixture(t)
			p, _, err := Discover(ws.Root)
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			tc.bend(t, ws, p)

			problems := VerifyPlan(p)
			errs, warns := plan.Problems(problems)

			if tc.clean {
				if len(errs) != 0 || len(warns) != 0 {
					t.Fatalf("expected a silent verify, got errors %v warnings %v", errs, warns)
				}
				return
			}

			findProblem(t, problems, tc.wantSeverity, tc.wantDest, tc.wantMessage)
			if tc.wantSeverity == plan.SeverityWarning && len(errs) != 0 {
				t.Fatalf("expected warnings only, got errors %v", errs)
			}
			if tc.wantSeverity == plan.SeverityError && len(errs) != 1 {
				t.Fatalf("expected exactly one error, got %v", errs)
			}
		})
	}
}

func TestVerifyPlanReportsTimeoutsAsUnreachable(t *testing.T) {
	ws := fixture(t)
	p, _, err := Discover(ws.Root)
	if err != nil {
		t.Fatal(err)
	}
	// A remote that never answers must not wedge a pack. The timeout is a
	// package variable precisely so this test can prove it.
	prev := NetworkTimeout
	NetworkTimeout = 50 * time.Millisecond
	t.Cleanup(func() { NetworkTimeout = prev })

	setSource(p, "repos/backend", func(s *plan.Source) {
		s.Remote = "file://" + filepath.Join(ws.RemotesDir, "never-existed.git")
	})

	errs, _ := plan.Problems(VerifyPlan(p))
	if len(errs) != 1 {
		t.Fatalf("expected one error, got %v", errs)
	}
	if !strings.Contains(errs[0].Message, "unreachable") && !strings.Contains(errs[0].Message, "did not answer") {
		t.Fatalf("unhelpful message: %s", errs[0].Message)
	}
}

func TestVerifyPlanHandlesNil(t *testing.T) {
	if ps := VerifyPlan(nil); ps != nil {
		t.Fatalf("VerifyPlan(nil) = %v", ps)
	}
}

func setSource(p *plan.Plan, dest string, f func(*plan.Source)) {
	for i := range p.Sources {
		if p.Sources[i].Dest == dest {
			f(&p.Sources[i])
			return
		}
	}
}
