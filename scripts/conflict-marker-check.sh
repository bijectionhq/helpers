#!/usr/bin/env bash
# Refuse a commit that would record an unresolved merge state.
#
# An automatic commit sweep can run while a merge is half-resolved, and the
# markers then reach HEAD where every later reader trusts them as content.
# This scans the index — exactly the bytes a commit right now would record,
# which for an unstaged file is the same content HEAD already holds — so the
# same command serves the pre-commit hook and a manual verification.
#
# The pattern covers all three exact structural lines, including an orphaned
# separator. Live Markdown uses ATX headings (`#`), so a seven-equals Setext
# underline is forbidden in exchange for making the corruption gate complete.
# Immutable source locks, raw inputs, and legal notices may quote or use these
# lines and remain explicitly outside the live-tree ratchet. Binary files are
# excluded by git grep -I.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

pattern='^(<<<<<<<( .*)?|=======|>>>>>>>( .*)?)$'

if matches=$(git grep --cached -n -I -E "$pattern" -- . \
  ':(exclude)llm-wiki/raw/**' \
  ':(exclude)**/*.lock.md' \
  ':(exclude,glob)**/NOTICE*' \
  ':(exclude,glob)**/LICENSE*' \
  ':(exclude,glob)**/COPYING*' 2>/dev/null); then
  printf '%s\n' "$matches" >&2
  echo >&2
  echo "conflict-marker-check: unresolved merge markers in the staged tree." >&2
  echo "conflict-marker-check: finish the merge before committing." >&2
  exit 1
fi

echo "conflict-marker-check: clean ($(git ls-files | wc -l | tr -d ' ') tracked files)"
