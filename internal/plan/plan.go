// Package plan defines the pack plan: the description of what to capture,
// produced either by the /checkpoint skill interrogating a live session or by
// walk-mode discovery.
//
// A plan is local input, never shipped: its Path fields are absolute paths on
// the packing machine. What travels is the manifest, which by construction
// cannot hold an absolute path. Plans arriving from a skill are untrusted —
// call Verify before acting on one.
package plan

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// Version is the plan schema version this build understands.
const Version = 1

// Plan is the complete description of a pack.
type Plan struct {
	PlanVersion int    `json:"plan_version"`
	Name        string `json:"name"`
	// SourceRoot is the workspace root on the packing machine. Optional, but
	// without it a cross-machine restore cannot rewrite workspace paths inside
	// memory files — the archive has no other record of where "here" was.
	SourceRoot string   `json:"source_root,omitempty"`
	Sources    []Source `json:"sources,omitempty"`
	Context    []Entry  `json:"context,omitempty"`
	State      State    `json:"state,omitempty"`
	Skills     []Entry  `json:"skills,omitempty"`
	MCP        *Entry   `json:"mcp,omitempty"`
}

// Source is a git repository travelling as a reference: a remote URL and a
// pinned ref, never content.
type Source struct {
	// Path is the absolute working-tree path on the packing machine.
	Path string `json:"path"`
	// Dest is the archive-relative location, e.g. "repos/frontend".
	Dest string `json:"dest"`
	// Remote is the URL a restore will clone from.
	Remote string `json:"remote"`
	// Ref is the pinned commit, preferably a full sha.
	Ref string `json:"ref"`
	// Branch, when set, is checked out on restore instead of a detached HEAD.
	Branch string `json:"branch,omitempty"`
	// External marks a repository too large to clone per restore. It is never
	// cloned on install; the target machine is expected to already have a copy,
	// which gobag locates and attaches a worktree to.
	External bool `json:"external,omitempty"`
	// SizeBytes is the measured object-store size at pack time. It is why a
	// repository was marked external, and it tells the far side what it is
	// being asked to find.
	SizeBytes int64 `json:"size_bytes,omitempty"`
	// LocationHints are paths where this repository lived on the packing
	// machine — a starting point for a human answering "where is it here?".
	LocationHints []string   `json:"location_hints,omitempty"`
	Worktrees     []Worktree `json:"worktrees,omitempty"`
}

// Worktree is a linked git worktree of its parent Source.
type Worktree struct {
	Path   string `json:"path"`
	Dest   string `json:"dest"`
	Ref    string `json:"ref"`
	Branch string `json:"branch,omitempty"`
}

// Entry is a file or directory copied into the archive verbatim. When Path
// names a directory, pack walks it and preserves the tree under Dest.
type Entry struct {
	Path string `json:"path"`
	Dest string `json:"dest"`
}

// State is raw Claude Code cargo. Transcripts are opt-in keepsakes and never
// load-bearing; continuity comes from the handoff document in Context.
type State struct {
	Memory      []Entry `json:"memory,omitempty"`
	Transcripts []Entry `json:"transcripts,omitempty"`
	MCP         *Entry  `json:"mcp,omitempty"`
}

// Load reads and decodes a plan, rejecting unknown fields so that a typo in a
// skill-authored plan surfaces immediately instead of being silently dropped.
func Load(path string) (*Plan, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening plan: %w", err)
	}
	defer f.Close()
	return Decode(f)
}

// Decode reads a plan from r.
func Decode(r io.Reader) (*Plan, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()

	var p Plan
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("decoding plan: %w", err)
	}
	if p.PlanVersion != Version {
		return nil, fmt.Errorf("plan_version %d is not supported (this gobag understands %d)",
			p.PlanVersion, Version)
	}
	if strings.TrimSpace(p.Name) == "" {
		return nil, fmt.Errorf("plan is missing a name")
	}
	return &p, nil
}

// Encode writes a plan as indented JSON.
func (p *Plan) Encode(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(p)
}

// Entries returns every non-repository entry in the plan, in archive order.
// Transcripts are included only when withTranscripts is set.
func (p *Plan) Entries(withTranscripts bool) []Entry {
	var out []Entry
	out = append(out, p.Context...)
	out = append(out, p.Skills...)
	out = append(out, p.State.Memory...)
	if withTranscripts {
		out = append(out, p.State.Transcripts...)
	}
	if p.State.MCP != nil {
		out = append(out, *p.State.MCP)
	}
	if p.MCP != nil {
		out = append(out, *p.MCP)
	}
	return out
}

// ValidateDest rejects destinations that are absolute, empty, or escape the
// archive root. Every Dest in a plan passes through here before anything is
// written, because a plan may be authored by an agent.
func ValidateDest(dest string) error {
	if dest == "" {
		return fmt.Errorf("destination is empty")
	}
	if strings.ContainsRune(dest, '\\') {
		return fmt.Errorf("destination %q must use forward slashes", dest)
	}
	if path.IsAbs(dest) {
		return fmt.Errorf("destination %q must be relative to the archive root", dest)
	}
	if vol := volumeName(dest); vol != "" {
		return fmt.Errorf("destination %q must not name a volume", dest)
	}
	clean := path.Clean(dest)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("destination %q escapes the archive root", dest)
	}
	if clean == "." {
		return fmt.Errorf("destination %q does not name anything", dest)
	}
	return nil
}

// volumeName reports a Windows-style volume prefix ("C:") if present. gobag is
// POSIX-only, but a plan is untrusted input and may claim anything.
func volumeName(p string) string {
	if len(p) >= 2 && p[1] == ':' {
		return p[:2]
	}
	return ""
}
