package gitops

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/satmihir/gobag/internal/plan"
)

// candidate is one git checkout found by the walk, before parents and
// children are bound to each other.
type candidate struct {
	path   string // absolute working-tree path
	dest   string // archive-relative, forward slashes
	linked bool   // a .git file rather than a .git directory
	parent string // absolute parent working tree, linked checkouts only
}

// Discover walks root and produces the plan a walk-mode pack would carry: the
// repositories it finds as references, plus whatever context and skills are
// sitting in the conventional places. It is the reason the binary is useful
// with no session present.
//
// Problems describe what the user should know before sealing: uncommitted
// work that will not travel, refs that were never pushed, and repositories
// that cannot travel at all.
func Discover(root string) (*plan.Plan, []plan.Problem, error) {
	root, err := normalizeRoot(root)
	if err != nil {
		return nil, nil, err
	}

	found, problems, err := walk(root)
	if err != nil {
		return nil, nil, err
	}

	p := &plan.Plan{PlanVersion: plan.Version, Name: filepath.Base(root)}

	// Index the plain repositories so linked worktrees can find their parent.
	byPath := map[string]*plan.Source{}
	var order []string
	for _, c := range found {
		if c.linked {
			continue
		}
		s, ps := describe(c)
		problems = append(problems, ps...)
		if s == nil {
			continue
		}
		byPath[c.path] = s
		order = append(order, c.path)
	}

	for _, c := range found {
		if !c.linked {
			continue
		}
		parent, ok := byPath[c.parent]
		if !ok {
			// The parent repository lives outside root, so there is nothing
			// here to attach to. The worktree still travels — as its own
			// clone of the same remote at the same ref, which restores to
			// the same content even though it is no longer a linked worktree.
			problems = append(problems, plan.Problem{
				Severity: plan.SeverityWarning,
				Dest:     c.dest,
				Message: fmt.Sprintf("linked worktree of %s, which is outside the packed root; "+
					"it will be restored as a separate clone rather than a linked worktree", c.parent),
			})
			s, ps := describe(candidate{path: c.path, dest: c.dest})
			problems = append(problems, ps...)
			if s != nil {
				byPath[c.path] = s
				order = append(order, c.path)
			}
			continue
		}

		w, ps := describeWorktree(c)
		problems = append(problems, ps...)
		if w != nil {
			parent.Worktrees = append(parent.Worktrees, *w)
		}
	}

	for _, path := range order {
		s := byPath[path]
		sort.Slice(s.Worktrees, func(i, j int) bool { return s.Worktrees[i].Dest < s.Worktrees[j].Dest })
		p.Sources = append(p.Sources, *s)
	}
	sort.Slice(p.Sources, func(i, j int) bool { return p.Sources[i].Dest < p.Sources[j].Dest })

	p.Context = discoverContext(root)
	p.Skills = discoverSkills(root)

	plan.SortProblems(problems)
	return p, problems, nil
}

func normalizeRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolving root %s: %w", root, err)
	}
	// Temp directories on macOS are symlinks; resolve so that paths recorded
	// here match the ones git reports back.
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("reading root %s: %w", root, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("root %s is not a directory", root)
	}
	return abs, nil
}

// walk finds every checkout under root. A .git directory marks a repository;
// a .git file marks a linked worktree and points at its parent.
func walk(root string) ([]candidate, []plan.Problem, error) {
	var found []candidate
	var problems []plan.Problem

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subtree is the user's problem to fix, not a
			// reason to abandon the whole walk.
			problems = append(problems, plan.Problem{
				Severity: plan.SeverityWarning,
				Dest:     relDest(root, path),
				Message:  fmt.Sprintf("skipped, could not be read: %v", err),
			})
			return fs.SkipDir
		}
		if d.Name() != ".git" {
			return nil
		}
		work := filepath.Dir(path)
		c := candidate{path: work, dest: relDest(root, work)}
		if d.IsDir() {
			found = append(found, c)
			return fs.SkipDir // never descend into a git directory
		}
		c.linked = true
		parent, perr := parentOfWorktree(path, work)
		if perr != nil {
			problems = append(problems, plan.Problem{
				Severity: plan.SeverityWarning,
				Dest:     c.dest,
				Message:  fmt.Sprintf("looks like a linked worktree but its parent could not be located: %v", perr),
			})
			return nil
		}
		c.parent = parent
		found = append(found, c)
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("walking %s: %w", root, err)
	}

	sort.Slice(found, func(i, j int) bool { return found[i].dest < found[j].dest })
	return found, problems, nil
}

// parentOfWorktree resolves the "gitdir:" pointer in a linked worktree's .git
// file back to the working tree of the repository that owns it. The pointer
// names <parent-git-dir>/worktrees/<name>, so the parent is two levels up —
// and the parent may live anywhere on disk, including outside the packed root.
func parentOfWorktree(gitFile, work string) (string, error) {
	raw, err := os.ReadFile(gitFile)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", gitFile, err)
	}
	ptr := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(raw)), "gitdir:"))
	if ptr == "" || ptr == strings.TrimSpace(string(raw)) {
		return "", fmt.Errorf("%s has no gitdir: pointer", gitFile)
	}
	if !filepath.IsAbs(ptr) {
		ptr = filepath.Join(work, ptr)
	}
	ptr = filepath.Clean(ptr)

	if filepath.Base(filepath.Dir(ptr)) == "worktrees" {
		common := filepath.Dir(filepath.Dir(ptr)) // the parent's .git directory
		if filepath.Base(common) == ".git" {
			cand := filepath.Dir(common)
			if isRepo(cand) {
				return resolvedPath(cand), nil
			}
		}
	}

	// Separate git dirs and unusual layouts: ask git, which always lists the
	// main working tree first.
	out, err := local(work, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	for _, l := range lines(out) {
		if rest, ok := strings.CutPrefix(l, "worktree "); ok {
			return resolvedPath(strings.TrimSpace(rest)), nil
		}
	}
	return "", fmt.Errorf("git listed no main working tree for %s", work)
}

func resolvedPath(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return filepath.Clean(p)
}

// describe reads one repository into a plan Source, reporting what would stop
// it or surprise the user.
func describe(c candidate) (*plan.Source, []plan.Problem) {
	var problems []plan.Problem

	sha, err := head(c.path)
	if err != nil {
		return nil, append(problems, plan.Problem{
			Severity: plan.SeverityError,
			Dest:     c.dest,
			Message:  "repository has no commit at HEAD, so there is nothing to pin",
		})
	}

	_, url, err := remoteURL(c.path)
	if err != nil {
		return nil, append(problems, plan.Problem{
			Severity: plan.SeverityError,
			Dest:     c.dest,
			Message:  fmt.Sprintf("remotes could not be read: %v", err),
		})
	}
	if url == "" {
		// Repos travel as references. Without a remote there is no reference.
		problems = append(problems, plan.Problem{
			Severity: plan.SeverityError,
			Dest:     c.dest,
			Message:  "no remote is configured, so this repository cannot travel as a reference; add a remote and push it, or drop it from the plan",
		})
	}

	s := &plan.Source{
		Path:   c.path,
		Dest:   c.dest,
		Remote: url,
		Ref:    sha,
		Branch: branchOf(c.path),
	}
	problems = append(problems, stateProblems(c.path, c.dest, sha, url != "")...)
	return s, problems
}

// describeWorktree reads a linked worktree into a plan Worktree.
func describeWorktree(c candidate) (*plan.Worktree, []plan.Problem) {
	sha, err := head(c.path)
	if err != nil {
		return nil, []plan.Problem{{
			Severity: plan.SeverityError,
			Dest:     c.dest,
			Message:  "worktree has no commit at HEAD, so there is nothing to pin",
		}}
	}
	w := &plan.Worktree{
		Path:   c.path,
		Dest:   c.dest,
		Ref:    sha,
		Branch: branchOf(c.path),
	}
	return w, stateProblems(c.path, c.dest, sha, true)
}

// stateProblems reports the two things a user most often gets wrong before
// packing: work left uncommitted, and commits left unpushed.
func stateProblems(path, dest, sha string, hasRemote bool) []plan.Problem {
	var problems []plan.Problem
	if d, err := dirty(path); err == nil && d {
		problems = append(problems, plan.Problem{
			Severity: plan.SeverityWarning,
			Dest:     dest,
			Message:  "uncommitted changes will not travel; commit them before packing if you need them",
		})
	}
	if !hasRemote {
		return problems
	}
	if ok, _ := pushed(path, sha); !ok {
		problems = append(problems, plan.Problem{
			Severity: plan.SeverityWarning,
			Dest:     dest,
			Message:  fmt.Sprintf("commit %s is on no remote branch; the restore will fail until you push it", short(sha)),
		})
	}
	return problems
}

// discoverContext picks up the conventional handoff documents at the root.
// Absent files are not a problem: walk mode works without any of them.
func discoverContext(root string) []plan.Entry {
	var out []plan.Entry
	for _, name := range []string{"context.md", "HANDOFF.md"} {
		p := filepath.Join(root, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			out = append(out, plan.Entry{Path: p, Dest: "context/" + name})
		}
	}
	return out
}

// discoverSkills picks up project-local skills from .claude/skills.
func discoverSkills(root string) []plan.Entry {
	dir := filepath.Join(root, ".claude", "skills")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []plan.Entry
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		out = append(out, plan.Entry{
			Path: filepath.Join(dir, e.Name()),
			Dest: "skills/" + e.Name(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dest < out[j].Dest })
	return out
}

// relDest maps an absolute path under root to its archive-relative
// destination. A repository at the root itself keeps its own directory name so
// that the destination never collapses to ".".
func relDest(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == "" {
		return filepath.Base(root)
	}
	return filepath.ToSlash(rel)
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
