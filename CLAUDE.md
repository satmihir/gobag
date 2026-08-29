# gobag

Static Go binary + Claude Code skill pair. The skill (`/checkpoint`) has a
running session distill its own working state — handoff doc, source list,
expectations — into a plan; the binary verifies the plan and packs it into
a single encrypted `.gobag` archive; on the other side, `gobag install`
restores mechanically and `/restore` seeds the new session oriented. Read
`DESIGN.md` before proposing anything structural. `PLAN.md` is the v1
build order — follow it milestone by milestone.

## Hard constraints — do not drift

- **Tool is mechanism, skill is policy.** The binary must be complete and
  useful with no session present (walk mode). Never move verification,
  crypto, or git operations into the skill; never move
  curation/judgment into the binary.
- **Agent output is untrusted input.** Every claim in a plan gets
  verified (remote reachable, ref pushed, file exists) before sealing.
  The secret scan runs on agent-authored docs too — model sanitization
  is a courtesy, not a security boundary.
- **Dependency budget: 0–3.** Currently allowed: `filippo.io/age`, one
  zstd implementation. Everything else is stdlib. No config framework,
  logging framework, or CLI framework without explicit approval. No new
  dependency without asking first.
- **No daemon, no background process, no state outside the archive and
  the workspace root.** gobag runs, exits, leaves nothing behind.
- **Encrypt by default.** Plaintext output only behind an explicit
  `--plaintext` flag. Secrets (handoffs, state) must never be written
  unencrypted to disk outside the user's workspace — no temp-file
  plaintext spills; compose `tar → zstd → age` as chained streaming
  writers.
- **Every install step is idempotent.** `install` into a dirty or partial
  target must converge, never duplicate, never destroy user work. Prefer
  fetch + fast-forward over re-clone.
- **No absolute paths in the manifest or archive.** Everything is
  relative to the install root chosen at restore time. If you find
  yourself writing an absolute path into an artifact, stop.
- **Repos travel as references (remote + pinned ref), never as content.**
  A repository too large to clone per restore travels as a *located*
  reference: never cloned by install, attached as a linked worktree of a
  clone the target machine already has. Never move or check out a branch
  in someone's existing clone — external links are always detached.
- **The per-machine registry (`~/.gobag/machine.json`) is the only state
  outside a workspace,** and it is written only by an explicit
  `gobag link`. Do not read or write it from `pack`, and do not extend it
  into a general-purpose config file.
- **Transcripts are opt-in keepsakes, never load-bearing.** Continuity
  comes from `context/HANDOFF.md`. Do not implement JSONL path
  rewriting; do not make any restore behavior depend on transcript
  contents.
- **The stage is the source of truth about the thread; a session is only
  its editor.** Anything that touches `.gobag/stage/` reads it first and
  revises — never rewrites from session memory, because compaction may
  have intervened since the last write. Hooks write outward to the stage;
  they never inject stage or bag contents into a session.
- **Stage plaintext stays inside the workspace.** `.gobag/stage/` is
  deliberately unencrypted (the threat model is transit, not residence);
  the encryption boundary is `gobag seal`, and nothing crosses the
  machine boundary unencrypted. Do not add encryption to the stage, and
  do not let staged content be written outside the workspace root.
- **The one-shot path survives.** `pack`/`install`/`/checkpoint` with no
  stage present must keep working exactly as today — stage and seal are
  additive, never prerequisites. Do not make `pack` depend on a stage.
- **Restore is explicit.** `/restore <archive>` is the only way a packed
  session's context enters a new session — it starts a new teammate
  briefed by the old one's notes. No hook injects orientation silently
  (an opt-in SessionStart hook is roadmap, not v1).
- **Claude Code only.** Do not add abstraction layers for hypothetical
  other agents. v1 has no cross-agent anything.
- **v1 does not capture uncommitted changes.** Do not implement
  patch/bundle capture without being asked.

## Stage semantics (quick reference)

- `.gobag/stage/` holds `plan.json`, `HANDOFF.md`, series metadata.
- `gobag stage refresh` is mechanical only (refs, branches, dirty flags)
  and must stay headless-safe: no prompts, no network hangs beyond the
  usual timeouts, exit 0 on a merely stale stage.
- `gobag stage status` reports staleness (handoff age, refs moved, base
  drift) reusing the reconcile/reality machinery — do not fork a second
  staleness implementation.
- `gobag seal` = stage → verify → scan → encrypt. It is the only place
  the passphrase ceremony happens in the stage flow.
- Plugin hooks: PreCompact records the compaction (mechanical only — it
  cannot elicit narrative, and there is no model turn before compaction);
  UserPromptSubmit runs `stage nudge`, which is silent unless a
  compaction post-dates the last narrative revision; SessionEnd runs a
  mechanical refresh. Hooks never bootstrap the binary and never fail a
  session: no stage or no binary means exit 0 and no output.
- `stamp()` in internal/stage uses RFC3339**Nano**. Second granularity
  silently reads as "the narrative is current" when a compaction lands
  in the same second as a handoff edit.

## Style

- Table-driven tests; fixture workspace under `testdata/`.
- Errors are wrapped with context (`fmt.Errorf("packing state: %w", err)`),
  surfaced to the user in plain language.
- User-facing output is terse. No spinners, no emoji, no ASCII art.
- Exit codes: 0 ok, 1 user/environment error, 2 internal error.

## Layout

- `cmd/gobag/`          — main, flag parsing
- `internal/plan/`      — plan.json schema, claim verification
- `internal/manifest/`  — MANIFEST.json schema + (de)serialization
- `internal/archive/`   — tar/zstd/age streaming pipeline
- `internal/gitops/`    — clone/fetch/worktree recreation, walk-mode
                          discovery
- `internal/reconcile/` — manifest-vs-reality diff + ORIENTATION.md
                          generation
- `internal/scan/`      — pack-time secret scanning (warn, don't block)
- `internal/overlay/`   — converge-never-destroy file writes; where the
                          `.from-gobag` conflict rule lives
- `internal/claudestate/` — Claude Code's encoded-cwd scheme and the
                          memory re-key
- `internal/stage/`     — the thread's living record (.gobag/stage/),
                          staleness, and the nudge decision
- `internal/host/`      — machine identity recorded at pack time
- `internal/machine/`   — the per-machine external-repo registry
- `internal/testutil/`  — fixture workspaces built at run time (never
                          check in a nested `.git`)
- `.claude-plugin/`     — plugin.json + marketplace.json (the repo is
                          its own marketplace)
- `skills/`             — the plugin skills: `checkpoint/SKILL.md`,
                          `restore/SKILL.md`
- `hooks/hooks.json`    — PreCompact / UserPromptSubmit / SessionEnd,
                          all routed through `scripts/gobag-hook.sh`
- `scripts/`            — `gobag-hook.sh` (hook entry point; silent and
                          exit 0 always), `gobag-bootstrap.sh` (pinned
                          first-use install,
                          called by both skills), `install.sh` (headless
                          curl|sh), `release-pin.sh` (rewrites the pins;
                          never edit them by hand)

## Review flags

Encryption code, plan verification, and reconciliation logic get human
review before merge — say so explicitly when a change touches
`internal/archive` crypto paths, `internal/plan`, or
`internal/reconcile`.
