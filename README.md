# gobag

> **Your agent's go-bag.** Pack the thread, not the machine.

[![CI](https://github.com/satmihir/gobag/actions/workflows/ci.yml/badge.svg)](https://github.com/satmihir/gobag/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/satmihir/gobag)](https://github.com/satmihir/gobag/releases)
[![License](https://img.shields.io/github/license/satmihir/gobag)](LICENSE)

A long-running Claude Code session turns into something worth keeping: it knows
your repos, the decisions and their reasons, the dead ends, the landmine that
cost you an afternoon. All of it lives in one directory on one machine — and it
dies quietly. A devcontainer gets recycled. A spot instance disappears. Most
often nothing dies at all: the session ends, context gets compacted away, and
two weeks later the files are intact but the thread is gone.

gobag makes the thread durable. The session writes down what it knows, keeps
that record current as you work, and seals it into a tiny encrypted archive.
A fresh session — tomorrow, on another machine, after a compaction — picks it
up **oriented, not amnesiac**.

## The moment it clicks

You seal a workspace mid–pull-request on a dying devcontainer:

```
$ gobag seal -label "mid token-refresh PR"
packing "api-migration" — 2 sources, 3 files

Packed: /home/dev/api-migration.gobag (2.1 KB)
```

Yes, 2.1 KB — repos travel as pinned references, not content. A week later you
restore on your workstation, and the first thing the new session reads is
`ORIENTATION.md`, which gobag wrote by comparing the archive against the world
as it is *now*:

```markdown
**Packed on devbox-3f9c (a container); restored on workstation — different
machines.** Any same-named repository that already exists here is unrelated
to this archive, however familiar it looks.

## Since you were packed

- `repos/api` — pinned to `user/token-refresh`, whose tip is unchanged, but
  `main` has advanced 95 commits underneath it (1 ahead, 95 behind). Anything
  the handoff says about merge state, CI, or dependency versions may already
  be false.

## Start here

Read `context/HANDOFF.md` next. It carries the previous session's account of
the work: goal and status, open threads, decisions and their reasoning.
```

That second bullet is the one that saves you: your branch didn't move, so every
other tool would say "nothing changed." gobag tells the new session exactly
what its predecessor could not have known — that the ground moved underneath
the work — before it confidently acts on stale beliefs.

Then it reads the handoff its predecessor wrote — goal, open threads, the
reasoning behind decisions — and continues the work instead of rediscovering it.

## Try it in two minutes

In Claude Code:

```
/plugin marketplace add satmihir/gobag
/plugin install gobag@gobag
```

That's the whole install. The binary fetches itself, checksum-verified, the
first time a skill needs it. Then either:

- **`/checkpoint`** — one shot, right now. The session interrogates itself
  about what it actually touched, writes the handoff, and packs. Built for the
  machine-is-dying moment.
- **`gobag stage init`** — start keeping the thread alive continuously
  (read on).

And on any other machine: `/restore <file>.gobag`. Headless boxes skip the
plugin entirely:

```bash
curl -fsSL https://raw.githubusercontent.com/satmihir/gobag/main/scripts/install.sh | sh
gobag install teammate.gobag
```

## Keep the thread alive between machines dying

Machines die rarely. Sessions die constantly — and compaction is a small death
in the middle of a live one. `gobag stage init` starts a living record of the
thread in `.gobag/stage/`: the handoff document plus pinned refs, as plain
files. The plugin's hooks then keep it honest without ever getting in your way:

- When your context is about to be compacted, gobag records that it happened.
- On a later prompt — and only when the record has actually fallen behind —
  the session gets one nudge:

  > Your context was compacted at 15:54. This workspace keeps a running record
  > of the thread at `.gobag/stage/HANDOFF.md`, last revised before that
  > compaction... **Read it first: it may already contain work you no longer
  > remember doing.**

- Every session end takes a last mechanical snapshot of where the refs stand.

The rule that makes this work across compactions: **the record is the source
of truth about the thread; a session is only ever its editor.** Sessions read
it before revising it, because the session doing the writing may not remember
what it wrote an hour ago.

When you want something shippable, sealing the warm record takes seconds:

```bash
gobag seal -label "after the auth refactor"
```

## What the archive knows that you'd have to guess

`install` doesn't just unpack — it interrogates reality and writes down the
answers:

| It states outright | So the new session never |
| --- | --- |
| Same machine or different one (hashed host identity) | Mistakes a look-alike clone for the original on a uniform fleet |
| How far `main` moved under your pinned feature branch | Trusts a handoff the world has already falsified |
| Which carried files exist in **no commit anywhere** | Lets the archive silently become the last copy of real work |
| How stale the handoff already was when sealed | Reads a four-day-old status table as current |
| Every repo it deliberately left untouched | Wonders whether its dirty checkout was clobbered (it never is) |

At pack time the same honesty runs in reverse: warnings for uncommitted work,
unpushed refs, remote-less repos, possible credentials in the files about to be
sealed — including the ones the agent itself wrote.

## The 30 GB monorepo

A monorepo can't be cloned once per restore — and on any machine where you'd
restore it, a clone almost certainly already exists. gobag measures each repo
and, past a threshold, ships a *located reference* instead:

```
$ gobag install team.gobag
  repos/monorepo: not-linked
  repos/small:    cloned

$ gobag link repos/monorepo ~/src/monorepo
repos/monorepo: linked
  linked as a worktree of ~/src/monorepo, detached — objects are shared,
  nothing was cloned
  remembered — future restores will link it without asking
```

Your workspace gets its own checkout at the pinned ref, the 30 GB object store
stays single, and the branch your main clone is sitting on never moves. Answer
"where does it live?" once per machine.

## What never happens

- **Nothing leaves the machine unencrypted.** Archives are age-encrypted by
  default; `--plaintext` is a loud, explicit opt-out.
- **Nothing of yours is ever destroyed.** Restore into a directory holding real
  work: your files win, the archived versions land beside them as
  `.from-gobag`, dirty and diverged repos are left byte-for-byte alone. Every
  step is idempotent — re-run after any interruption and it converges.
- **Nothing runs in the background.** No daemon, no sync service, no server.
  The archive is the product; move it with scp, S3, or a USB stick.
- **Hooks never act on their own.** They keep the record current and at most
  tell the session to go read it. They never download anything, and they exit
  silently in any workspace without a record.
- **One deliberate exception to "nothing outside the workspace":**
  `~/.gobag/machine.json` remembers where your big repos live on this machine —
  written only by an explicit `gobag link`, never as a side effect.

## Why not…

**`docker commit`?** Bakes secrets into immutable layers, ships gigabytes to
move what gobag moves in kilobytes, freezes repos as stale bits with no
catch-up protocol — and your target environment is usually already a container.
Docker is the venue, not the luggage.

**A context doc in a gist?** That's the habit gobag grew from — productized,
with the repo refs, the encryption, the secret scan, and the reality diff your
gist can't do.

**`tar`?** tar cannot tell your agent that `main` moved 95 commits while the
bag was in transit. That sentence is the product.

## Under the hood

Single static Go binary (three dependencies), verbs not services:
`pack` · `install` · `stage` · `seal` · `link` · `inspect` · `verify`.
The skills do the thinking; the binary verifies every claim they make —
agent output is treated as untrusted input, always.

[DESIGN.md](DESIGN.md) has the reasoning, [PLAN.md](PLAN.md) the build order,
and [CLAUDE.md](CLAUDE.md) the constraints this project refuses to drift from.

## License

Apache 2.0. See [LICENSE](LICENSE).
