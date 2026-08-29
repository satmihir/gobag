package main

import (
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/satmihir/gobag/internal/gitops"
	"github.com/satmihir/gobag/internal/manifest"
	"github.com/satmihir/gobag/internal/plan"
	"github.com/satmihir/gobag/internal/stage"
)

// cmdSeal turns the living stage into a shippable archive.
//
// This is the moment the encryption boundary is crossed: the stage sits
// unencrypted beside the repositories it describes, which is fine while it
// stays on the box. A seal is what leaves, so a seal is what gets encrypted,
// verified, and secret-scanned. Because the stage is kept warm, this costs
// seconds rather than a full interrogation — which is the point, since cheap
// seals are the ones that actually happen.
func cmdSeal(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("seal", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var opts packOptions
	fs.StringVar(&opts.out, "o", "", "output archive path")
	fs.StringVar(&opts.out, "out", "", "output archive path (alias for -o)")
	fs.BoolVar(&opts.plaintext, "plaintext", false, "do not encrypt")
	fs.BoolVar(&opts.transcripts, "transcripts", false, "include session transcripts")
	label := fs.String("label", "", "one line on why this moment mattered")
	refresh := fs.Bool("refresh", true, "re-resolve refs before sealing")
	fs.Usage = func() {
		io.WriteString(stderr, `usage: gobag seal [-o FILE] [-label "..."] [-plaintext] [root]

Seals the workspace's staged record into an encrypted archive. Requires a
stage; for a workspace without one, use gobag pack.
`)
	}
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return errUser("seal takes at most one directory")
	}

	root, err := resolveRoot(firstOr(rest, ""))
	if err != nil {
		return err
	}
	if !stage.Exists(root) {
		return errUser("no stage in %s — run `gobag stage init` first, or use `gobag pack` for a one-shot archive", root)
	}

	s, err := stage.Load(root)
	if err != nil {
		return wrapUser(err)
	}

	// The staged plan describes the thread as of the last refresh. Sealing a
	// record that has drifted from the working tree would ship a lie, so
	// refresh unless told not to.
	if *refresh {
		for _, pr := range gitops.RefreshPlan(s.Plan) {
			fmt.Fprintf(stdout, "  %s\n", problemLine(pr))
		}
		if err := s.Save(); err != nil {
			return wrapUser(err)
		}
	}

	// The living narrative travels as the handoff. It is the payload; a seal
	// without it would be a bag of references and nothing else.
	p := s.Plan
	if path, dest, ok := s.StagedHandoffEntry(); ok {
		p = withHandoff(p, path, dest)
	} else {
		fmt.Fprintln(stdout, "note: the stage has no handoff document — sealing references only")
	}

	problems := append(plan.Validate(p), gitops.VerifyPlan(p)...)
	problems = dedupeProblems(problems)
	plan.SortProblems(problems)
	errs, warns := plan.Problems(problems)
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(stderr, "  %s\n", problemLine(e))
		}
		return errUser("the staged plan has %d problem(s) that must be fixed before sealing", len(errs))
	}

	lineage := &manifest.Manifest{
		Series:   s.Meta.Series,
		Sequence: s.Meta.Sequence + 1,
		Previous: s.Meta.PreviousSeal,
		Label:    *label,
	}
	out, err := runPack(p, root, opts, warns, lineage, stdout, stderr)
	if err != nil {
		return err
	}

	// Advance the lineage only once the archive actually exists.
	if err := s.MarkSealed(time.Now(), out); err != nil {
		return wrapUser(err)
	}
	fmt.Fprintf(stdout, "\nsealed as %s, sequence %d\n", s.Meta.Series, s.Meta.Sequence)
	return nil
}

// withHandoff returns a copy of the plan carrying the staged narrative, without
// mutating what is on disk. Any handoff the plan already names is replaced: the
// stage's own document is the authoritative one.
func withHandoff(p *plan.Plan, path, dest string) *plan.Plan {
	clone := *p
	clone.Context = nil
	for _, e := range p.Context {
		if e.Dest != dest {
			clone.Context = append(clone.Context, e)
		}
	}
	clone.Context = append(clone.Context, plan.Entry{Path: path, Dest: dest})
	return &clone
}
