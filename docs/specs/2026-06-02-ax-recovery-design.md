# claude-grep AX Recovery — Design Spec

- **Date:** 2026-06-02
- **Status:** Approved in brainstorming → pending implementation plan
- **Scope owner:** Evgeny
- **Code:** `~/src/claude-grep` (Go, stdlib + Ollama for semantic)

## Problem

30-day `--usage` telemetry (414 searches) exposes one dominant failure class:

| Signal | Value | Meaning |
|--------|-------|---------|
| Hit rate | 74% (106 empty) | 1 in 4 searches returns nothing |
| Prefilter rejected ALL files | **78** | Multi-word queries fed to a regex tool |
| Retry chains | **119** (avg 2.7, worst 16) | Agent flailing after empties |
| Wasted time in chains | **21.6s** | Compounded empty-search latency |
| Cap-hit | 93 (22%) | Results truncated at `-n 100`, silently |

**Root cause.** A query like `"ucp-manifest jsonld-presence product-identifiers generalize"`
is compiled as a case-insensitive regex (`search.go:57`). Spaces are literal, so
`extractPrefilterLiterals` (`search.go:372`) returns the whole phrase as one literal; no
session file contains that exact byte sequence; every file is prefilter-skipped; 0 results.
The agent rewords and retries — a chain. The existing regex→semantic auto-fallback
(`main.go:269`) fires only when Ollama is up and, even then, embeddings are a poor match for
these **keyword-bag** queries where an exact AND-of-terms match is more precise.

AX principles violated: **#9** (empty results are the worst UX), **#11** (auto-escalate
cheap→expensive), **#12** (fix the interface, not the docs).

## Goals / Non-goals

**Goals**
1. Kill the dominant empty-result failure: multi-word queries should rescue automatically.
2. Make truncation visible and the cap hint correct.
3. Align failure hints with the actual failure shape.
4. A deterministic, committed benchmark that proves the fix (replaces "re-run `--usage` in a week").

**Non-goals**
- Duplicate-search short-circuit (deferred — mostly vanishes once recovery works; reading
  `usage.jsonl` per call would tax the success path, AX #8).
- Latency tuning of the 976ms average (retry chains are the real wall-clock cost).
- Semantic index / embedding-model changes.

## Design

### 1. Recovery ladder (core)

Replace the `if len(matches) == 0` block at `main.go:267`. **Failure path only** — the
success path is untouched, so fast searches stay fast (AX #8/#11). New order:

1. **regex as typed** — unchanged.
2. **NEW — tokenized recovery (no Ollama).** Extract word-tokens from the raw pattern
   (`[\w-]+`, lowercased, de-duplicated, length ≥ 2). If ≥ 2 distinct tokens:
   - **Select sessions containing ALL tokens** (file-level AND via a new
     `containsAllTokens(data, tokens)` byte check — the precise prefilter).
   - **Surface messages** in those sessions with an OR-regex `(?i)(t1|t2|…)` reusing the
     existing per-file/per-message loop and `re.MatchString`. RE2 has no lookahead, so AND
     is the file-level gate and OR is the message matcher. Minimal new code.
3. **semantic fallback** — existing (`vector.go:semanticSearch`, Ollama). Safety net for
   AND-misses and genuinely conceptual queries.
4. **hint + near-miss + exit 1** — existing, with §3 changes.

The recovery dispatcher returns `(matches []Match, layer string)` where `layer ∈
{regex, tokenized, semantic, none}`. The live path uses `layer` for telemetry; the
benchmark (§5) records it directly.

- On a tokenized rescue, print one line to **stderr** so the agent learns the better shape:
  `phrase auto-matched as AND-of-terms (4 tokens, 6 sessions)`.
- Log a `UsageEvent{Mode: "tokenized-fallback"}` on rescue (parallel to the existing
  `semantic-fallback`). The `--usage` mode histogram already groups by mode string, so the
  new layer appears automatically.

**Trigger boundary.** Tokenized recovery runs only when (a) the live regex returned 0 and
(b) ≥ 2 word-tokens were extracted. Single-token or metachar-only patterns skip straight to
semantic. This naturally covers alternation-shaped queries
(`"branded_accuracy|rank_for_merchant|sources dict"` → tokens
`[branded_accuracy, rank_for_merchant, sources, dict]`); if file-level AND is too strict for
those, the semantic layer catches them.

### 2. Cap-hit count + correct hint

`regexSearch` already collects **all** matches before truncating to `MaxResults`
(`search.go:124`), so the true total is in hand for free.

- `SearchStats`: add `TotalMatches int`, set to `len(allMatches)` before the
  `[:opts.MaxResults]` slice.
- `printCapHint` (`main.go:485`) currently prints the nonsensical
  `results capped at 100 — narrow your pattern or use -n 100` (suggests the *current* value).
  Rewrite to use the real total: `showing 100 of 437 — narrow the pattern or raise -n (e.g. -n 300)`.
  Stderr. Signature becomes `printCapHint(opts, stats)`.

### 3. Hint alignment

`printNoMatchHint` (`main.go:405`) reorder so the *actionable* line leads. Since semantic was
already auto-tried by the time this prints, drop the implication that `-s` is the next move;
lead with "narrow to the most distinctive token." Show `try -a -d 30` **only** for the
single-literal-not-found case, not phrase queries (where widening scope can't surface a
non-existent literal phrase). Add a one-line stderr note when `normalizeBRE` rewrote the
pattern (`main.go:499`), so the agent learns ERE (currently silent; 9 auto-fixes/30d).

### 4. Testing

Table-driven, matching existing `*_test.go` style:
- `extractWordTokens`: phrase, alternation, hyphen-symbols (`sp-ucp-manifest` → one token,
  not three), metachar-only (→ 0 tokens), single token (→ skip).
- `containsAllTokens`: all-present, one-missing, case-insensitivity.
- `printCapHint` string with a known `TotalMatches`.
- `printNoMatchHint` branch selection (phrase vs single-literal).

### 5. Benchmark (before/after, deterministic)

The mechanical verifier — lives next to the code (Eugene's rule).

- **Corpus** — `bench/queries.json`, committed. Seeded by extracting the real hard queries
  from `~/.claude/search-index/usage.jsonl`: prefilter-reject-all + empty patterns +
  retry-chain members (~80–120 queries). A one-shot extraction script
  (`bench/extract-corpus.sh`) documents provenance; the JSON is the frozen fixture so the
  benchmark never depends on live telemetry drifting.
- **Harness** — `claude-grep --bench bench/queries.json` → JSON array to **stdout** (bare
  array, AX #2), deterministic exit code. Per query: `{query, found, layer, results, ms}`.
  Aggregate footer to **stderr**: hit rate, layer breakdown, p50/p95 latency, still-empty
  count. The harness calls the same recovery dispatcher (§1) in-process, reading `layer`
  directly — **no change to `--json` output shape**, preserving the bare-array contract.
- **Determinism** — every query runs at a fixed scope (`-a`, `-d 30`); the fixture stores no
  per-query flags. Absolute numbers drift slowly as sessions age out of the 30-day window, so
  baseline and post-change runs are executed **on the same day** against an effectively
  identical session corpus — the before/after *delta* is the metric, not the absolute counts.
- **Metric — layer attribution, not found-rate.** Measurement-first revealed that the pristine
  binary already "finds" 163/164 of the corpus at `-a -d 30`: semantic fallback is a catch-all
  whenever Ollama is up, so raw found-rate has ~zero headroom. Tokenized recovery's value is
  **precision + cost** — exact token match, no Ollama, lower latency. Because the ladder tries
  tokenized *before* semantic, one after-run attributes each query to the cheapest layer that
  answered it:
  - **Headline:** "of N historically-empty queries — regex/scope R, **tokenized K** (cheap,
    precise, no Ollama), semantic S, none E" + median latency per layer (tokenized ≪ semantic).
  - `bench/baseline.json` stays committed as the evidence that found-rate is the wrong metric.
  - **No-regression slice:** a sample of queries that already succeeded must stay 100% with
    stable result counts, and success-path p50 latency unchanged.
- Wire to `go test` / CI later (out of scope for this round).

## File-by-file changes

| File | Change |
|------|--------|
| `search.go` | `SearchStats.TotalMatches`; set before truncation; new `tokenizedSearch`, `extractWordTokens`, `containsAllTokens` |
| `main.go` | Recovery dispatcher (regex→tokenized→semantic→hint) returning `layer`; `tokenized-fallback` telemetry + stderr rescue note; `printCapHint(opts, stats)` rewrite; `printNoMatchHint` reorder; BRE-fix stderr note; `--bench` flag → `runBench` |
| `bench.go` (new) | `runBench(fixturePath)`: read fixture, run dispatcher per query, emit per-query JSON + aggregate footer |
| `bench/queries.json` (new) | Frozen corpus of historically-failing queries |
| `bench/extract-corpus.sh` (new) | Provenance: how the corpus was extracted from `usage.jsonl` |
| `bench/baseline.json` (new) | Baseline metrics from the pre-change binary |
| `search_test.go`, `main_test.go` | Unit tests per §4 |
| `README.md` | Document `--bench` + the recovery ladder one-liner |

## Success criteria

1. On `bench/queries.json`: the `tokenized` layer answers a meaningful share of queries that
   the pristine binary could only serve via fuzzy `semantic` (or `none`); tokenized median
   latency is far below semantic median (no Ollama round-trip).
2. No-regression slice stays 100% with stable counts; success-path p50 latency unchanged.
3. Next live `--usage` (1 week post-merge): prefilter-reject count and retry-chain count fall;
   `tokenized-fallback` rescues visible in the mode histogram.

## Prior art (learned constraint)

Commit `3cff314` added a *different* auto-recovery — scope escalation (current-project regex →
**all-projects** regex → semantic) — and it was reverted in `8ca40e6`. The lesson:
**recovery must not silently widen scope.** When an agent scopes to the current project,
surfacing matches from ~950 other projects is surprising noise (and slow over ~10K files).

This design rewrites the *query* (phrase → AND-of-terms) **within the agent's chosen scope**;
it never touches `-a`/`-d`. Scope stays a deliberate agent decision — telemetry shows 7
manual project→all escalations, which is the desired pattern, not a gap to automate away.

## Risks & mitigations

- **File-level AND too strict** (all tokens must co-occur in one session) → semantic layer is
  the safety net; benchmark still-empty count quantifies the residual.
- **Tokenization edge cases** (hyphens, underscores, pure-metachar patterns) → covered by
  unit tests; `[\w-]+` keeps `sp-ucp-manifest` whole.
- **Corpus staleness** → fixture is frozen + provenance script; refresh is a deliberate,
  reviewable act, not silent drift.
