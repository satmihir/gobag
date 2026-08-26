# gobag

**Your agent's go-bag.** A long-running Claude Code session accumulates
something worth keeping: distilled context, memory, skills, and a multi-repo
workspace it knows its way around. That teammate is trapped on one machine, and
on a devcontainer or spot instance, that machine disappears.

gobag has the session pack its own working state — the sources it actually
touched, a handoff document it writes itself, its expectations about where
things live — into a single encrypted archive. A fresh session anywhere picks
it up already oriented.

> Status: v1 in development. The API and archive format may still move.

## Install

In Claude Code:

```
/plugin marketplace add satmihir/gobag
/plugin install gobag@gobag
```

That gives you `/checkpoint` and `/restore`. The binary installs itself,
checksum-verified, the first time a skill needs it.

Headless (CI, provisioning scripts, a spot instance with no session running):

```bash
curl -fsSL https://raw.githubusercontent.com/satmihir/gobag/main/scripts/install.sh | sh
```

## Use

Before the machine goes away:

```
/checkpoint
```

The session enumerates what it actually touched, writes a handoff document,
sanitizes it, and packs everything into `teammate-<timestamp>.gobag`. It warns
about the things that will disappoint you later — uncommitted work, unpushed
refs, repositories with no remote, credentials about to be sealed in.

Move the file however you like. gobag has no service and no sync: `scp`, S3, a
USB stick, your problem.

On the other side:

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
- **No absolute paths** in the archive, no daemon, no background process, no
  state left behind.

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
