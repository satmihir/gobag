package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/satmihir/gobag/internal/gitops"
	"github.com/satmihir/gobag/internal/plan"
	"github.com/satmihir/gobag/internal/stage"
)

const stageUsage = `usage: gobag stage <init|refresh|status|nudge> [flags]

  init     [root] [-plan FILE]   start maintaining a record of this thread
  refresh  [-local] [-compaction] re-resolve refs, branches, dirty state
  status   [-json] [-remote]      report how far behind the record is
  nudge                           hook-facing: ask the agent to revise the
                                  narrative, but only when it is warranted

The stage is plain files in .gobag/stage/. It is the thread's living record:
read it before writing it, because the session doing the writing may have been
compacted since it last wrote.
`

func cmdStage(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		io.WriteString(stderr, stageUsage)
		return errUser("stage needs a subcommand")
	}
	switch args[0] {
	case "init":
		return stageInit(args[1:], stdout, stderr)
	case "refresh":
		return stageRefresh(args[1:], stdout, stderr)
	case "status":
		return stageStatus(args[1:], stdout, stderr)
	case "nudge":
		return stageNudge(args[1:], stdout, stderr)
	case "-h", "--help":
		io.WriteString(stdout, stageUsage)
		return nil
	default:
		io.WriteString(stderr, stageUsage)
		return errUser("unknown stage subcommand %q", args[0])
	}
}

func stageInit(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("stage init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	planPath := fs.String("plan", "", "seed the stage from an existing plan.json")
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return errUser("stage init takes at most one directory")
	}

	root, err := resolveRoot(firstOr(rest, ""))
	if err != nil {
		return err
	}

	var p *plan.Plan
	if *planPath != "" {
		p, err = plan.Load(*planPath)
		if err != nil {
			return wrapUser(err)
		}
	} else {
		fmt.Fprintf(stdout, "walking %s\n", root)
		discovered, problems, derr := gitops.Discover(root)
		if derr != nil {
			return derr
		}
		// Discovery errors are reported but do not block a stage: a record of
		// an imperfect workspace beats no record at all, and seal will refuse
		// later if the plan is still unusable then.
		for _, pr := range problems {
			fmt.Fprintf(stdout, "  %s\n", problemLine(pr))
		}
		p = discovered
	}
	if p.SourceRoot == "" {
		p.SourceRoot = root
	}

	s, err := stage.Init(root, p, time.Now())
	if err != nil {
		return wrapUser(err)
	}
	fmt.Fprintf(stdout, "\nstage created at %s (series %s)\n", stage.Dir(root), s.Meta.Series)
	fmt.Fprintf(stdout, "Write the thread's story into %s — that document is the payload.\n",
		filepath.Join(stage.DirName, stage.StageName, stage.HandoffFile))
	return nil
}

func stageRefresh(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("stage refresh", flag.ContinueOnError)
	fs.SetOutput(stderr)
	local := fs.Bool("local", false, "local only: never contact a remote (default for hooks)")
	compaction := fs.Bool("compaction", false, "also record that a compaction just happened")
	quiet := fs.Bool("quiet", false, "print nothing on success")
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	_ = *local // refresh is local-only by construction; the flag documents intent

	root, err := resolveRoot(firstOr(rest, ""))
	if err != nil {
		return err
	}
	// Hooks run everywhere, including workspaces that never staged anything.
	// Silence and success is the only acceptable behavior there.
	if !stage.Exists(root) {
		return nil
	}

	s, err := stage.Load(root)
	if err != nil {
		return wrapUser(err)
	}
	problems := gitops.RefreshPlan(s.Plan)
	if err := s.MarkRefreshed(time.Now(), *compaction); err != nil {
		return wrapUser(err)
	}
	if *quiet {
		return nil
	}

	fmt.Fprintf(stdout, "refreshed %s\n", stage.Dir(root))
	for _, pr := range problems {
		fmt.Fprintf(stdout, "  %s\n", problemLine(pr))
	}
	if *compaction {
		fmt.Fprintln(stdout, "  recorded a compaction — the narrative is now behind")
	}
	return nil
}

func stageStatus(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("stage status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "machine-readable output")
	withRemote := fs.Bool("remote", false, "also compare against remotes (costs network)")
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}

	root, err := resolveRoot(firstOr(rest, ""))
	if err != nil {
		return err
	}
	if !stage.Exists(root) {
		if *asJSON {
			return json.NewEncoder(stdout).Encode(map[string]any{"exists": false})
		}
		fmt.Fprintf(stdout, "no stage in %s — run: gobag stage init\n", root)
		return nil
	}

	s, err := stage.Load(root)
	if err != nil {
		return wrapUser(err)
	}
	st := s.Status(time.Now(), *withRemote)

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}
	printStageStatus(stdout, st)
	return nil
}

func printStageStatus(w io.Writer, st stage.Status) {
	fmt.Fprintf(w, "series %s, %s\n", st.Series, sealCount(st.Sequence))

	switch {
	case st.HandoffMissing:
		fmt.Fprintln(w, "\nnarrative: MISSING — the record has no handoff document")
	case st.CompactionSince:
		fmt.Fprintf(w, "\nnarrative: BEHIND — context was compacted %s, after the last revision.\n"+
			"  Whatever that session learned and did not write down is gone.\n",
			humanStamp(st.LastCompaction))
	case st.HandoffStale:
		fmt.Fprintf(w, "\nnarrative: stale — last revised %s ago\n", st.HandoffAge)
	case st.HandoffAge != "":
		fmt.Fprintf(w, "\nnarrative: last revised %s ago\n", st.HandoffAge)
	default:
		fmt.Fprintln(w, "\nnarrative: fresh")
	}

	fmt.Fprintln(w, "\nsources:")
	for _, src := range st.Sources {
		switch {
		case src.Missing:
			fmt.Fprintf(w, "  %s — no longer on disk\n", src.Dest)
			continue
		case src.Moved:
			fmt.Fprintf(w, "  %s — moved since staged (%s → %s)%s\n",
				src.Dest, shortRef(src.StagedRef), shortRef(src.CurrentRef), dirtyNote(src.Dirty))
		default:
			fmt.Fprintf(w, "  %s — at %s%s\n", src.Dest, shortRef(src.StagedRef), dirtyNote(src.Dirty))
		}
		if r := src.Reality; r != nil {
			switch {
			case r.Unreachable:
				fmt.Fprintln(w, "      remote unreachable")
			case r.BaseBranch != "" && r.BaseBehind > 0:
				fmt.Fprintf(w, "      %s advanced %s underneath this branch\n",
					r.BaseBranch, plural(r.BaseBehind, "commit"))
			case r.Ahead > 0:
				fmt.Fprintf(w, "      remote advanced %s\n", plural(r.Ahead, "commit"))
			}
		}
	}

	if st.NeedsRefresh() {
		fmt.Fprintln(w, "\nrun `gobag stage refresh` to bring the record up to date")
	}
}

// stageNudge is the hook-facing path. It prints nothing at all unless the stage
// genuinely wants attention, because it runs on every prompt.
func stageNudge(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("stage nudge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}

	root, err := resolveRoot(firstOr(rest, ""))
	if err != nil {
		return nil // a hook must never fail a session over an unusable path
	}
	if !stage.Exists(root) {
		return nil
	}

	s, err := stage.Load(root)
	if err != nil {
		return nil
	}
	now := time.Now()
	_, message, want := s.Nudge(now)
	if !want {
		return nil
	}
	if err := s.MarkNudged(now); err != nil {
		return nil
	}

	// The shape Claude Code reads for UserPromptSubmit hooks.
	return json.NewEncoder(stdout).Encode(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "UserPromptSubmit",
			"additionalContext": message,
		},
	})
}

// sealCount reads like a sentence rather than a counter.
func sealCount(n int) string {
	switch n {
	case 0:
		return "never sealed"
	case 1:
		return "sealed once"
	default:
		return fmt.Sprintf("sealed %d times", n)
	}
}

// humanStamp renders a recorded time for a person. The stage stores nanosecond
// precision because it must compare instants correctly; nobody should have to
// read that.
func humanStamp(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Local().Format("15:04 on 2 Jan")
}

func dirtyNote(dirty bool) string {
	if dirty {
		return ", uncommitted changes"
	}
	return ""
}

func firstOr(args []string, fallback string) string {
	if len(args) > 0 {
		return args[0]
	}
	return fallback
}
