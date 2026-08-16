package manifest

import (
	"strings"
	"testing"
	"time"

	"github.com/satmihir/gobag/internal/plan"
)

func fullPlan() *plan.Plan {
	return &plan.Plan{
		PlanVersion: plan.Version,
		Name:        "teammate",
		Sources: []plan.Source{{
			Path:   "/Users/someone/ws/proj/repos/frontend",
			Dest:   "repos/frontend",
			Remote: "git@github.com:org/frontend.git",
			Ref:    "3f9c2ab0000000000000000000000000000000ab",
			Branch: "main",
			Worktrees: []plan.Worktree{{
				Path:   "/Users/someone/ws/proj/repos/frontend-wip",
				Dest:   "repos/frontend-wip",
				Ref:    "8a1b2c30000000000000000000000000000000cd",
				Branch: "wip",
			}},
		}},
		Context: []plan.Entry{{Path: "/Users/someone/ws/proj/HANDOFF.md", Dest: "context/HANDOFF.md"}},
		Skills:  []plan.Entry{{Path: "/Users/someone/.claude/skills/deploy", Dest: "skills/deploy"}},
		State: plan.State{
			Memory:      []plan.Entry{{Path: "/Users/someone/.claude/projects/x", Dest: "state/memory"}},
			Transcripts: []plan.Entry{{Path: "/Users/someone/.claude/projects/x/s.jsonl", Dest: "state/sessions/s.jsonl"}},
			MCP:         &plan.Entry{Path: "/Users/someone/ws/proj/.mcp.json", Dest: "state/mcp.json"},
		},
	}
}

// The manifest travels inside the archive, so an absolute path in it would leak
// the packing machine's layout and break the relative-only contract. The type
// system prevents representing one; this guards the serialized form too.
func TestManifestHasNoAbsolutePaths(t *testing.T) {
	m := FromPlan(fullPlan(), "gobag v0.1.0", "", time.Unix(0, 0), true)

	var b strings.Builder
	if err := m.Encode(&b); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out := b.String()

	for _, leak := range []string{
		"/Users/someone",
		"/Users/someone/ws/proj",
		"/Users/someone/.claude",
	} {
		if strings.Contains(out, leak) {
			t.Errorf("manifest leaked an absolute path %q:\n%s", leak, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, `": "/`) {
			t.Errorf("manifest field holds an absolute path: %s", strings.TrimSpace(line))
		}
	}
}

// SourceRoot is the one place an absolute path is allowed, because orientation
// prose says "packed from X". It must be opt-in, never implied.
func TestSourceRootIsTheOnlyAbsolutePath(t *testing.T) {
	m := FromPlan(fullPlan(), "gobag v0.1.0", "/Users/someone/ws/proj", time.Unix(0, 0), true)
	if m.SourceRoot != "/Users/someone/ws/proj" {
		t.Errorf("SourceRoot = %q", m.SourceRoot)
	}

	empty := FromPlan(fullPlan(), "gobag v0.1.0", "", time.Unix(0, 0), true)
	var b strings.Builder
	if err := empty.Encode(&b); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "source_root") {
		t.Error("empty source_root should be omitted entirely")
	}
}

func TestFromPlanProjectsEveryField(t *testing.T) {
	m := FromPlan(fullPlan(), "gobag v0.1.0", "", time.Unix(0, 0).UTC(), true)

	if got := len(m.Sources); got != 1 {
		t.Fatalf("got %d sources, want 1", got)
	}
	s := m.Sources[0]
	if s.Dest != "repos/frontend" || s.Remote != "git@github.com:org/frontend.git" || s.Branch != "main" {
		t.Errorf("source not projected: %+v", s)
	}
	if len(s.Worktrees) != 1 || s.Worktrees[0].Dest != "repos/frontend-wip" {
		t.Errorf("worktree not projected: %+v", s.Worktrees)
	}
	for _, tc := range []struct {
		name string
		got  []string
		want string
	}{
		{"context", m.Context, "context/HANDOFF.md"},
		{"skills", m.Skills, "skills/deploy"},
		{"memory", m.Memory, "state/memory"},
		{"transcripts", m.Transcripts, "state/sessions/s.jsonl"},
	} {
		if len(tc.got) != 1 || tc.got[0] != tc.want {
			t.Errorf("%s = %v, want [%s]", tc.name, tc.got, tc.want)
		}
	}
	if m.MCP != "state/mcp.json" {
		t.Errorf("mcp = %q", m.MCP)
	}
	if m.Created != "1970-01-01T00:00:00Z" {
		t.Errorf("created = %q, want RFC3339 UTC", m.Created)
	}
}

// A manifest must never advertise contents the archive does not hold.
func TestTranscriptsOmittedWhenNotPacked(t *testing.T) {
	m := FromPlan(fullPlan(), "gobag v0.1.0", "", time.Unix(0, 0), false)
	if len(m.Transcripts) != 0 {
		t.Errorf("transcripts listed despite not being packed: %v", m.Transcripts)
	}
}

func TestRoundTrip(t *testing.T) {
	original := FromPlan(fullPlan(), "gobag v0.1.0", "/ws/proj", time.Unix(0, 0), true)

	var b strings.Builder
	if err := original.Encode(&b); err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(strings.NewReader(b.String()))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if decoded.Name != original.Name || len(decoded.Sources) != len(original.Sources) {
		t.Errorf("round trip lost data: %+v", decoded)
	}
}
