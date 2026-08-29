package stage

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/satmihir/gobag/internal/gitops"
	"github.com/satmihir/gobag/internal/manifest"
	"github.com/satmihir/gobag/internal/reconcile"
)

// Status is what the stage knows about its own freshness.
//
// A stale stage that looks alive is the same disease as a bag that cannot say
// which machine packed it: the reader draws a confident wrong conclusion. So
// the stage must always be able to say how far behind it is.
type Status struct {
	Series   string
	Sequence int

	// HandoffAge is human-readable ("3 days 4h"), empty when fresh enough to
	// be unremarkable.
	HandoffAge   string
	HandoffStale bool
	// HandoffMissing means the narrative file is gone entirely.
	HandoffMissing bool

	// CompactionSince reports that a session's context was compacted after the
	// narrative was last revised — so whatever that session learned before the
	// compaction is not in the record, and is now gone.
	CompactionSince bool
	LastCompaction  string
	LastRefresh     string

	Sources []SourceStatus
}

// SourceStatus is one repository's drift from what the stage recorded.
type SourceStatus struct {
	Dest string
	// StagedRef is what the stage believes; CurrentRef is what the checkout is
	// actually at. They differ when work happened since the last refresh.
	StagedRef  string
	CurrentRef string
	Moved      bool
	Missing    bool
	Dirty      bool

	// Reality is the remote comparison, populated only when status was asked
	// for it — it costs network.
	Reality *gitops.Reality
}

// NeedsRefresh reports whether the mechanical facts have drifted.
func (s Status) NeedsRefresh() bool {
	for _, src := range s.Sources {
		if src.Moved {
			return true
		}
	}
	return false
}

// Status computes the stage's freshness. withRemote adds the remote
// comparison, which costs network and is therefore never done from a hook.
func (s *Stage) Status(now time.Time, withRemote bool) Status {
	out := Status{
		Series:         s.Meta.Series,
		Sequence:       s.Meta.Sequence,
		LastCompaction: s.Meta.LastCompaction,
		LastRefresh:    s.Meta.LastRefresh,
	}

	_, modified, ok := s.Handoff()
	if !ok {
		out.HandoffMissing = true
	} else {
		// Reuse the archive-side staleness wording so the stage and
		// ORIENTATION.md never describe the same gap differently.
		if age, stale := reconcile.Staleness(stamp(modified), stamp(now)); stale {
			out.HandoffAge, out.HandoffStale = age, now.Sub(modified) >= StaleAfter
			if !out.HandoffStale {
				// Older than a day but not yet stale: worth showing, not
				// worth complaining about.
				out.HandoffAge = age
			}
		}
		if compacted, ok := parseStamp(s.Meta.LastCompaction); ok && compacted.After(modified) {
			out.CompactionSince = true
		}
	}

	for _, src := range s.Plan.Sources {
		st := SourceStatus{Dest: src.Dest, StagedRef: src.Ref}
		if !present(src.Path) {
			st.Missing = true
			out.Sources = append(out.Sources, st)
			continue
		}
		st.CurrentRef, st.Dirty = gitops.CheckoutState(src.Path)
		st.Moved = st.CurrentRef != "" && st.CurrentRef != src.Ref

		if withRemote {
			if r, err := gitops.RealityAt(src.Path, manifest.Source{
				Dest: src.Dest, Remote: src.Remote, Ref: src.Ref, Branch: src.Branch,
			}); err == nil {
				st.Reality = &r
			}
		}
		out.Sources = append(out.Sources, st)
	}
	return out
}

// NudgeReason is why the stage wants the agent's attention. Empty means it does
// not — which is the overwhelmingly common case, and the reason a nudge can sit
// on the prompt path without becoming a nag.
type NudgeReason string

const (
	// NudgeCompaction is the important one: context was lost after the record
	// was last revised.
	NudgeCompaction NudgeReason = "compaction"
	// NudgeStale means the narrative has simply aged out.
	NudgeStale NudgeReason = "stale"
)

// Nudge decides whether to ask the agent to revise the narrative, and returns
// the message to inject.
//
// Each condition is reported at most once: a nudge already delivered for a
// given compaction is not delivered again, because a reminder repeated on every
// prompt is noise the reader learns to skip.
func (s *Stage) Nudge(now time.Time) (NudgeReason, string, bool) {
	_, modified, ok := s.Handoff()
	if !ok {
		return "", "", false
	}
	lastNudge, hasNudge := parseStamp(s.Meta.LastNudge)

	if compacted, ok := parseStamp(s.Meta.LastCompaction); ok && compacted.After(modified) {
		if !hasNudge || lastNudge.Before(compacted) {
			return NudgeCompaction, fmt.Sprintf(
				"Your context was compacted at %s. This workspace keeps a running record of "+
					"the thread at %s, last revised before that compaction — so anything the "+
					"compacted session learned and did not write down is now only in your "+
					"summary, and will be gone entirely next time. Read that file, then "+
					"revise it to cover what is missing. Read it first: it may already "+
					"contain work you no longer remember doing.",
				compacted.Local().Format("15:04 on 2 Jan"),
				filepath.Join(DirName, StageName, HandoffFile)), true
		}
		return "", "", false
	}

	if now.Sub(modified) >= StaleAfter {
		if !hasNudge || now.Sub(lastNudge) >= StaleAfter {
			age, _ := reconcile.Staleness(stamp(modified), stamp(now))
			return NudgeStale, fmt.Sprintf(
				"This workspace keeps a running record of the thread at %s. It was last "+
					"revised %s ago, so its account of open threads, pull request states, and "+
					"version pins may have drifted. Read it and revise what has changed.",
				filepath.Join(DirName, StageName, HandoffFile), age), true
		}
	}
	return "", "", false
}

// MarkNudged records that a nudge was delivered, so the same condition is not
// reported again.
func (s *Stage) MarkNudged(now time.Time) error {
	s.Meta.LastNudge = stamp(now)
	return s.Save()
}

// MarkRefreshed records a mechanical refresh, optionally noting that it was
// triggered by a compaction.
func (s *Stage) MarkRefreshed(now time.Time, compaction bool) error {
	s.Meta.LastRefresh = stamp(now)
	if compaction {
		s.Meta.LastCompaction = stamp(now)
	}
	return s.Save()
}

// MarkSealed records that the stage became an archive, advancing the sequence
// and remembering the checksum so seals form a lineage.
func (s *Stage) MarkSealed(now time.Time, checksum string) error {
	s.Meta.Sequence++
	s.Meta.LastSeal = stamp(now)
	s.Meta.PreviousSeal = checksum
	return s.Save()
}

// SourcePaths maps archive destinations back to the absolute paths the stage
// tracks, which is what a caller needs to act on a status entry.
func (s *Stage) SourcePaths() map[string]string {
	out := map[string]string{}
	for _, src := range s.Plan.Sources {
		out[src.Dest] = src.Path
	}
	return out
}

// present reports whether a path is an existing directory. A source that has
// been moved or deleted is reported, never silently dropped: the stage
// remembers a thread, and a repository being temporarily absent is not a reason
// to forget it was part of one.
func present(path string) bool {
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// StagedHandoffEntry is the plan entry that carries the living narrative into
// an archive, so a seal always ships the record the stage maintained.
func (s *Stage) StagedHandoffEntry() (path, dest string, ok bool) {
	p := HandoffPath(s.Root)
	if _, err := os.Stat(p); err != nil {
		return "", "", false
	}
	return p, filepath.ToSlash(filepath.Join("context", HandoffFile)), true
}
