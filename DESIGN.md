# gobag — Design

> Your agent's go-bag. Have your Claude Code session pack its own working
> state — distilled context, repo references, expectations — into one
> encrypted archive. A fresh session anywhere picks it up oriented.

## Problem

Long-running Claude Code sessions accumulate irreplaceable working state:
distilled context, memory files, skills, MCP config, and a multi-repo
workspace the agent knows its way around. This "senior engineer teammate"
is far more valuable than a fresh agent, but it is trapped on one machine.
On ephemeral dev environments (devcontainers, cloud workspaces, spot
instances) that machine disappears.

The thesis, refined: the *distilled context* is the agent. Not the bytes.
Exact environment reproduction is a mirage in AI-assisted work anyway —
identical files don't yield identical model responses. gobag optimizes
quality of resumed experience, not bit-fidelity. It productizes the
workflow that already works by hand: "drop a context doc, then pick it up
in the new session" — plus encryption, verification, and reconciliation.

## Architecture: tool + skill

Two layers, split sharply. The tool is mechanism; the skill is policy.

**The binary** is deterministic and owns everything that must never depend
on agent judgment: the archive format, the tar → zstd → age streaming
pipeline, checksums, the mechanical secret scan, git clone/fetch/worktree
operations, idempotent install, and verification of every claim in a pack
plan. It works standalone (walk mode) when no session is available.

**The skills** (a Claude Code plugin, shipped alongside the binary) are the
brain:

- `/checkpoint` — interrogates the running session: every source it
  actually touched (including outside the cwd), open threads, decisions
  made, where it expects what. The session writes `HANDOFF.md` — the
  distilled, sanitized context — and emits `plan.json`. Then it invokes
  `gobag pack --plan plan.json`.
- `/restore <archive>` — bootstraps the binary if missing, runs
  `gobag install`, then reads the orientation and the handoff, verifies
  placement, links things up, and proceeds oriented. Restore starts a
  *new* teammate and hands it the old one's notes — it does not
  resurrect the old session. Explicit invocation is the contract: no
  hook silently injects a packed session's ghost into whatever session
  happens to start in the workspace.

Discovery is interrogation, not inference. The session knows which three
repos it leaned on; a filesystem walk never will. Agent output is
untrusted input: the binary verifies every plan claim (remote reachable?
ref pushed? file exists?) and secret-scans everything, including the
agent's "sanitized" handoff — sanitization by the model is a courtesy,
not a security boundary.

## Non-goals (v1)

- Exact byte-reproducibility of the environment. Explicitly abandoned;
  see thesis.
- Cross-agent portability (Codex, Gemini, etc.). Claude Code only.
- Capturing uncommitted changes / dirty state. Committed refs only.
- Session JSONL path rewriting. Dead entirely — transcripts are keepsakes
  now (see below), and keepsakes don't need surgery.
- Any sync service, daemon, or hosted component. The artifact is the
  product; transport is the user's problem (scp, S3+SSE, whatever).
- Windows.

## The artifact

A self-contained archive. Extension: `.gobag`, whether encrypted or not —
encryption is detected from the age header on read, so users handle exactly
one file with one name.

```
teammate-20260816-1432.gobag        # tar → zstd → (age)
├── MANIFEST.json     # the verified plan: sources (remote + pinned ref),
│                     # worktree topology, layout expectations, skills
│                     # list, MCP config, gobag version
├── context/
│   ├── HANDOFF.md    # agent-authored distilled context — the payload
│   └── ...           # supporting docs the session chose to include
├── state/            # optional raw cargo
│   ├── memory/       # ~/.claude project memory (best-effort re-key
│   │                 # on install)
│   └── sessions/     # transcripts, opt-in via --transcripts: a keepsake
│                     # for people who care about the beautiful convo they
│                     # had, never load-bearing
├── skills/           # vendored, not referenced — install never fetches
└── CHECKSUMS
```

Repos travel as *references* (remote URL + pinned ref), never as content.
MB-scale artifacts, restore-time fresh, small enough to encrypt and
secret-scan as a unit. `context/HANDOFF.md` is the actual cargo of value;
everything else is scaffolding around it.

Transcripts flip from opt-out to opt-in. Continuity comes from the
handoff doc, the same way it does locally when you compact to a doc and
keep going.

## CLI surface (v1)

Single static Go binary, four verbs, no daemon:

- `gobag pack`     — primary form: `--plan plan.json` (skill-driven).
                     Fallback form: `gobag pack <root>` walks a directory
                     and enumerates what it finds — for dying spot
                     instances, cron, and no-session freezes. Both forms
                     verify claims, secret-scan (warn, don't block), and
                     encrypt by default (`--plaintext` to opt out;
                     `--transcripts` to include session JSONLs). Prints
                     the pack plan — sources, refs, warnings (dirty trees,
                     unpushed refs, remote-less repos) — before sealing.
                     Ends with the boarding pass: where the archive is,
                     plus the exact commands for the other side.
- `gobag install`  — mechanical restore into a target root: clone/fetch
                     pinned refs, recreate worktrees, unpack context and
                     state, then emit `ORIENTATION.md` (see handshake).
                     Safe on a dirty target: fetch + fast-forward instead
                     of re-clone, overlay state, regenerate orientation
                     against current reality. Every step idempotent.
- `gobag inspect`  — list contents + manifest without installing.
- `gobag verify`   — validate checksums.

## Reconciliation handshake

Still the soul of the tool, now a dialogue instead of a monologue. Three
voices:

1. The **past agent** wrote its expectations into `HANDOFF.md`: "the
   metrics repo should be at ref X; I left the schema migration
   mid-thread."
2. The **tool** states facts in `ORIENTATION.md`: manifest refs diffed
   against current remote reality — "main advanced 14 commits; worktree
   Y recreated; repo Z's remote unreachable."
3. The **future agent**, seeded by `/restore`, reconciles the two: reads
   both, verifies placement, links what needs linking, and starts work
   oriented, not amnesiac.

This is the moat over `docker commit`, bare `tar`, and a context doc in a
gist: none of them can tell your agent that the world moved while it was
in the bag.

## Repositories too large to clone

A monorepo measured in tens of gigabytes breaks the "clone it on the other
side" assumption: cloning it once per restore is untenable, and on any
machine where you would restore it, a clone almost certainly already
exists. Such a repository travels as a third thing — neither content nor a
clonable reference, but a **located** one.

- `pack` measures each repository's object store (`git count-objects`) and
  marks anything past a threshold (1GB default, `--external-threshold`) as
  external; `--external DEST` forces the decision. The choice is always
  announced, never silent, and the archive records size and the paths it
  occupied here as hints for whoever has to find it there.
- `install` never clones an external repository. It consults the
  per-machine registry and, finding a clone, attaches the workspace to it
  as a **linked worktree**: the object store stays single and shared, so
  the repository costs nothing extra, while the workspace still gets its
  own checkout at the pinned ref. The checkout is detached on purpose —
  the pinned branch is likely checked out in the user's main clone, and
  git allows a branch in only one worktree. Nothing the user is standing
  on moves.
- Finding nothing, install still succeeds. Every other repository lands
  and orientation carries the exact command to finish:
  `gobag link repos/monorepo <path>`.
- `gobag link` is the interactive half, kept out of `install` so install
  stays headless. It verifies the clone is actually the right repository
  (remote match) before attaching anything, and records the answer in
  `~/.gobag/machine.json` so later restores link without asking.

The registry is the one piece of gobag that persists state outside a
workspace. It is written only when the user explicitly links something —
never as a side effect of packing or installing.

## Paths

The canonical root (`~/ws/<name>/`) is demoted from requirement to default
suggestion. Locations are *declared* in the plan, not inferred from layout;
the importing session places and links per the handoff, wherever the new
machine wants things. Identical roots still buy a zero-thought restore.

Raw `~/.claude` memory cargo is re-keyed on install (rename the
encoded-cwd directory, prefix-rewrite the small markdown files) —
best-effort mechanical work, no longer the thing correctness hinges on.
No absolute paths in the manifest or archive, ever; everything is
expressed relative to the install root chosen at restore time.

## Encryption & secrets

- `filippo.io/age`. Scrypt/passphrase recipients for v1; X25519 recipient
  keys are ~15 extra lines and can ship whenever.
- Encrypt-by-default (`--plaintext` to opt out): handoffs and state carry
  credentials, and the artifact must never be shareable-by-accident.
- Pipeline composes as chained writers: `tar → zstd → age`. No temp files,
  no plaintext spills.
- Secret scan at pack time over *everything*, agent-authored docs
  included (untracked-style patterns: .env, kubeconfig, AWS keys,
  tokens). Warn loudly; don't block.
- Optional plaintext manifest sidecar (`<name>.manifest.json`) so
  encrypted archives can be peeked without decryption — manifest holds
  only repo URLs and refs, never secrets.

## Non-determinism, stated plainly

Two packs of the same workspace will differ. Pack quality varies with
session quality. This is a save file, not a build artifact, and the trade
is deliberate: an imperfect handoff doc restored anywhere is still mostly
useful; a "perfect" mechanical restore to the wrong path is silently
broken. Walk mode exists for the cases where determinism actually
matters (unattended freezes).

## Why not Docker (README FAQ)

Docker ships environments; gobag ships working state.

- `docker commit`: bakes secrets into immutable layers, ships GBs to move
  MBs, freezes repos as stale bits with no catch-up protocol, and misses
  bind-mounted repos entirely.
- Dockerfile + init scripts + state tarball: that *is* gobag's walk mode,
  hand-rolled per person, minus encryption, secret scanning, worktree
  recreation, reconciliation — and minus the session's own account of
  what matters.
- Structurally: the target env is already a container. Docker is the
  venue, not the luggage. gobag is a 10MB binary that runs inside
  whatever container the platform provides.

And why not just a context doc in git? That's the workflow gobag grew
from — minus encryption, ref verification, state cargo, worktree
topology, and the handshake. gobag is that habit, productized.

## Implementation constraints

- Go, stdlib-first. Dependency budget: zero to three
  (expected: `filippo.io/age`, a zstd impl, maybe cobra-or-nothing).
- Static binary, linux/darwin × amd64/arm64 via goreleaser.
- Skills ship as a Claude Code plugin in the same repo; the binary never
  depends on them (walk mode is complete without a session).
- Module path: `github.com/satmihir/gobag`. Repo lives under `satmihir/`
  (stars accrue to profile); the `gobag` GitHub org is parked defensively.

## Distribution

The plugin is the front door; the binary is a bootstrapped dependency.

- One repo, self-hosting its own marketplace: `satmihir/gobag` carries
  the Go source plus `.claude-plugin/marketplace.json` and
  `plugin.json`. Install, from day one, from anywhere:

      /plugin marketplace add satmihir/gobag
      /plugin install gobag@gobag

- The skill bootstraps the binary on first use: check PATH; if missing,
  offer the install from GitHub Releases at a version and sha256 pinned
  in the skill text itself. Updating the plugin updates the pin — the
  marketplace is also the binary's update channel.
- Pack and restore are symmetric: both are skills invoked in a session.
  The receiving machine needs only Claude Code — the product's habitat
  by definition — plus the archive. No chicken-and-egg.
- Secondary channels for headless use (CI, provisioning scripts, the
  dying spot instance with no session): GitHub Releases → curl|sh
  installer → Homebrew tap → `go install`. Skip apt/yum/npm-wrapper
  for v1.
- Discovery, post-v1: PR to `anthropics/claude-plugins-community`
  (browsable at claude.com/plugins, automated validation + safety
  screening). The directory changes discovery, not capability.

## Roadmap sketch (post-v1)

1. Uncommitted-state capture (per-repo patches / git bundles).
2. X25519 recipients ("send my teammate to a colleague").
3. Manifest-in-git workflow; S3 helpers.
4. Richer interrogation templates (per-project checkpoint questions).
5. Opt-in SessionStart hook that surfaces a fresh `ORIENTATION.md` in a
   restored workspace — offered by `/restore`, never installed silently.
6. Cross-agent converters (the multi-subscription crowd) — the handoff
   doc is already agent-agnostic prose, so this gets cheaper, not
   harder.
