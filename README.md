# gobag

**Your agent's go-bag.** A long-running Claude Code session accumulates
something worth keeping: distilled context, memory, skills, and a multi-repo
workspace it knows its way around. That teammate is trapped on one machine, and
on a devcontainer or spot instance, that machine disappears.

Mostly it isn't the machine that dies. The session ends, context gets compacted
away, and eleven days later the files are all still there while the thread has
gone cold.

gobag has the session write down what it knows — the sources it actually
touched, a handoff document it maintains itself, its expectations about where
things live — and keeps that record current as you work. When you need it
somewhere else, it seals into a single encrypted archive that a fresh session
anywhere picks up already oriented.

> Status: v0.1.0 released. The archive format may still move before v1.

## Install

In Claude Code:

```
/plugin marketplace add satmihir/gobag
/plugin install gobag@gobag
```

That gives you `/checkpoint` and `/restore`, plus the hooks that keep a
workspace's record current. The binary installs itself, checksum-verified, the
first time a skill needs it.

Headless (CI, provisioning scripts, a spot instance with no session running):

```bash
curl -fsSL https://raw.githubusercontent.com/satmihir/gobag/main/scripts/install.sh | sh
```

## Two ways to use it

### Keeping a thread alive (the common one)

```bash
gobag stage init
```

The workspace starts keeping a record of the thread in `.gobag/stage/`: a
living `HANDOFF.md` plus the repository refs. Plain files, deliberately
unencrypted — they sit next to the repos and `.env` files they describe, all
equally unencrypted, and the threat model for an archive is transit, not
residence.

From then on the plugin's hooks keep it honest:

- **PreCompact** records that your context was about to be compacted away.
- **UserPromptSubmit** stays silent on virtually every prompt — and speaks only
  when the record has fallen behind something that actually happened:

  > Your context was compacted at 15:54 on 29 Aug. This workspace keeps a
  > running record of the thread at `.gobag/stage/HANDOFF.md`, last revised
  > before that compaction... Read it first: it may already contain work you no
  > longer remember doing.

- **SessionEnd** takes a last mechanical snapshot of where the refs stand.

Check on it any time, and ship it when you want something portable:

```bash
gobag stage status
gobag seal -label "after the auth refactor"
```

Sealing a warm stage takes seconds rather than a full interrogation — which is
the point, because cheap seals are the ones that actually happen.

### One shot, right now (the machine is dying)

```
/checkpoint
```

No stage required. The session enumerates what it actually touched, writes a
handoff, sanitizes it, and packs everything into `teammate-<timestamp>.gobag`.
It warns about the things that will disappoint you later — uncommitted work,
unpushed refs, repositories with no remote, credentials about to be sealed in,
and files this archive is about to become the only copy of.

Move the file however you like. gobag has no service and no sync: `scp`, S3, a
USB stick, your problem.

### Picking it up on the other side

```
/restore ~/Downloads/teammate-20260816-1432.gobag
```

The new session restores the repositories, reads what changed while the bag was
packed, reads its predecessor's notes, and tells you where things stand.

## What travels

| Travels | Does not |
| --- | --- |
| Repositories as references (remote + pinned ref) | Repository contents |
| The handoff document the session wrote | Uncommitted changes |
| Claude Code memory and skills | Anything unpushed |
| Worktree topology, MCP config | Session transcripts, unless you ask |

Repositories travel as references, which is why archives are megabytes rather
than gigabytes, and why a restore gives you a fresh clone rather than stale
bits.

## Repositories too big to clone

A monorepo of tens of gigabytes can't be cloned once per restore — and on any
machine where you'd restore it, you almost certainly already have a clone. So
it travels as a *located* reference instead:

```
$ gobag install team.gobag --root ~/ws/proj
  repos/monorepo: not-linked
  repos/small:    cloned

$ gobag link repos/monorepo ~/src/monorepo
repos/monorepo: linked
  linked as a worktree of ~/src/monorepo at 97f576e5, detached
  — objects are shared, nothing was cloned
  remembered — future restores will link it without asking
```

gobag measures each repo and marks anything past 1GB external (tune with
`--external-threshold`, force with `--external DEST`). Install never clones
one; it attaches a linked worktree to the clone you already have, so the
object store stays single and your main checkout never moves. Answer once per
machine and later restores link it automatically.

## The interesting part

`install` does not just unpack. It compares what the archive remembers against
the world as it is now, and writes `ORIENTATION.md`: main advanced fourteen
commits, this worktree was recreated, that remote is unreachable, these files
you had already differed and were left alone.

It also states the things a restored session would otherwise have to guess:
whether this is even the machine that packed the bag (on a uniform fleet, a
familiar-looking clone is not evidence), how far the default branch moved under
a pinned feature branch, which carried files exist in no commit, and how stale
the handoff already was when it was sealed.

The same discipline runs through the stage: **it is the source of truth about
the thread, and a session is only ever its editor.** Read it before writing it —
the session doing the writing may have been compacted since it last wrote, and
may not remember writing at all.

Restore then has two documents that say different kinds of thing:

- `ORIENTATION.md` is **fact** — what is on disk and what moved.
- `context/HANDOFF.md` is **intent** — the previous session's goal, open
  threads, decisions, and expectations.

The gap between them is the most valuable thing in the bag: it is exactly what
the previous session could not have known.

## Safety

- **Encrypted by default** (age, passphrase). `--plaintext` is an explicit
  opt-out, because a handoff document is a credential-shaped object.
- **Secret-scanned at pack time**, including the agent's own writing. Model
  sanitization is a courtesy, not a boundary. Warnings never block.
- **Never destroys.** Restoring into a directory that already holds work keeps
  your version and writes the archived one beside it as `.from-gobag`.
  Every step is idempotent; re-running after an interruption converges.
- **No absolute paths** in the archive, no daemon, no background process.
- **Almost nothing outside your workspace.** The record lives in
  `.gobag/stage/` inside the workspace it describes. The one exception is
  `~/.gobag/machine.json`, which remembers where your large repositories live
  on this machine — written only when you explicitly run `gobag link`, never as
  a side effect of packing or restoring.
- **Hooks never act on their own.** They keep the record current and, at most,
  tell the session to go read it. They never install anything, never inject an
  archive's contents into a session, and exit silently in any workspace that has
  no record.

## Why not Docker?

Docker ships environments; gobag ships working state.

`docker commit` bakes secrets into immutable layers, moves gigabytes to carry
megabytes, freezes repositories as stale bits with no catch-up protocol, and
misses bind-mounted repos entirely. And structurally, the target environment is
usually already a container — Docker is the venue, not the luggage.

Dockerfile plus init scripts plus a state tarball is gobag's walk mode,
hand-rolled per person, minus encryption, secret scanning, worktree recreation,
reconciliation, and the session's own account of what mattered.

**Why not just a context doc in git?** That is the habit gobag grew from. This
is that habit, productized: with the repository refs, the reality diff, the
encryption, and the state your context doc never captured.

**Why not `tar`?** tar cannot tell your agent that main moved fourteen commits.

## Design

[DESIGN.md](DESIGN.md) is the reasoning; [PLAN.md](PLAN.md) is the build order;
[CLAUDE.md](CLAUDE.md) is the set of constraints this project refuses to drift
from.

## License

Apache 2.0. See [LICENSE](LICENSE).
