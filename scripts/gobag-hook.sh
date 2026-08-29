#!/bin/sh
# Hook entry point for the gobag plugin.
#
# Hooks run in every session in every workspace, including ones that never
# staged anything and ones where gobag was never installed. So the only
# acceptable failure mode here is silence: a hook must never break a session,
# never prompt, and never print anything the model has to read unless the stage
# genuinely wants attention.
#
# Never bootstraps the binary. Downloading something from a hook, without a
# person present to consent, is not a thing this tool does.
set -u

# No stage, nothing to do — the common case in most workspaces.
[ -d "${CLAUDE_PROJECT_DIR:-$PWD}/.gobag/stage" ] || exit 0

if command -v gobag >/dev/null 2>&1; then
    bin=gobag
elif [ -x "$HOME/.local/bin/gobag" ]; then
    bin="$HOME/.local/bin/gobag"
else
    exit 0
fi

sub=${1:-status}
shift 2>/dev/null || true

case "$sub" in
    refresh)
        # Quiet: the mechanical half has nothing to say to the model.
        "$bin" stage refresh --quiet "$@" "${CLAUDE_PROJECT_DIR:-$PWD}" >/dev/null 2>&1
        ;;
    nudge)
        # Prints the additionalContext JSON, or nothing at all.
        "$bin" stage nudge "${CLAUDE_PROJECT_DIR:-$PWD}" 2>/dev/null
        ;;
    *)
        exit 0
        ;;
esac
exit 0
