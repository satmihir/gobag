// Package stage maintains the thread's living record inside a workspace.
//
// A bag answers "the machine is dying". The stage answers the far more common
// way a thread is lost: the session ends, context is compacted away, and the
// files sit intact while the narrative goes cold. It is deliberately plain,
// unencrypted files inside the workspace — the threat model for archives is
// transit, not residence, and the stage sits beside the repositories and .env
// files it describes, all equally unencrypted. The encryption boundary is
// `gobag seal`.
//
// The governing rule, which every caller must honor: the stage is the source of
// truth about the thread, and a session is only ever its editor. Read before
// writing. The session doing the writing may have been compacted since it last
// wrote and may not remember writing at all.
package stage

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/satmihir/gobag/internal/plan"
)

// Layout inside the workspace root. The parent .gobag directory is shared with
// the manifest install leaves behind; the stage keeps its own subdirectory.
const (
	DirName     = ".gobag"
	StageName   = "stage"
	PlanFile    = "plan.json"
	HandoffFile = "HANDOFF.md"
	MetaFile    = "stage.json"
)

// StaleAfter is how old a handoff may be before the stage says so. Prose about
// pull requests, CI, and version pins goes wrong on roughly this timescale.
var StaleAfter = 72 * time.Hour

// Meta is stage.json: the bookkeeping a stage keeps about itself.
//
// Note what is absent: when the narrative was last revised. That is the
// handoff file's own modification time, so an agent that simply edits the file
// cannot forget to record that it did.
type Meta struct {
	// Series ties every seal from this stage into one thread.
	Series   string `json:"series"`
	Sequence int    `json:"sequence"`
	Created  string `json:"created"`
	// LastRefresh is when the mechanical facts were last re-resolved.
	LastRefresh string `json:"last_refresh,omitempty"`
	// LastCompaction is when a session's context was last compacted away. The
	// single most useful fact the stage holds: everything the narrative does
	// not already contain from before this moment is gone.
	LastCompaction string `json:"last_compaction,omitempty"`
	// LastNudge prevents the same condition from being reported on every
	// prompt. A nudge is a reminder, not a nag.
	LastNudge string `json:"last_nudge,omitempty"`
	// LastSeal is when this stage last became an archive.
	LastSeal string `json:"last_seal,omitempty"`
	// PreviousSeal is the checksum of that archive, giving seals a lineage.
	PreviousSeal string `json:"previous_seal,omitempty"`
}

// Stage is a loaded stage: its metadata and the plan it maintains.
type Stage struct {
	// Root is the absolute workspace root.
	Root string
	Meta Meta
	Plan *plan.Plan
}

// Dir returns the stage directory for a workspace root.
func Dir(root string) string { return filepath.Join(root, DirName, StageName) }

// Exists reports whether a workspace has a stage. Every hook checks this first
// and exits silently when it does not: enabling the plugin in a workspace that
// never staged anything must be a no-op.
func Exists(root string) bool {
	_, err := os.Stat(filepath.Join(Dir(root), MetaFile))
	return err == nil
}

// HandoffPath is the living narrative's location on disk.
func HandoffPath(root string) string { return filepath.Join(Dir(root), HandoffFile) }

// Init creates a stage for a workspace, minting a series id. It refuses to
// overwrite an existing stage: the stage is a record, and clobbering one would
// discard exactly the thread it exists to preserve.
func Init(root string, p *plan.Plan, now time.Time) (*Stage, error) {
	if Exists(root) {
		return nil, fmt.Errorf("a stage already exists at %s", Dir(root))
	}
	if p == nil {
		return nil, fmt.Errorf("a stage needs a plan")
	}
	if err := os.MkdirAll(Dir(root), 0o755); err != nil {
		return nil, fmt.Errorf("creating stage: %w", err)
	}

	s := &Stage{
		Root: root,
		Plan: p,
		Meta: Meta{Series: newSeries(), Sequence: 0, Created: stamp(now)},
	}
	if err := s.Save(); err != nil {
		return nil, err
	}
	// Seed a handoff so the next session has something to read and revise
	// rather than a blank page it will be tempted to fill from memory.
	if _, err := os.Stat(HandoffPath(root)); os.IsNotExist(err) {
		if err := writeAtomic(HandoffPath(root), []byte(handoffSeed(p.Name))); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Load reads a stage.
func Load(root string) (*Stage, error) {
	s := &Stage{Root: root}

	metaBytes, err := os.ReadFile(filepath.Join(Dir(root), MetaFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no stage in %s — run: gobag stage init", root)
		}
		return nil, fmt.Errorf("reading stage: %w", err)
	}
	if err := json.Unmarshal(metaBytes, &s.Meta); err != nil {
		return nil, fmt.Errorf("reading %s: %w", MetaFile, err)
	}

	p, err := plan.Load(filepath.Join(Dir(root), PlanFile))
	if err != nil {
		return nil, fmt.Errorf("reading staged plan: %w", err)
	}
	s.Plan = p
	return s, nil
}

// Save writes the stage atomically. A crash mid-write must never leave a
// half-written stage, because a corrupt record is worse than a stale one.
func (s *Stage) Save() error {
	if err := os.MkdirAll(Dir(s.Root), 0o755); err != nil {
		return fmt.Errorf("creating stage: %w", err)
	}

	metaBytes, err := json.MarshalIndent(s.Meta, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding stage metadata: %w", err)
	}
	if err := writeAtomic(filepath.Join(Dir(s.Root), MetaFile), append(metaBytes, '\n')); err != nil {
		return err
	}

	if s.Plan != nil {
		var buf []byte
		bw := &byteWriter{&buf}
		if err := s.Plan.Encode(bw); err != nil {
			return fmt.Errorf("encoding staged plan: %w", err)
		}
		if err := writeAtomic(filepath.Join(Dir(s.Root), PlanFile), buf); err != nil {
			return err
		}
	}
	return nil
}

// Handoff returns the living narrative and when it was last revised.
func (s *Stage) Handoff() (content string, modified time.Time, ok bool) {
	path := HandoffPath(s.Root)
	info, err := os.Stat(path)
	if err != nil {
		return "", time.Time{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", info.ModTime(), false
	}
	return string(b), info.ModTime(), true
}

// writeAtomic writes through a temporary file in the same directory, then
// renames — atomic on every filesystem gobag supports.
func writeAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// byteWriter adapts a byte slice to io.Writer for the plan encoder.
type byteWriter struct{ b *[]byte }

func (w *byteWriter) Write(p []byte) (int, error) {
	*w.b = append(*w.b, p...)
	return len(p), nil
}

func newSeries() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A series id only has to be distinct, never secret; a clock-derived
		// fallback is fine and keeps Init from failing over entropy.
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
	}
	return hex.EncodeToString(b[:])
}

// stamp records a time at nanosecond precision. Second granularity is not
// enough: the stage compares a recorded compaction against a file's
// modification time, and both can land inside the same second — which would
// silently read as "the narrative is current" at exactly the moment it is not.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// parseStamp reads a recorded time, reporting whether it was usable.
func parseStamp(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	return t, err == nil
}

func handoffSeed(name string) string {
	return fmt.Sprintf(`# Handoff — %s

> This is the thread's living record. It is maintained across sessions, and
> the session reading it may have been compacted since it last wrote. Read it
> first and revise it — never rewrite it from memory.

## Goal and status

_Not yet written._

## Open threads

_Not yet written._

## Decisions and why

_Not yet written._

## Where things are

_Not yet written._

## Gotchas

_Not yet written._

## Start with these

_Not yet written._
`, name)
}
