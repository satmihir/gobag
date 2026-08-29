package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/satmihir/gobag/internal/stage"
	"github.com/satmihir/gobag/internal/testutil"
)

// stagedWorkspace builds a fixture and starts maintaining a record in it.
func stagedWorkspace(t *testing.T) *testutil.Workspace {
	t.Helper()
	ws := testutil.NewWorkspace(t)
	if code, out := cli(t, "stage", "init", ws.Root); code != 0 {
		t.Fatalf("stage init exited %d:\n%s", code, out)
	}
	return ws
}

func TestStageInit(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to git")
	}
	ws := stagedWorkspace(t)

	for _, name := range []string{stage.MetaFile, stage.PlanFile, stage.HandoffFile} {
		if _, err := os.Stat(filepath.Join(stage.Dir(ws.Root), name)); err != nil {
			t.Errorf("stage is missing %s: %v", name, err)
		}
	}
	// A seeded handoff exists so the next session revises rather than
	// inventing from memory.
	if h := readFile(t, stage.HandoffPath(ws.Root)); !strings.Contains(h, "Read it") {
		t.Errorf("seeded handoff should tell the reader to read before writing:\n%s", h)
	}
	// Re-initialising must never clobber a record.
	if code, out := cli(t, "stage", "init", ws.Root); code != 1 {
		t.Errorf("re-init should refuse, got exit %d:\n%s", code, out)
	}
}

// Refresh is mechanical: it updates refs and must never touch the narrative.
func TestStageRefreshIsIdempotentAndLeavesNarrativeAlone(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to git")
	}
	ws := stagedWorkspace(t)

	const mine = "# Handoff\n\nmy own words\n"
	writeFile(t, stage.HandoffPath(ws.Root), mine)

	if code, out := cli(t, "stage", "refresh", ws.Root); code != 0 {
		t.Fatalf("refresh exited %d:\n%s", code, out)
	}
	first := readFile(t, filepath.Join(stage.Dir(ws.Root), stage.PlanFile))

	if code, out := cli(t, "stage", "refresh", ws.Root); code != 0 {
		t.Fatalf("second refresh exited %d:\n%s", code, out)
	}
	if second := readFile(t, filepath.Join(stage.Dir(ws.Root), stage.PlanFile)); second != first {
		t.Error("a second refresh changed the staged plan with nothing to change")
	}
	if got := readFile(t, stage.HandoffPath(ws.Root)); got != mine {
		t.Errorf("refresh rewrote the narrative:\n%s", got)
	}
}

// Work that happens after staging must show up as drift.
func TestStageStatusSeesWorkSinceStaging(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to git")
	}
	ws := stagedWorkspace(t)

	code, out := cli(t, "stage", "status", ws.Root)
	if code != 0 {
		t.Fatalf("status exited %d:\n%s", code, out)
	}
	if strings.Contains(out, "moved since staged") {
		t.Errorf("a freshly staged workspace should not report drift:\n%s", out)
	}

	ws.CommitUnpushed("backend") // work happens
	code, out = cli(t, "stage", "status", ws.Root)
	if code != 0 {
		t.Fatalf("status exited %d:\n%s", code, out)
	}
	if !strings.Contains(out, "moved since staged") {
		t.Errorf("status did not notice work done since staging:\n%s", out)
	}

	// And a refresh brings the record back in line.
	if code, out := cli(t, "stage", "refresh", ws.Root); code != 0 {
		t.Fatalf("refresh exited %d:\n%s", code, out)
	}
	if _, out := cli(t, "stage", "status", ws.Root); strings.Contains(out, "moved since staged") {
		t.Errorf("refresh did not bring the record up to date:\n%s", out)
	}
}

// The nudge sits on the prompt path, so silence is the default and the bar for
// speaking is a real loss of context.
func TestStageNudge(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to git")
	}
	ws := stagedWorkspace(t)

	if code, out := cli(t, "stage", "nudge", ws.Root); code != 0 || out != "" {
		t.Errorf("a fresh stage must nudge nothing, got exit %d: %q", code, out)
	}

	// A compaction after the last narrative revision is the one condition
	// worth interrupting for.
	if code, out := cli(t, "stage", "refresh", "-compaction", ws.Root); code != 0 {
		t.Fatalf("refresh exited %d:\n%s", code, out)
	}

	code, out := cli(t, "stage", "nudge", ws.Root)
	if code != 0 {
		t.Fatalf("nudge exited %d:\n%s", code, out)
	}
	var payload struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("nudge output is not the JSON a hook expects: %v\n%s", err, out)
	}
	if payload.HookSpecificOutput.HookEventName != "UserPromptSubmit" {
		t.Errorf("wrong hook event name: %q", payload.HookSpecificOutput.HookEventName)
	}
	for _, want := range []string{"compacted", stage.HandoffFile, "Read it first"} {
		if !strings.Contains(payload.HookSpecificOutput.AdditionalContext, want) {
			t.Errorf("nudge message is missing %q:\n%s", want, payload.HookSpecificOutput.AdditionalContext)
		}
	}

	// A reminder repeated on every prompt is noise, so the same compaction
	// must not be reported twice.
	if _, out := cli(t, "stage", "nudge", ws.Root); out != "" {
		t.Errorf("the same compaction was nudged twice: %q", out)
	}

	// Revising the narrative clears the condition entirely.
	writeFile(t, stage.HandoffPath(ws.Root), "# Handoff\n\nrevised after the compaction\n")
	if _, out := cli(t, "stage", "nudge", ws.Root); out != "" {
		t.Errorf("nudged despite a revision newer than the compaction: %q", out)
	}
}

// Hooks run in every workspace, most of which have no stage and may not even
// have gobag installed. Silence and success is the only acceptable behavior.
func TestStageCommandsAreSilentWithoutAStage(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{
		{"stage", "refresh", root},
		{"stage", "refresh", "-compaction", root},
		{"stage", "nudge", root},
	} {
		if code, out := cli(t, args...); code != 0 || out != "" {
			t.Errorf("%v in an unstaged workspace: exit %d, output %q", args, code, out)
		}
	}
	// status is user-facing, so it may speak — but it must not fail.
	if code, _ := cli(t, "stage", "status", root); code != 0 {
		t.Errorf("status in an unstaged workspace exited %d", code)
	}
}

// M8's definition of done: the thread survives a session losing its memory.
//
// Every command below runs in a process that knows nothing about the ones
// before it, which is exactly the position a post-compaction session is in.
// What carries the thread across that gap is the stage, and nothing else.
func TestThreadSurvivesCompaction(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to git")
	}
	ws := stagedWorkspace(t)

	// Session one writes down something only it could know.
	const secret = "the retry race is in Handler(), not the middleware"
	writeFile(t, stage.HandoffPath(ws.Root),
		"# Handoff\n\n## Open threads\n\n"+secret+"\n")

	// Work happens, then the session's context is compacted away.
	ws.CommitUnpushed("backend")
	if code, out := cli(t, "stage", "refresh", "-compaction", ws.Root); code != 0 {
		t.Fatalf("refresh exited %d:\n%s", code, out)
	}

	// Session two: no memory of any of the above. It is told to look.
	code, nudge := cli(t, "stage", "nudge", ws.Root)
	if code != 0 || nudge == "" {
		t.Fatalf("the successor session was not told the record had fallen behind (exit %d): %q", code, nudge)
	}

	// Reading the record recovers what session one knew.
	recovered := readFile(t, stage.HandoffPath(ws.Root))
	if !strings.Contains(recovered, secret) {
		t.Fatalf("the thread did not survive the compaction:\n%s", recovered)
	}

	// It revises rather than rewriting: the earlier finding is preserved.
	writeFile(t, stage.HandoffPath(ws.Root),
		recovered+"\n## Decisions and why\n\nFixed with a mutex, not a channel.\n")

	// And the whole thread seals and restores intact.
	archivePath := filepath.Join(t.TempDir(), "thread.gobag")
	if code, out := cli(t, "seal", "-o", archivePath, "-plaintext",
		"-label", "after the race fix", ws.Root); code != 0 {
		t.Fatalf("seal exited %d:\n%s", code, out)
	}

	target := filepath.Join(t.TempDir(), "restored")
	if code, out := cli(t, "install", archivePath, "-root", target); code != 0 {
		t.Fatalf("install exited %d:\n%s", code, out)
	}
	handoff := readFile(t, filepath.Join(target, "context", stage.HandoffFile))
	if !strings.Contains(handoff, secret) {
		t.Errorf("the sealed handoff lost session one's finding:\n%s", handoff)
	}
	if !strings.Contains(handoff, "mutex") {
		t.Errorf("the sealed handoff lost session two's revision:\n%s", handoff)
	}
}

func TestSealRequiresAStage(t *testing.T) {
	root := t.TempDir()
	code, out := cli(t, "seal", "-plaintext", root)
	if code != 1 {
		t.Fatalf("seal without a stage should exit 1, got %d:\n%s", code, out)
	}
	if !strings.Contains(out, "gobag pack") {
		t.Errorf("the error should point at the one-shot alternative:\n%s", out)
	}
}

// A seal is an ordinary archive: lineage rides along, and restore is unchanged.
func TestSealRecordsLineage(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to git")
	}
	ws := stagedWorkspace(t)

	first := filepath.Join(t.TempDir(), "one.gobag")
	if code, out := cli(t, "seal", "-o", first, "-plaintext", "-label", "first", ws.Root); code != 0 {
		t.Fatalf("first seal exited %d:\n%s", code, out)
	}
	second := filepath.Join(t.TempDir(), "two.gobag")
	if code, out := cli(t, "seal", "-o", second, "-plaintext", "-label", "second", ws.Root); code != 0 {
		t.Fatalf("second seal exited %d:\n%s", code, out)
	}

	s, err := stage.Load(ws.Root)
	if err != nil {
		t.Fatal(err)
	}
	if s.Meta.Sequence != 2 {
		t.Errorf("sequence = %d, want 2", s.Meta.Sequence)
	}
	if s.Meta.Series == "" {
		t.Error("series id was not recorded")
	}

	code, out := cli(t, "inspect", second)
	if code != 0 {
		t.Fatalf("inspect exited %d:\n%s", code, out)
	}
	if !strings.Contains(out, "repos/frontend") {
		t.Errorf("a seal should be an ordinary archive:\n%s", out)
	}
}

// StaleAfter drives the secondary nudge; verify the boundary rather than
// waiting three days for it.
func TestStaleNarrativeNudge(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to git")
	}
	ws := stagedWorkspace(t)

	old := time.Now().Add(-5 * 24 * time.Hour)
	if err := os.Chtimes(stage.HandoffPath(ws.Root), old, old); err != nil {
		t.Fatal(err)
	}
	code, out := cli(t, "stage", "nudge", ws.Root)
	if code != 0 || out == "" {
		t.Fatalf("a five-day-old narrative should be nudged (exit %d): %q", code, out)
	}
	if !strings.Contains(out, "revised") {
		t.Errorf("stale nudge should say how old the record is:\n%s", out)
	}
}
