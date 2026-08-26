package reconcile

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/satmihir/gobag/internal/gitops"
	"github.com/satmihir/gobag/internal/host"
	"github.com/satmihir/gobag/internal/manifest"
	"github.com/satmihir/gobag/internal/overlay"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")

	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden file (run: go test ./internal/reconcile -update): %v", err)
	}
	if got != string(want) {
		t.Errorf("output does not match %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

func render(t *testing.T, in Input) string {
	t.Helper()
	var b strings.Builder
	if err := Render(&b, in); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return b.String()
}

// A restore where the world moved: the case the whole tool exists for.
func TestRenderWorldMoved(t *testing.T) {
	in := Input{
		Manifest: &manifest.Manifest{
			Name:       "teammate",
			Created:    "2026-08-01T14:32:00Z",
			SourceRoot: "/Users/someone/ws/proj",
		},
		Root:        "/home/dev/workspace",
		HandoffPath: "context/HANDOFF.md",
		Repos: []gitops.Result{
			{Dest: "repos/frontend", Outcome: gitops.OutcomeCloned, Ref: "3f9c2ab0000000000000000000000000000000ab"},
			{Dest: "repos/backend", Outcome: gitops.OutcomeCloned, Ref: "8a1b2c30000000000000000000000000000000cd"},
			{Dest: "repos/frontend-wip", Outcome: gitops.OutcomeCreated, Ref: "3f9c2ab0000000000000000000000000000000ab"},
		},
		Reality: []gitops.Reality{
			{Dest: "repos/frontend", PinnedRef: "3f9c2ab0000000000000000000000000000000ab", Ahead: 14},
			{Dest: "repos/backend", PinnedRef: "8a1b2c30000000000000000000000000000000cd", Ahead: 0},
		},
		Files: []overlay.Result{
			{Path: "context/HANDOFF.md", Outcome: overlay.OutcomeWritten},
			{Path: "state/memory/MEMORY.md", Outcome: overlay.OutcomeWritten},
		},
		MemoryRewritten: []string{"memory/deploy-runbook.md"},
	}
	golden(t, "world_moved", render(t, in))
}

// A restore onto a target that already holds work, with a remote down.
func TestRenderConflictsAndUnreachable(t *testing.T) {
	in := Input{
		Manifest:    &manifest.Manifest{Name: "teammate", Created: "2026-08-01T14:32:00Z"},
		Root:        "/home/dev/workspace",
		HandoffPath: "context/HANDOFF.md",
		Repos: []gitops.Result{
			{Dest: "repos/frontend", Outcome: gitops.OutcomeLeftDirty, Ref: "3f9c2ab", Detail: "uncommitted changes"},
			{Dest: "repos/backend", Outcome: gitops.OutcomeUnreachable},
		},
		Reality: []gitops.Reality{
			{Dest: "repos/backend", PinnedRef: "8a1b2c30000000000000000000000000000000cd", Unreachable: true},
		},
		Files: []overlay.Result{
			{Path: "context/HANDOFF.md", Outcome: overlay.OutcomeConflict, SidecarPath: "context/HANDOFF.md.from-gobag"},
			{Path: "context/notes.md", Outcome: overlay.OutcomeIdentical},
		},
		Notes: []string{"2 session transcripts were not packed"},
	}
	golden(t, "conflicts", render(t, in))
}

// The quiet case: nothing moved, nothing conflicted.
func TestRenderNothingMoved(t *testing.T) {
	in := Input{
		Manifest:    &manifest.Manifest{Name: "teammate", Created: "2026-08-01T14:32:00Z"},
		Root:        "/home/dev/workspace",
		HandoffPath: "context/HANDOFF.md",
		Repos: []gitops.Result{
			{Dest: "repos/frontend", Outcome: gitops.OutcomeAlreadyAtRef, Ref: "3f9c2ab0000000000000000000000000000000ab"},
		},
		Reality: []gitops.Reality{{Dest: "repos/frontend", PinnedRef: "3f9c2ab", Ahead: 0}},
		Files:   []overlay.Result{{Path: "context/HANDOFF.md", Outcome: overlay.OutcomeIdentical}},
	}
	golden(t, "nothing_moved", render(t, in))
}

// Output must be deterministic regardless of input ordering, or the goldens
// would flake and a re-run would churn the file.
func TestRenderIsDeterministic(t *testing.T) {
	forward := Input{
		Manifest: &manifest.Manifest{Name: "t", Created: "2026-08-01T14:32:00Z"},
		Root:     "/root",
		Repos: []gitops.Result{
			{Dest: "a", Outcome: gitops.OutcomeCloned},
			{Dest: "b", Outcome: gitops.OutcomeCloned},
			{Dest: "c", Outcome: gitops.OutcomeCloned},
		},
		Notes: []string{"alpha", "beta"},
	}
	reversed := forward
	reversed.Repos = []gitops.Result{
		{Dest: "c", Outcome: gitops.OutcomeCloned},
		{Dest: "b", Outcome: gitops.OutcomeCloned},
		{Dest: "a", Outcome: gitops.OutcomeCloned},
	}
	reversed.Notes = []string{"beta", "alpha"}

	if render(t, forward) != render(t, reversed) {
		t.Error("orientation depends on input ordering")
	}
}

// Orientation states facts; it must not be empty or misleading when the
// archive carried no handoff at all.
func TestRenderWithoutHandoff(t *testing.T) {
	out := render(t, Input{
		Manifest: &manifest.Manifest{Name: "bare", Created: "2026-08-01T14:32:00Z"},
		Root:     "/root",
	})
	if !strings.Contains(out, "No handoff document") {
		t.Errorf("missing handoff should be stated plainly:\n%s", out)
	}
	if !strings.Contains(out, "No repositories travelled") {
		t.Errorf("empty source list should be stated plainly:\n%s", out)
	}
}

// The issue's exact scenario: a bag packed in a dead devcontainer, restored
// onto a different machine whose uniform layout makes same-named clones look
// like the originals. Orientation must contradict that inference outright.
func TestRenderDifferentHost(t *testing.T) {
	in := Input{
		Manifest: &manifest.Manifest{
			Name:       "teammate",
			Created:    "2026-08-25T18:00:00Z",
			SourceRoot: "/home/repo",
			Host:       &host.Info{Name: "devcontainer-3f9c", ID: "aaaaaaaaaaaaaaaa", Container: true},
		},
		Root:        "/home/user/ws",
		CurrentHost: host.Info{Name: "workstation", ID: "bbbbbbbbbbbbbbbb"},
		HandoffPath: "context/HANDOFF.md",
		Repos: []gitops.Result{
			{Dest: "repos/api", Outcome: gitops.OutcomeCloned, Ref: "3f9c2ab0000000000000000000000000000000ab"},
		},
	}
	out := render(t, in)
	for _, want := range []string{"different machines", "unrelated to this archive", "/home/user/ws"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	golden(t, "different_host", out)
}

func TestRenderSameHost(t *testing.T) {
	id := &host.Info{Name: "workstation", ID: "cccccccccccccccc"}
	out := render(t, Input{
		Manifest:    &manifest.Manifest{Name: "t", Created: "2026-08-25T18:00:00Z", Host: id},
		Root:        "/ws",
		CurrentHost: *id,
	})
	if !strings.Contains(out, "same machine") {
		t.Errorf("same-host case must be stated as plainly as the different-host one:\n%s", out)
	}
}

// A missing identifier must read as unknown, never as "same".
func TestRenderUndeterminableHost(t *testing.T) {
	out := render(t, Input{
		Manifest:    &manifest.Manifest{Name: "t", Created: "2026-08-25T18:00:00Z", Host: &host.Info{Name: "box"}},
		Root:        "/ws",
		CurrentHost: host.Info{Name: "other", ID: "dddddddddddddddd"},
	})
	if !strings.Contains(out, "Cannot determine") {
		t.Errorf("an inconclusive comparison must say so:\n%s", out)
	}
	if strings.Contains(out, "same machine") {
		t.Error("unknown must never be reported as same")
	}
}

// A tip that has not moved while its base advanced 95 commits is the case that
// made orientation say "nothing moved" and mean the opposite.
func TestRenderBaseDrift(t *testing.T) {
	in := Input{
		Manifest:    &manifest.Manifest{Name: "t", Created: "2026-08-25T18:00:00Z"},
		Root:        "/ws",
		HandoffPath: "context/HANDOFF.md",
		Reality: []gitops.Reality{{
			Dest:         "repos/api",
			PinnedRef:    "3f9c2ab0000000000000000000000000000000ab",
			PinnedBranch: "user/my-feature",
			Ahead:        0,
			BaseBranch:   "main",
			BaseAhead:    1,
			BaseBehind:   95,
		}},
	}
	out := render(t, in)
	if strings.Contains(out, "Nothing moved") {
		t.Errorf("base drift must not be reported as nothing moved:\n%s", out)
	}
	for _, want := range []string{"95 commits", "user/my-feature", "main", "may already be false"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	golden(t, "base_drift", out)
}

func TestRenderAdvisories(t *testing.T) {
	in := Input{
		Manifest: &manifest.Manifest{
			Name:    "t",
			Created: "2026-08-25T18:00:00Z",
			ContextModified: map[string]string{
				"context/HANDOFF.md": "2026-08-22T00:00:00Z", // 3d 18h earlier
			},
			SoleCopies: []string{"context/design.md"},
		},
		Root:        "/ws",
		HandoffPath: "context/HANDOFF.md",
	}
	out := render(t, in)
	for _, want := range []string{"3 days 18h before", "only copy", "context/design.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	golden(t, "advisories", out)
}

// A handoff written just before the pack is not worth a line.
func TestFreshHandoffIsNotFlagged(t *testing.T) {
	out := render(t, Input{
		Manifest: &manifest.Manifest{
			Name:            "t",
			Created:         "2026-08-25T18:00:00Z",
			ContextModified: map[string]string{"context/HANDOFF.md": "2026-08-25T16:00:00Z"},
		},
		Root:        "/ws",
		HandoffPath: "context/HANDOFF.md",
	})
	if strings.Contains(out, "before this archive was packed") {
		t.Errorf("a two-hour-old handoff should not be flagged as stale:\n%s", out)
	}
}
