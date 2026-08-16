package gitops

import (
	"fmt"

	"github.com/satmihir/gobag/internal/plan"
)

// VerifyPlan checks the claims in a plan that only git can settle, and is the
// companion to the pure plan.Validate. A plan may have been written by an
// agent, so nothing in it is taken on trust: every repository is opened, every
// ref is resolved, and every remote is contacted.
//
// Errors mean the archive would not restore. Warnings mean it would restore,
// but not into the state the user is picturing.
func VerifyPlan(p *plan.Plan) []plan.Problem {
	if p == nil {
		return nil
	}
	var problems []plan.Problem
	for _, s := range p.Sources {
		problems = append(problems, verifySource(s)...)
	}
	return problems
}

func verifySource(s plan.Source) []plan.Problem {
	var problems []plan.Problem
	errf := func(dest, format string, a ...any) {
		problems = append(problems, plan.Problem{Severity: plan.SeverityError, Dest: dest, Message: fmt.Sprintf(format, a...)})
	}
	warnf := func(dest, format string, a ...any) {
		problems = append(problems, plan.Problem{Severity: plan.SeverityWarning, Dest: dest, Message: fmt.Sprintf(format, a...)})
	}

	if !isRepo(s.Path) {
		errf(s.Dest, "%s is not a git repository", s.Path)
		return problems
	}

	if _, err := resolve(s.Path, s.Ref); err != nil {
		errf(s.Dest, "ref %s does not exist in %s", short(s.Ref), s.Path)
	} else if ok, _ := pushed(s.Path, s.Ref); !ok {
		warnf(s.Dest, "commit %s is on no remote branch; the restore will fail until you push it", short(s.Ref))
	}

	if d, err := dirty(s.Path); err == nil && d {
		warnf(s.Dest, "uncommitted changes will not travel; commit them before packing if you need them")
	}

	switch {
	case s.Remote == "":
		errf(s.Dest, "no remote is recorded, so this repository cannot travel as a reference")
	default:
		// ls-remote is the cheapest honest test that a restore could clone
		// this, and the only network call verification makes. No refspec is
		// given on purpose: the question is whether the remote answers, not
		// whether it publishes any particular ref.
		if _, err := network(s.Path, "ls-remote", "--quiet", s.Remote); err != nil {
			if timedOut(err) {
				errf(s.Dest, "remote %s did not answer within %s", s.Remote, NetworkTimeout)
			} else {
				errf(s.Dest, "remote %s is unreachable: %v", s.Remote, firstLine(err.Error()))
			}
		}
	}

	for _, w := range s.Worktrees {
		// A worktree's objects live in its parent, so the parent is where the
		// ref has to resolve — the worktree directory may not even exist.
		if _, err := resolve(s.Path, w.Ref); err != nil {
			errf(w.Dest, "ref %s does not exist in %s", short(w.Ref), s.Path)
			continue
		}
		if ok, _ := pushed(s.Path, w.Ref); !ok {
			warnf(w.Dest, "commit %s is on no remote branch; the restore will fail until you push it", short(w.Ref))
		}
		if isRepo(w.Path) {
			if d, err := dirty(w.Path); err == nil && d {
				warnf(w.Dest, "uncommitted changes will not travel; commit them before packing if you need them")
			}
		}
	}
	return problems
}
