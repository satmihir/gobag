package plan

import (
	"fmt"
	"os"
	"sort"
)

// MaxContextBytes is the size past which a context document is reported as
// suspiciously large. Handoff notes are prose; anything this big is almost
// always a log or a dump the user did not mean to carry.
const MaxContextBytes = 5 << 20 // 5MB

// Severity separates the problems that stop a pack from the ones that only
// deserve a loud line of output. Errors exit 1; warnings continue.
type Severity int

const (
	SeverityWarning Severity = iota
	SeverityError
)

func (s Severity) String() string {
	if s == SeverityError {
		return "error"
	}
	return "warning"
}

// Problem is one finding about a plan, addressed to the user rather than to a
// log. Dest names the archive-relative subject; it is empty for problems that
// concern the plan as a whole.
type Problem struct {
	Severity Severity
	Dest     string
	Message  string
}

func (p Problem) String() string {
	if p.Dest == "" {
		return fmt.Sprintf("%s: %s", p.Severity, p.Message)
	}
	return fmt.Sprintf("%s: %s: %s", p.Severity, p.Dest, p.Message)
}

// Problems partitions findings by severity, preserving order within each half.
func Problems(ps []Problem) (errs, warns []Problem) {
	for _, p := range ps {
		if p.Severity == SeverityError {
			errs = append(errs, p)
		} else {
			warns = append(warns, p)
		}
	}
	return errs, warns
}

// Validate checks everything about a plan that can be checked without git and
// without the network: destinations are safe and unique, local paths exist,
// and each repository reference is complete. It is deliberately pure so that
// pack can reject a malformed plan before spending a single subprocess on it;
// gitops.VerifyPlan covers the claims that need git to settle.
func Validate(p *Plan) []Problem {
	var out []Problem
	seen := map[string]string{} // dest -> what first claimed it

	// check records one dest/path pair: destination legality, uniqueness, and
	// the existence of the local file or directory it will be read from.
	check := func(kind, dest, path string) os.FileInfo {
		if err := ValidateDest(dest); err != nil {
			out = append(out, Problem{SeverityError, dest, fmt.Sprintf("%s destination is unusable: %v", kind, err)})
		} else if first, dup := seen[dest]; dup {
			out = append(out, Problem{SeverityError, dest,
				fmt.Sprintf("%s destination is already claimed by the %s entry; two entries cannot land in the same place", kind, first)})
		} else {
			seen[dest] = kind
		}

		if path == "" {
			out = append(out, Problem{SeverityError, dest, fmt.Sprintf("%s entry has no local path", kind)})
			return nil
		}
		fi, err := os.Stat(path)
		if err != nil {
			out = append(out, Problem{SeverityError, dest, fmt.Sprintf("%s is missing on disk: %s", kind, path)})
			return nil
		}
		return fi
	}

	for _, s := range p.Sources {
		check("repository", s.Dest, s.Path)
		if s.Remote == "" {
			out = append(out, Problem{SeverityError, s.Dest,
				"repository has no remote; it cannot travel as a reference — add a remote and push, or leave it out"})
		}
		if s.Ref == "" {
			out = append(out, Problem{SeverityError, s.Dest, "repository has no pinned ref"})
		}
		for _, w := range s.Worktrees {
			check("worktree", w.Dest, w.Path)
			if w.Ref == "" {
				out = append(out, Problem{SeverityError, w.Dest, "worktree has no pinned ref"})
			}
		}
	}

	for _, e := range p.Context {
		if fi := check("context", e.Dest, e.Path); fi != nil && !fi.IsDir() && fi.Size() > MaxContextBytes {
			out = append(out, Problem{SeverityWarning, e.Dest,
				fmt.Sprintf("context document is %d MB; handoff notes are prose, so this is probably a log or a dump", fi.Size()>>20)})
		}
	}
	for _, e := range p.Skills {
		check("skill", e.Dest, e.Path)
	}
	for _, e := range p.State.Memory {
		check("memory", e.Dest, e.Path)
	}
	for _, e := range p.State.Transcripts {
		check("transcript", e.Dest, e.Path)
	}
	if e := p.State.MCP; e != nil {
		check("mcp", e.Dest, e.Path)
	}
	if e := p.MCP; e != nil {
		check("mcp", e.Dest, e.Path)
	}
	return out
}

// SortProblems orders findings errors-first, then by destination, then by
// message, so repeated runs print the same list.
func SortProblems(ps []Problem) {
	sort.SliceStable(ps, func(i, j int) bool {
		a, b := ps[i], ps[j]
		if a.Severity != b.Severity {
			return a.Severity > b.Severity
		}
		if a.Dest != b.Dest {
			return a.Dest < b.Dest
		}
		return a.Message < b.Message
	})
}
