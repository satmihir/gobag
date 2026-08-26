// Package reconcile generates ORIENTATION.md, the tool's half of the
// reconciliation handshake.
//
// The handshake has three voices. The past agent wrote its expectations into
// HANDOFF.md. This package states facts: what was restored, and how the world
// moved while the bag was packed. The restored agent reconciles the two.
//
// Facts only. Nothing here speculates about intent — that is the handoff
// document's job, and blurring the two would make both less trustworthy.
// Output is deterministic (everything sorted) so it can be golden-tested.
package reconcile

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/satmihir/gobag/internal/gitops"
	"github.com/satmihir/gobag/internal/host"
	"github.com/satmihir/gobag/internal/manifest"
	"github.com/satmihir/gobag/internal/overlay"
)

// Name is the orientation document's filename at the workspace root.
const Name = "ORIENTATION.md"

// Input is everything install learned, assembled for rendering.
type Input struct {
	Manifest *manifest.Manifest
	// Root is the absolute workspace root the archive was restored into.
	Root string
	// Repos holds one result per source and worktree, in any order.
	Repos []gitops.Result
	// Reality holds the remote-vs-pinned diff per source, in any order.
	Reality []gitops.Reality
	// Files holds every overlay outcome from unpacking context, skills, state.
	Files []overlay.Result
	// MemoryRewritten lists memory files whose paths were substituted.
	MemoryRewritten []string
	// Notes carries anything else worth surfacing: skipped transcripts,
	// re-key failures, secret-scan warnings recorded at pack time.
	Notes []string
	// HandoffPath is the archive-relative handoff document, if one travelled.
	HandoffPath string
	// CurrentHost is this machine, compared against the one that packed the
	// archive. A restore that cannot say which machine it is on invites the
	// reader to guess, and a uniform fleet makes the wrong guess look obvious.
	CurrentHost host.Info
}

// Render writes the orientation document.
func Render(w io.Writer, in Input) error {
	var b strings.Builder

	b.WriteString("# Orientation\n\n")
	b.WriteString(intro(in))
	writeHostVerdict(&b, in)

	writeRestored(&b, in)
	writeSincePacked(&b, in)
	writeConflicts(&b, in)
	writeStartHere(&b, in)

	_, err := io.WriteString(w, b.String())
	return err
}

func intro(in Input) string {
	name, created, from := "this workspace", "", ""
	if in.Manifest != nil {
		if in.Manifest.Name != "" {
			name = in.Manifest.Name
		}
		if in.Manifest.Created != "" {
			created = " packed " + in.Manifest.Created
		}
		if in.Manifest.SourceRoot != "" && in.Manifest.SourceRoot != in.Root {
			from = fmt.Sprintf(" It was packed from `%s` and restored to `%s`.",
				in.Manifest.SourceRoot, in.Root)
		}
	}
	return fmt.Sprintf(
		"You are a new session picking up %s,%s. This document states what is "+
			"on disk and what changed while the workspace was packed; it does "+
			"not describe intent.%s\n",
		name, orDefault(created, " packed earlier"), from)
}

// writeHostVerdict states the same-machine question outright, in both
// directions.
//
// Leaving it unstated is how a restore goes wrong quietly: on a fleet with a
// uniform layout, same-named clones sit at the same paths on every box, so a
// reader with no host information will reasonably conclude "same machine,
// stale bag" and start editing the wrong tree. The comparison is cheap; the
// wrong guess is not.
func writeHostVerdict(b *strings.Builder, in Input) {
	var packed *host.Info
	if in.Manifest != nil {
		packed = in.Manifest.Host
	}
	if packed == nil {
		b.WriteString("\n**This archive records no host identity** (it predates that field), " +
			"so whether it was packed on this machine cannot be established. Treat " +
			"same-named repositories already present here as unrelated until you have " +
			"checked, and do your work in the restored tree.\n")
		return
	}

	switch host.Compare(*packed, in.CurrentHost) {
	case host.SameHost:
		b.WriteString(fmt.Sprintf(
			"\n**Packed and restored on the same machine** (%s). Anything you recognize "+
				"here may genuinely be the workspace this bag came from.\n",
			packed.Describe()))
	case host.DifferentHost:
		b.WriteString(fmt.Sprintf(
			"\n**Packed on %s; restored on %s — different machines.** Any same-named "+
				"repository or path that already exists here is unrelated to this archive, "+
				"however familiar it looks. The work this bag carries is in the restored "+
				"tree at `%s`.\n",
			packed.Describe(), in.CurrentHost.Describe(), in.Root))
	default:
		b.WriteString(fmt.Sprintf(
			"\n**Cannot determine whether this is the machine that packed the archive** "+
				"(packed on %s; no stable identifier on one side or the other). Do not "+
				"assume a same-named repository here is the one this bag describes.\n",
			packed.Describe()))
	}
}

func writeRestored(b *strings.Builder, in Input) {
	repos := append([]gitops.Result(nil), in.Repos...)
	sort.Slice(repos, func(i, j int) bool { return repos[i].Dest < repos[j].Dest })

	b.WriteString("\n## Restored\n\n")
	if len(repos) == 0 {
		b.WriteString("No repositories travelled in this archive.\n")
	}
	for _, r := range repos {
		line := fmt.Sprintf("- `%s` — %s", r.Dest, r.Outcome)
		if r.Ref != "" {
			line += " at " + shortRef(r.Ref)
		}
		if r.Detail != "" {
			line += " (" + r.Detail + ")"
		}
		b.WriteString(line + "\n")
	}

	written, identical := 0, 0
	for _, f := range in.Files {
		switch f.Outcome {
		case overlay.OutcomeWritten:
			written++
		case overlay.OutcomeIdentical:
			identical++
		}
	}
	if written+identical > 0 {
		b.WriteString(fmt.Sprintf("- %s unpacked", plural(written, "state and context file")))
		if identical > 0 {
			b.WriteString(fmt.Sprintf(" (%d already matched)", identical))
		}
		b.WriteString("\n")
	}
	if n := len(in.MemoryRewritten); n > 0 {
		b.WriteString(fmt.Sprintf(
			"- %s had workspace paths rewritten for this machine\n", plural(n, "memory file")))
	}
}

func writeSincePacked(b *strings.Builder, in Input) {
	reality := append([]gitops.Reality(nil), in.Reality...)
	sort.Slice(reality, func(i, j int) bool { return reality[i].Dest < reality[j].Dest })

	b.WriteString("\n## Since you were packed\n\n")

	var moved bool
	for _, r := range reality {
		// Base drift is reported first and unconditionally: for a bag packed
		// mid-pull-request it is the whole answer, and "the tip has not moved"
		// on its own reads as "nothing happened".
		if r.BaseBranch != "" && r.BaseBehind > 0 {
			b.WriteString(fmt.Sprintf(
				"- `%s` — pinned to `%s`, whose tip is unchanged, but `%s` has advanced "+
					"%s underneath it (%d ahead, %d behind). Anything the handoff says about "+
					"merge state, CI, or dependency versions may already be false.\n",
				r.Dest, r.PinnedBranch, r.BaseBranch,
				plural(r.BaseBehind, "commit"), r.BaseAhead, r.BaseBehind))
			moved = true
		}

		switch {
		case r.Unreachable:
			b.WriteString(fmt.Sprintf(
				"- `%s` — remote unreachable; the working copy is at the pinned ref %s "+
					"and may be behind.\n", r.Dest, shortRef(r.PinnedRef)))
			moved = true
		case r.Ahead > 0:
			b.WriteString(fmt.Sprintf(
				"- `%s` — the remote advanced %s since this bag was packed.\n",
				r.Dest, plural(r.Ahead, "commit")))
			moved = true
		}
	}
	for _, r := range in.Repos {
		if r.Outcome == gitops.OutcomeLeftDiverged || r.Outcome == gitops.OutcomeLeftDirty ||
			r.Outcome == gitops.OutcomeRemoteMismatch {
			b.WriteString(fmt.Sprintf("- `%s` — %s; gobag left it untouched.\n", r.Dest, r.Outcome))
			moved = true
		}
	}
	if !moved {
		b.WriteString("Nothing moved: every repository is at the ref recorded in the archive.\n")
	}
}

func writeConflicts(b *strings.Builder, in Input) {
	var conflicts []overlay.Result
	for _, f := range in.Files {
		if f.Conflicted() {
			conflicts = append(conflicts, f)
		}
	}
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].Path < conflicts[j].Path })

	notes := append([]string(nil), in.Notes...)
	sort.Strings(notes)

	extra := advisories(in)
	if len(conflicts) == 0 && len(notes) == 0 && len(extra) == 0 {
		return
	}
	b.WriteString("\n## Conflicts and skips\n\n")
	for _, a := range extra {
		b.WriteString("- " + a + "\n")
	}
	for _, c := range conflicts {
		b.WriteString(fmt.Sprintf(
			"- `%s` already existed and differed. Your copy was kept; the archived "+
				"version is at `%s`.\n", c.Path, c.SidecarPath))
	}
	for _, n := range notes {
		b.WriteString("- " + n + "\n")
	}
}

// advisories are the facts that are nobody's fault and still change how much
// the handoff can be trusted.
func advisories(in Input) []string {
	if in.Manifest == nil {
		return nil
	}
	var out []string

	// A handoff written days before the pack describes a world that had
	// already moved on by the time the bag was sealed.
	if in.HandoffPath != "" {
		if written, ok := in.Manifest.ContextModified[in.HandoffPath]; ok {
			if gap, ok := staleness(written, in.Manifest.Created); ok {
				out = append(out, fmt.Sprintf(
					"`%s` was last modified %s before this archive was packed. Time-sensitive "+
						"claims in it — pull request states, CI results, version pins, what is "+
						"still open — were already that old when the bag was sealed.",
					in.HandoffPath, gap))
			}
		}
	}

	if n := len(in.Manifest.SoleCopies); n > 0 {
		sole := append([]string(nil), in.Manifest.SoleCopies...)
		sort.Strings(sole)
		out = append(out, fmt.Sprintf(
			"This archive is the only copy of %s that %s in no commit: %s. "+
				"If that work matters, commit it somewhere durable.",
			plural(n, "file"), agree(n, "exists", "exist"),
			"`"+strings.Join(sole, "`, `")+"`"))
	}
	return out
}

// staleness reports how long before the pack a document was last written,
// suppressing gaps too small to be worth a line.
func staleness(written, packed string) (string, bool) {
	w, err1 := time.Parse(time.RFC3339, written)
	p, err2 := time.Parse(time.RFC3339, packed)
	if err1 != nil || err2 != nil {
		return "", false
	}
	gap := p.Sub(w)
	if gap < 24*time.Hour {
		return "", false
	}
	days := int(gap.Hours()) / 24
	hours := int(gap.Hours()) % 24
	if hours == 0 {
		return plural(days, "day"), true
	}
	return fmt.Sprintf("%s %dh", plural(days, "day"), hours), true
}

func writeStartHere(b *strings.Builder, in Input) {
	b.WriteString("\n## Start here\n\n")
	if in.HandoffPath == "" {
		b.WriteString("No handoff document travelled in this archive. " +
			"Read the repositories listed above to orient yourself.\n")
		return
	}
	b.WriteString(fmt.Sprintf(
		"Read `%s` next. It carries the previous session's account of the work: "+
			"goal and status, open threads, decisions and their reasoning, and where "+
			"it expected things to be. Where it disagrees with this document, this "+
			"document describes the disk and the handoff describes the intent.\n",
		in.HandoffPath))
}

func shortRef(ref string) string {
	if len(ref) > 12 {
		return ref[:12]
	}
	return ref
}

// agree picks a verb form matching a count.
func agree(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
