# gobag — v1 Implementation Plan

Executable build order for v1. Companion to `DESIGN.md` (the why) and
`CLAUDE.md` (the rules). This plan is self-contained: execute it
milestone by milestone without re-deriving design decisions. If this
plan conflicts with `DESIGN.md`, `DESIGN.md` wins — flag the conflict
instead of silently picking one.

## Locked decisions — do not re-litigate

- Go, stdlib-first. Direct dependencies (transitive deps of these do
  not count against the budget):
  1. `filippo.io/age` — encryption
  2. `github.com/klauspost/compress/zstd` — compression
  3. `golang.org/x/term` — no-echo passphrase prompt
  That is 3/3 of the budget. **No cobra.** CLI is stdlib `flag` plus
  manual subcommand dispatch.
- Archive pipeline is `tar → zstd → age` as chained streaming writers.
  Plaintext never touches a temp file.
- Encrypt by default; `--plaintext` opts out. On read, encryption is
  detected by sniffing the age header (`age-encryption.org/v1`).
- Transcripts are **opt-in** (`--transcripts`) and never load-bearing.
- Repos travel as references (remote URL + pinned ref). No content, no
  bundles, no patches.
- Binary is mechanism, skill is policy. The binary is complete without
  any session (walk mode). Agent output (plan.json, handoff docs) is
  untrusted input: verify every claim, secret-scan everything.
- Git operations shell out to system `git` via `os/exec`. No go-git.
- Exit codes: 0 ok, 1 user/environment error, 2 internal error.
- Escalate to the human (do not decide alone): any new dependency, any
  change to crypto or reconciliation semantics, any conflict-handling
  behavior not specified below.

## Milestone graph

```
M0 scaffolding
├── M1 archive pipeline      (independent)
├── M2 plan/manifest schemas (independent)
└── M3 gitops                (independent)
M4 pack        needs M1+M2+M3
M5 install     needs M1+M2+M3
M6 plugin      needs M4+M5 behavior finalized
M7 release     last
```

---

## M0 — Scaffolding

- `go.mod` → `github.com/satmihir/gobag`.
- `cmd/gobag/main.go`: subcommand dispatch for `pack`, `install`,
  `inspect`, `verify`; `-h`/no-args prints terse usage; unknown verb
  exits 1.
- Package stubs per the CLAUDE.md layout: `internal/plan`,
  `internal/manifest`, `internal/archive`, `internal/gitops`,
  `internal/reconcile`, `internal/scan`.
- `internal/testutil`: helper that builds a fixture workspace in
  `t.TempDir()` programmatically — never check in `.git` directories.
  Fixture contents: two small git repos whose "remotes" are local bare
  repos (`file://` URLs), one linked worktree, a fake Claude state dir
  (`projects/<encoded-cwd>/memory/*.md` + `MEMORY.md`), a
  `context.md`, one file containing a fake AWS key
  (`AKIAIOSFODNN7EXAMPLE`) for scan tests.
- CI (GitHub Actions): `go vet`, `gofmt -l` clean, `go test ./...` on
  ubuntu + macos.

**Done when:** CI is green; `gobag -h` lists the four verbs.

---

## M1 — Archive pipeline (`internal/archive`)

- **Writer**: `NewWriter(dst io.Writer, passphrase string)` (empty
  passphrase = plaintext). Composes `tar.Writer` → zstd → age over the
  destination, in that wrapping order. `AddFile(relPath string, mode
  os.FileMode, r io.Reader)` streams content and accumulates a sha256
  per entry. `Close()` writes a final `CHECKSUMS` tar entry (format:
  `<sha256-hex>  <relPath>\n`, sorted by path), then closes the chain
  innermost-out.
- **Reader**: sniff the first bytes of the stream for the age header;
  if encrypted, a passphrase is required (error in plain language if
  missing/wrong). Expose a tar-entry iterator. age streams are not
  seekable — all reads are full streaming passes; do not build seek
  hacks.
- `gobag verify <archive>`: streaming pass, recompute each entry's
  sha256, compare against `CHECKSUMS`. Report per-file mismatches;
  exit 1 on any.
- `gobag inspect <archive>`: list entries (path, size); pretty-print
  `MANIFEST.json` when present.
- Tests: round-trip plaintext and encrypted; single-bit corruption is
  caught by verify; wrong passphrase produces a clear error, not a Go
  stack trace.

**Done when:** an archive built by the test harness round-trips,
verifies, and inspects in both modes.
**Review flag:** crypto paths — request human review per CLAUDE.md.

---

## M2 — Plan & manifest schemas (`internal/plan`, `internal/manifest`)

`plan.json` — pack's input. Produced by the `/checkpoint` skill, or
generated internally by walk mode. Local, never shipped verbatim;
`path` fields may be absolute. All `dest` fields are relative,
forward-slash, and must not escape the archive root (reject `..` and
absolute — tested).

```json
{
  "plan_version": 1,
  "name": "teammate",
  "sources": [
    {
      "path": "/home/u/work/frontend",
      "dest": "repos/frontend",
      "remote": "git@github.com:org/frontend.git",
      "ref": "3f9c2ab…(full sha)",
      "branch": "main",
      "worktrees": [
        { "path": "…", "dest": "repos/frontend-wip",
          "ref": "…", "branch": "wip" }
      ]
    }
  ],
  "context":  [ { "path": "…", "dest": "context/HANDOFF.md" } ],
  "state": {
    "memory":      [ { "path": "…", "dest": "state/memory/<encoded>" } ],
    "transcripts": [ { "path": "…", "dest": "state/sessions/<f>.jsonl" } ]
  },
  "skills": [ { "path": "…", "dest": "skills/<name>" } ],
  "mcp":    { "path": "…", "dest": "state/mcp.json" }
}
```

`MANIFEST.json` — shipped inside the archive. The plan minus every
local `path`, plus `gobag_version`, `created` (RFC3339), and entry
counts. **The manifest contains no absolute paths — hard rule, add a
test that greps the serialized output.**

`plan.Verify(p)` — every claim checked before sealing:
- **Errors** (exit 1): local path missing; not a git repo; `dest`
  escapes root; remote unreachable (`git ls-remote`, with timeout);
  ref does not resolve locally.
- **Warnings** (print loudly, continue): dirty worktree
  (`status --porcelain` non-empty); ref not reachable from any remote
  branch (`git branch -r --contains <ref>` empty → "unpushed —
  restore will fail until you push"); context file suspiciously large.

Tests: table-driven over fixture plans — valid, missing file, dest
escape, unreachable remote, unpushed ref.

**Done when:** schemas round-trip; Verify distinguishes every
error/warning case above.

---

## M3 — Git discovery & operations (`internal/gitops`)

**Discovery (walk mode):** `Discover(root) (plan, warnings)`. Walk the
tree; a `.git` directory = repo, a `.git` file = worktree (parse the
`gitdir:` pointer to bind it to its parent repo, wherever that parent
is). For each repo: remote (`origin` preferred, else first), HEAD sha
+ branch, dirty/unpushed flags. Emit the same plan struct as M2, with
`dest` mirroring layout relative to root.

**Operations (install side):**
- `EnsureRepo(dest, remote, ref, branch)`: missing → `git clone` +
  checkout `ref` (detached if `branch` absent). Present → verify
  remote URL matches (mismatch = warn, don't touch), `git fetch`, then
  fast-forward only if the checkout is clean and ff-possible;
  otherwise leave as-is and report. **Never `reset --hard`, never
  delete, never force.** Every outcome returned as a typed result
  (`cloned`, `fast-forwarded`, `already-at-ref`, `left-diverged`,
  `remote-mismatch`, `unreachable`) — M5's orientation consumes these.
- `EnsureWorktree(repoDest, wtDest, ref, branch)`: same idempotent
  contract via `git worktree add`.

Tests: discovery over the fixture workspace yields the expected plan;
each Ensure function run twice — second run must be all no-ops; dirty
and diverged targets are left intact with the right typed result.

**Done when:** discovery and both Ensure functions converge on empty,
partial, up-to-date, and dirty targets.

---

## M4 — `pack` (+ `internal/scan`)

`gobag pack --plan plan.json [-o name.gobag] [--plaintext]
[--transcripts]` and `gobag pack <root> [same flags]` (walk mode:
`Discover` generates the plan).

Flow, in order:
1. Load or generate the plan; `plan.Verify`.
2. Secret scan (below) over every file about to be packed — including
   agent-authored context docs; model sanitization is a courtesy, not
   a boundary.
3. Print the pack summary ("the photograph"): sources with refs,
   worktrees, state entries, total warnings. Terse, no colors
   required.
4. Skip `state.transcripts` entries unless `--transcripts` (note the
   skip in output).
5. Passphrase prompt via `x/term` (entered twice, must match) unless
   `--plaintext`.
6. Stream the archive (M1): MANIFEST.json first entry, then context/,
   skills/, state/, CHECKSUMS last.
7. Print the **boarding pass** (exact text lives in one `const`,
   golden-tested):

```
Packed: /path/to/teammate-20260816-1432.gobag

To restore — in Claude Code on the target machine:
  /plugin marketplace add satmihir/gobag
  /plugin install gobag@gobag
  /restore /path/to/teammate-20260816-1432.gobag

Headless alternative:
  curl -fsSL https://raw.githubusercontent.com/satmihir/gobag/main/scripts/install.sh | sh
  gobag install /path/to/teammate-20260816-1432.gobag
```

`internal/scan`: pure-regex pass, warn-only, report `file:line` +
pattern name. Patterns: AWS access key (`AKIA[0-9A-Z]{16}`), private
key blocks (`-----BEGIN [A-Z ]*PRIVATE KEY`), GitHub tokens
(`ghp_[A-Za-z0-9]{36}`, `github_pat_`), Slack tokens (`xox[abps]-`),
JWTs (`eyJ[A-Za-z0-9_-]+\.eyJ`), suspicious filenames (`.env`,
`kubeconfig`, `id_rsa`, `*.pem`). Never block; the tone is "you are
about to pack this — sure?".

Tests: end-to-end walk-mode pack of the fixture workspace produces a
verifiable encrypted archive; plan-mode pack honors `dest` mapping;
the planted fake AWS key is flagged; `--transcripts` toggles
inclusion.

**Done when:** both pack forms produce archives that `verify` and
`inspect` cleanly, with correct summary/boarding-pass output.

---

## M5 — `install` + reconciliation (`internal/reconcile`)

`gobag install <archive> [--root DIR]` (default root: cwd; print the
resolved root first).

Flow:
1. Streaming read: manifest + all entries (buffer state/context in
   memory or scratch under the *target* root — never a world-readable
   temp dir; plaintext of an encrypted archive must not land outside
   the workspace).
2. For each source: `EnsureRepo`, then `EnsureWorktree` per worktree.
3. Unpack `context/`, `skills/`, `state/` with the **overlay rule**:
   file absent → write it; file present and byte-identical → no-op;
   file present and different → keep the user's copy, write
   `<name>.from-gobag` beside it, record the conflict for orientation.
   Converge, never destroy.
4. **Memory re-key** (best effort, failures are orientation warnings,
   never fatal): for each `state/memory/<encoded-old>/` dir, compute
   the new encoded key from the install root (Claude Code's scheme:
   absolute path with `/` → `-`), merge files into
   `~/.claude/projects/<encoded-new>/` under the same overlay rule,
   and prefix-rewrite the old root path → new root inside the small
   `.md` files only.
5. Reconcile: for each source, `git ls-remote` reality vs pinned ref;
   after fetch compute ahead count (`git rev-list --count
   <pinned>..<remote-head>`). Feed typed results from M3 + conflicts
   from step 3.
6. Generate `ORIENTATION.md` at the root. Deterministic (sorted)
   ordering, golden-tested. Sections:
   - **Restored** — what landed where (repos@refs, worktrees, state).
   - **Since you were packed** — per-repo reality diff: "main advanced
     14 commits", "remote unreachable", "worktree recreated at <ref>".
   - **Conflicts & skips** — overlay conflicts, re-key failures,
     transcript skips.
   - **Start here** — pointer to `context/HANDOFF.md`.

Idempotency test: install twice into the same root → second run is all
no-ops, orientation regenerated, byte-identical trees. Round-trip
test: pack fixture workspace on "machine A", install into a fresh
root, assert repos at pinned refs, worktrees recreated, memory
re-keyed under the new encoded dir, ORIENTATION.md matches golden.
Dirty-target test: pre-seed the root with a diverged repo and an
edited context file → converges, nothing lost, conflicts reported.

**Done when:** all three tests above pass.
**Review flag:** `internal/reconcile` — request human review.

---

## M6 — Plugin & skills

- `.claude-plugin/plugin.json` + `.claude-plugin/marketplace.json`
  (the repo is its own marketplace; plugin source `"./"`). Name the
  plugin `gobag`.
- `skills/checkpoint/SKILL.md` — the interrogation. It must direct the
  session to:
  1. **Enumerate honestly**: every repo/directory actually touched
     this session, including outside the cwd — with remote and exact
     ref. Uncommitted or unpushed work: tell the user gobag will not
     carry it; offer to commit/push first.
  2. **Write `context/HANDOFF.md`** answering: current goal and
     status; open threads (anything mid-flight, and exactly where it
     stopped); decisions made and *why* (so the next agent doesn't
     re-litigate them); the where-I-expect-what layout map; gotchas
     and landmines; suggested first three actions after restore.
  3. **Sanitize** the handoff — no credentials, tokens, or internal
     secrets — knowing the binary re-scans regardless.
  4. **Emit `plan.json`** per the M2 schema and run
     `gobag pack --plan plan.json`, bootstrapping the binary first:
     check PATH; if absent, download the release pinned below, verify
     its sha256, install to `~/.local/bin`, ask the user before
     executing anything downloaded.
  5. **Relay the boarding pass** to the user verbatim.
- `skills/restore/SKILL.md`: bootstrap the binary the same way; run
  `gobag install <archive> [--root …]`; read `ORIENTATION.md`, then
  `context/HANDOFF.md`; verify placement and links; summarize state +
  open threads to the user; proceed with the suggested first actions.
  State plainly: this starts a **new** teammate briefed by the old
  one's notes — it does not resurrect the old session.
- Both skills carry the pinned binary version + per-platform sha256s
  in their text, with placeholder values until M7 wires them.

**Done when:** manual E2E passes locally: `/plugin marketplace add
./path/to/gobag`, install the plugin, `/checkpoint` a toy workspace,
move the archive, `/restore` into a fresh directory, and the restored
session correctly summarizes the toy project's state.

---

## M7 — Release

- goreleaser: static builds, linux/darwin × amd64/arm64, checksums
  file, GitHub Releases.
- `scripts/install.sh` (curl|sh): detect OS/arch, download the release
  binary, verify checksum, install to `~/.local/bin` (fallback
  `/usr/local/bin` with sudo warning).
- Release automation (CI step or `make release`) rewrites the pinned
  version + sha256s inside both SKILL.md files and commits — the skill
  pin must never be updated by hand.
- README: three-sentence thesis, the two-command install, a
  `/checkpoint` → move file → `/restore` quickstart, the why-not-
  Docker FAQ lifted from DESIGN.md, headless usage.
- Tag `v0.1.0`. Post-v1: PR to `anthropics/claude-plugins-community`.

**Done when:** on a machine that has never seen the repo, the
two-slash-command install works from GitHub, and the curl|sh line
installs a working binary on macOS and Linux.

---

## Testing bar (applies throughout)

- Table-driven tests; fixtures generated by `internal/testutil`, never
  checked in as `.git` directories.
- Golden files for `ORIENTATION.md`, the pack summary, and the
  boarding pass.
- Integration tests use real system git against `file://` bare-repo
  "remotes" — no network in tests; guard slow ones with
  `testing.Short()`.
- Style per CLAUDE.md: wrapped errors, terse output, no spinners, no
  emoji.

## Explicitly not in v1 (do not build, even if tempting)

Uncommitted-state capture; JSONL path rewriting; X25519 recipients;
S3 helpers; SessionStart hooks; Windows; cross-agent anything; any
daemon or background process.
