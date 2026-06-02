#!/usr/bin/env bash
# Freeze historically-empty regex queries from claude-grep telemetry into a
# reproducible benchmark corpus. Re-running refreshes the corpus deliberately.
set -euo pipefail
LOG="${1:-$HOME/.claude/search-index/usage.jsonl}"
OUT="${2:-bench/queries.json}"

jq -s '
  [ .[]
    | select(.mode == "regex" and .results == 0)   # empty regex searches
    | .pattern ]
  | map(select(. != null and . != ""))
  | unique
' "$LOG" > "$OUT"

echo "wrote $OUT ($(jq length "$OUT") queries) from $LOG" >&2
