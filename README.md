<p align="center">
  <img src="assets/logo.png" width="340" alt="gobag: a duffel bag with a thread spooling out of the zipper">
</p>

<h1 align="center">gobag</h1>

<p align="center"><b>Checkpoint a Claude Code workspace and pick it up somewhere else.</b></p>

<p align="center">
  <a href="https://github.com/satmihir/gobag/actions/workflows/ci.yml"><img src="https://github.com/satmihir/gobag/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/satmihir/gobag/releases"><img src="https://img.shields.io/github/v/release/satmihir/gobag" alt="Release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/satmihir/gobag" alt="License"></a>
</p>

gobag saves the useful parts of a Claude Code session: what you were doing,
why you made certain decisions, the files that matter, and the exact commits
you were working from. It puts them in a single encrypted `.gobag` file.

On restore, gobag rebuilds the workspace from Git and writes an
`ORIENTATION.md` explaining what changed in the meantime, including movement
in the pinned branches and their base branches. The new session gets both sides
of the handoff: the previous session's notes and the current state of the
repositories.

Repositories are stored as remote URLs and pinned commits, not copied into the
archive. This keeps bags small, but it also means uncommitted and unpushed work
does not travel. gobag warns about both before packing.

## Install

Add the plugin in Claude Code:

```text
/plugin marketplace add satmihir/gobag
/plugin install gobag@gobag
```

The plugin installs a pinned, checksum-verified `gobag` binary the first time
it needs one.

## Checkpoint and restore

In the session you want to preserve, run:

```text
/checkpoint
```

The session reviews the work it actually used, writes a handoff, and asks
gobag to verify and pack it. Archives are encrypted by default, so you will be
prompted for a passphrase.

Move the resulting `.gobag` file however you normally move files. In a fresh
Claude Code session, run:

```text
/restore /path/to/work.gobag
```

Restore is safe to retry. Existing files are not overwritten; when a carried
file conflicts, the archived copy is written alongside it with a
`.from-gobag` suffix. Dirty or diverged repositories are left alone and called
out in `ORIENTATION.md`.

## Keep a handoff warm

For longer work, you can keep a plain-text record in the workspace instead of
writing a handoff only when you leave:

```bash
gobag stage init
# keep .gobag/stage/HANDOFF.md up to date
gobag stage status
gobag seal -label "before the auth rewrite"
```

The plugin hooks refresh repository facts at session boundaries. If Claude's
context is compacted after the handoff was last edited, the plugin nudges the
session to read and revise it. Nothing runs in the background, and the hooks
do nothing in workspaces without a stage.

The stage is unencrypted because it stays inside the workspace. `gobag seal`
is the boundary for anything you intend to move elsewhere.

## Without Claude Code

The binary also works on its own:

```bash
curl -fsSL https://raw.githubusercontent.com/satmihir/gobag/main/scripts/install.sh | sh

gobag pack /path/to/workspace -o work.gobag
gobag inspect work.gobag
gobag install work.gobag --root /path/to/restore
gobag verify work.gobag
```

Walk mode finds Git repositories plus conventional `HANDOFF.md`, `context.md`,
and project-local Claude skills. A `/checkpoint` is usually better because the
session can identify context outside the current directory and explain the
reasoning that a filesystem walk cannot see.

## Large repositories

Repositories larger than 1 GB are treated as external by default. gobag does
not clone them during restore; it attaches a detached worktree to a clone that
already exists on the target machine:

```bash
gobag link repos/monorepo /path/to/existing/clone
```

That location is remembered for later restores on the same machine. The
existing clone's checked-out branch is never moved.

## Boundaries

- Claude Code only; gobag does not try to preserve or revive a live process.
- Committed Git refs travel. Uncommitted changes do not.
- Transcripts are optional and are not used to restore context.
- Archives use age passphrase encryption unless `--plaintext` is explicit.
- Agent-written plans are verified, and carried files are scanned for likely
  secrets before packing.
- There is no daemon, account, server, or sync service. The archive is yours to
  store and move.

The implementation and its tradeoffs are described in [DESIGN.md](DESIGN.md).
Contributor constraints live in [CLAUDE.md](CLAUDE.md).

## License

Apache 2.0. See [LICENSE](LICENSE).
