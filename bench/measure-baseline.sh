#!/usr/bin/env bash
# Measure a claude-grep binary on the frozen corpus at a fixed scope.
# Emits a JSON array of {query, found, ms} to stdout. Used for the pre-change baseline.
set -euo pipefail
BIN="${1:-claude-grep}"
CORPUS="${2:-bench/queries.json}"

jq -r '.[]' "$CORPUS" | while IFS= read -r q; do
  start=$(date +%s%N)
  if "$BIN" -a -d 30 -- "$q" >/dev/null 2>&1; then found=true; else found=false; fi
  ms=$(( ($(date +%s%N) - start) / 1000000 ))
  jq -cn --arg q "$q" --argjson f "$found" --argjson ms "$ms" \
    '{query:$q, found:$f, ms:$ms}'
done | jq -s '.'
