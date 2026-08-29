package gitops

import (
	"fmt"
	"os"

	"github.com/satmihir/gobag/internal/plan"
)

// RefreshPlan re-resolves the mechanical facts of a staged plan in place: where
// each source's HEAD actually points now, which branch is checked out, and
// whether the tree is dirty.
//
// Deliberately local and network-free. This runs from hooks — including
// SessionEnd, which has a budget measured in milliseconds — so it must never
// reach a remote, never prompt, and never hang. Remote-dependent facts (how far
// the world moved) belong to status, which the user asks for explicitly.
//
// A source whose path has vanished is reported and left exactly as staged: the
// stage remembers a thread, and a repository being temporarily absent is not a
// reason to forget what it was.
func RefreshPlan(p *plan.Plan) []plan.Problem {
	if p == nil {
		return nil
	}
	var problems []plan.Problem

	for i := range p.Sources {
		s := &p.Sources[i]
		if !present(s.Path) {
			problems = append(problems, plan.Problem{
				Severity: plan.SeverityWarning,
				Dest:     s.Dest,
				Message:  fmt.Sprintf("no longer on disk at %s; left as staged", s.Path),
			})
			continue
		}
		problems = append(problems, refreshOne(s.Path, s.Dest, &s.Ref, &s.Branch)...)

		for j := range s.Worktrees {
			w := &s.Worktrees[j]
			if !present(w.Path) {
				problems = append(problems, plan.Problem{
					Severity: plan.SeverityWarning,
					Dest:     w.Dest,
					Message:  fmt.Sprintf("worktree no longer on disk at %s; left as staged", w.Path),
				})
				continue
			}
			problems = append(problems, refreshOne(w.Path, w.Dest, &w.Ref, &w.Branch)...)
		}
	}
	plan.SortProblems(problems)
	return problems
}

// refreshOne updates one checkout's ref and branch, reporting what it found.
func refreshOne(path, dest string, ref, branch *string) []plan.Problem {
	var problems []plan.Problem

	sha, err := head(path)
	if err != nil {
		return append(problems, plan.Problem{
			Severity: plan.SeverityWarning,
			Dest:     dest,
			Message:  fmt.Sprintf("could not read HEAD; left as staged (%v)", firstLine(err.Error())),
		})
	}
	*ref = sha
	*branch = branchOf(path)

	if d, err := dirty(path); err == nil && d {
		problems = append(problems, plan.Problem{
			Severity: plan.SeverityWarning,
			Dest:     dest,
			Message:  "uncommitted changes will not travel; commit them before sealing if you need them",
		})
	}
	return problems
}

func present(path string) bool {
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// CheckoutState reports a checkout's current commit and whether its tracked
// files have uncommitted changes. Local and cheap: safe from a hook.
func CheckoutState(dir string) (ref string, isDirty bool) {
	if sha, err := head(dir); err == nil {
		ref = sha
	}
	if d, err := dirty(dir); err == nil {
		isDirty = d
	}
	return ref, isDirty
}
