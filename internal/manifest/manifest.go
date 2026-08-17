// Package manifest defines MANIFEST.json, the description of an archive that
// travels inside it.
//
// The manifest deliberately mirrors the plan minus every local path. That is
// enforced by the type system rather than by a stripping step: these structs
// have no Path field, so an absolute path cannot be represented, let alone
// serialized. TestManifestHasNoAbsolutePaths guards the rule from the outside
// as well.
package manifest

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/satmihir/gobag/internal/plan"
)

// Name is the manifest's path inside the archive. It is written first so a
// streaming reader learns the layout before any payload arrives.
const Name = "MANIFEST.json"

// Manifest describes an archive's contents and provenance.
type Manifest struct {
	PlanVersion  int    `json:"plan_version"`
	GobagVersion string `json:"gobag_version"`
	Name         string `json:"name"`
	// Created is RFC3339 in UTC.
	Created string `json:"created"`
	// SourceRoot is the workspace root on the packing machine, recorded only
	// as a hint for orientation prose ("packed from ~/ws/proj"). Nothing in
	// install depends on it.
	SourceRoot string `json:"source_root,omitempty"`

	Sources     []Source `json:"sources,omitempty"`
	Context     []string `json:"context,omitempty"`
	Skills      []string `json:"skills,omitempty"`
	Memory      []string `json:"memory,omitempty"`
	Transcripts []string `json:"transcripts,omitempty"`
	MCP         string   `json:"mcp,omitempty"`
}

// Source is a repository reference: everything needed to reconstruct it, and
// nothing about where it lived.
type Source struct {
	Dest   string `json:"dest"`
	Remote string `json:"remote"`
	Ref    string `json:"ref"`
	Branch string `json:"branch,omitempty"`
	// External marks a repository install must locate rather than clone.
	External bool `json:"external,omitempty"`
	// SizeBytes is why: the object-store size measured at pack time.
	SizeBytes int64 `json:"size_bytes,omitempty"`
	// LocationHints are paths it occupied on the packing machine, offered to
	// whoever has to answer where it lives here.
	LocationHints []string   `json:"location_hints,omitempty"`
	Worktrees     []Worktree `json:"worktrees,omitempty"`
}

// Worktree is a linked worktree reference.
type Worktree struct {
	Dest   string `json:"dest"`
	Ref    string `json:"ref"`
	Branch string `json:"branch,omitempty"`
}

// FromPlan projects a plan into the manifest that will travel with the
// archive. withTranscripts must match what pack actually wrote, so the
// manifest never advertises contents the archive lacks.
func FromPlan(p *plan.Plan, gobagVersion, sourceRoot string, created time.Time, withTranscripts bool) *Manifest {
	m := &Manifest{
		PlanVersion:  p.PlanVersion,
		GobagVersion: gobagVersion,
		Name:         p.Name,
		Created:      created.UTC().Format(time.RFC3339),
		SourceRoot:   sourceRoot,
	}

	for _, s := range p.Sources {
		ms := Source{
			Dest: s.Dest, Remote: s.Remote, Ref: s.Ref, Branch: s.Branch,
			External: s.External, SizeBytes: s.SizeBytes, LocationHints: s.LocationHints,
		}
		for _, w := range s.Worktrees {
			ms.Worktrees = append(ms.Worktrees, Worktree{Dest: w.Dest, Ref: w.Ref, Branch: w.Branch})
		}
		m.Sources = append(m.Sources, ms)
	}
	m.Context = dests(p.Context)
	m.Skills = dests(p.Skills)
	m.Memory = dests(p.State.Memory)
	if withTranscripts {
		m.Transcripts = dests(p.State.Transcripts)
	}
	switch {
	case p.State.MCP != nil:
		m.MCP = p.State.MCP.Dest
	case p.MCP != nil:
		m.MCP = p.MCP.Dest
	}
	return m
}

func dests(entries []plan.Entry) []string {
	if len(entries) == 0 {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Dest)
	}
	return out
}

// Encode writes the manifest as indented JSON.
func (m *Manifest) Encode(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(m)
}

// Decode reads a manifest.
func Decode(r io.Reader) (*Manifest, error) {
	var m Manifest
	if err := json.NewDecoder(r).Decode(&m); err != nil {
		return nil, fmt.Errorf("decoding manifest: %w", err)
	}
	return &m, nil
}
