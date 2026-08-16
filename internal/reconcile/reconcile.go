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

	"github.com/satmihir/gobag/internal/gitops"
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
}

// Render writes the orientation document.
func Render(w io.Writer, in Input) error {
	var b strings.Builder

	b.WriteString("# Orientation\n\n")
	b.WriteString(intro(in))

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

	if len(conflicts) == 0 && len(notes) == 0 {
		return
	}
	b.WriteString("\n## Conflicts and skips\n\n")
	for _, c := range conflicts {
		b.WriteString(fmt.Sprintf(
			"- `%s` already existed and differed. Your copy was kept; the archived "+
				"version is at `%s`.\n", c.Path, c.SidecarPath))
	}
	for _, n := range notes {
		b.WriteString("- " + n + "\n")
	}
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
