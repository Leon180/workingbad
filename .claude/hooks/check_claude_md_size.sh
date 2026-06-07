#!/bin/bash
# CLAUDE.md context-budget guard.
#
# Fires after Edit/Write/MultiEdit. If the edited file is a CLAUDE.md and it
# exceeds the line budget, surface a warning back to Claude (exit 2) so detail
# gets pushed into a skill instead of bloating always-on context.
#
# See CLAUDE.md > 開發紀律 > Context 紀律.

set -euo pipefail

BUDGET=100   # max lines for CLAUDE.md before warning

if ! command -v jq >/dev/null 2>&1; then
  echo "check_claude_md_size: jq not found — budget guard DISABLED" >&2
  exit 1
fi

input="$(cat)"
file_path="$(printf '%s' "$input" | jq -r '.tool_input.file_path // empty')"
[ -z "$file_path" ] && exit 0

# Only care about CLAUDE.md files.
case "$(basename "$file_path")" in
  CLAUDE.md) ;;
  *) exit 0 ;;
esac

[ -f "$file_path" ] || exit 0

lines="$(wc -l < "$file_path" | tr -d ' ')"
if [ "$lines" -gt "$BUDGET" ]; then
  echo "⚠️  CLAUDE.md context-budget exceeded: ${lines} lines (budget ${BUDGET})." >&2
  echo "CLAUDE.md is ALWAYS in context every turn. Move detail (>5 lines) into a lazy-loaded skill" >&2
  echo "under .claude/skills/ and leave only a one-line pointer here. Run /harness-audit if unsure what to cut." >&2
  exit 2
fi

exit 0
