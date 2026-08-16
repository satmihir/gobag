---
name: checkpoint
description: Pack this workspace into a portable .gobag archive before the machine goes away. Use when the user says checkpoint, pack this up, freeze this workspace, save my session before this devcontainer/instance dies, or asks to move their working state to another machine.
---

# /checkpoint — pack this workspace

You are writing a letter to your successor. A different session, on a different
machine, possibly weeks from now, will read what you produce and try to carry
on. It will have your notes and the repositories — nothing else. Not this
conversation, not your reasoning, not the things you never wrote down.

The archive's real payload is the handoff document you are about to write.
Everything else is scaffolding around it.

## Step 1 — ensure the binary

```bash
sh "${CLAUDE_PLUGIN_ROOT}/scripts/gobag-bootstrap.sh"
```

It prints the path to a verified `gobag` and exits 0, or explains what to do.
If it reports no published binary yet, offer to build from source with
`go install github.com/satmihir/gobag/cmd/gobag@latest`. Never work around a
checksum failure — report it and stop.

## Step 2 — interrogate yourself

Answer these before writing anything. Investigate rather than recall: your
memory of this session is lossy, and the whole point of the exercise is to
catch what a filesystem walk would miss.

**Which sources did this work actually touch?**
- Every repository you read or edited, including ones outside the current
  working directory. Check your additional working directories and any path
  you have referenced this session.
- For each: absolute path, remote URL, current commit sha, current branch.
  Get these from git, not from memory:
  `git -C <path> remote get-url origin`, `git -C <path> rev-parse HEAD`,
  `git -C <path> rev-parse --abbrev-ref HEAD`.
- Repositories with no remote cannot travel — gobag captures references, not
  content. Tell the user which ones, and that they will be missing on the
  other side.

**Is any of it uncommitted or unpushed?**
- `git -C <path> status --porcelain` and
  `git -C <path> log --branches --not --remotes --oneline`.
- v1 does not capture uncommitted changes, and a pinned ref that exists only
  locally will fail to restore. Say so plainly and offer to commit and push
  first. This is the single most common way a restore disappoints someone.

**What context is worth carrying?**
- Design docs, notes, scratch analyses that a successor would need. Prefer a
  few load-bearing documents over everything you can find.
- Skills and MCP configuration specific to this workspace.

**What would a stranger need that only you know?**
- The reasoning behind decisions that look arbitrary from the outside.
- The dead ends, so nobody re-walks them.
- The landmines you discovered the hard way.

## Step 3 — write the handoff document

Write `HANDOFF.md` (put it in the workspace root unless the user prefers
elsewhere). Write for someone with zero context: spell out every name,
abbreviation, and codename this session invented. Prose, not fragments — the
reader is trying to understand, not to skim.

```markdown
# Handoff — <workspace name>

## Goal and status
What this work is trying to achieve, and honestly where it stands.

## Open threads
Everything mid-flight, and exactly where it stopped. Be specific: file, line,
what was about to happen next, what you were uncertain about.

## Decisions and why
Choices already made, with the reasoning. This section exists so your
successor does not re-litigate settled questions or undo something for reasons
you already considered and rejected.

## Where things are
The layout you expect: which repository holds what, which worktree is for
which line of work, where the important documents live. Reference repositories
by their archive destination (e.g. `repos/frontend`), not by absolute path —
the next machine will place them somewhere else.

## Gotchas
Landmines, flaky steps, environment quirks, anything that cost you time.

## Start with these
The two or three concrete actions you would take next if you were continuing.
```

Be candid about uncertainty. "I believe X but never verified it" is far more
useful to your successor than false confidence, and cheaper than the hour they
would spend discovering the truth.

## Step 4 — sanitize

Reread the handoff and remove credentials, tokens, internal hostnames, and
anything else that should not travel in a file the user may hand to a
colleague. The binary re-scans everything at pack time and will warn, but that
scan is a backstop: it matches patterns, it does not understand your prose.

## Step 5 — emit the plan

Write `plan.json`. Absolute paths are correct here — the plan is local input
and never ships. Every `dest` is relative, forward-slashed, and must not
escape the archive root.

```json
{
  "plan_version": 1,
  "name": "<short-workspace-name>",
  "source_root": "/absolute/path/to/the/workspace/root",
  "sources": [
    {
      "path": "/absolute/path/to/frontend",
      "dest": "repos/frontend",
      "remote": "git@github.com:org/frontend.git",
      "ref": "<full sha>",
      "branch": "main",
      "worktrees": [
        { "path": "/absolute/path/to/frontend-wip", "dest": "repos/frontend-wip",
          "ref": "<full sha>", "branch": "wip" }
      ]
    }
  ],
  "context": [
    { "path": "/absolute/path/to/HANDOFF.md", "dest": "context/HANDOFF.md" }
  ],
  "state": {
    "memory": [
      { "path": "/Users/you/.claude/projects/<encoded-cwd>", "dest": "state/memory" }
    ]
  },
  "skills": []
}
```

Keep `context/HANDOFF.md` as the handoff's destination — restore looks for it
there. Always set `source_root`: it is how restore knows which absolute paths
inside memory files refer to this workspace and need rewriting for the new
machine. Include `state.transcripts` only if the user explicitly wants their
session transcripts; they are keepsakes, not continuity, and they can be large.

## Step 6 — pack

```bash
gobag pack --plan plan.json -o <name>.gobag
```

Flags take a single dash (`-o`, `-plaintext`, `-transcripts`); `--` works
too. `-o` takes the archive's full path, not a directory.

Encrypted by default; it will prompt for a passphrase. If no terminal is
available, ask the user for a passphrase and pass it via the `GOBAG_PASSPHRASE`
environment variable for the single command — never write it to a file, and
never put it in the handoff. Use `--plaintext` only if the user explicitly
asks, and say out loud that the archive will be readable by anyone who gets it.

Read the output carefully. `pack` prints what it is about to seal and warns
about dirty trees, unpushed refs, remote-less repositories, and possible
secrets. If a warning matters, stop and tell the user before continuing —
those warnings are exactly the things that make a restore disappoint.

## Step 7 — hand it over

Relay gobag's boarding pass to the user verbatim: where the archive is, and
the exact commands for the other side. Then tell them, in one line, what is in
the bag and what is not — especially anything that could not travel.
