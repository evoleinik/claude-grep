# claude-grep: curated-docs search (learnings/ + docs/)

- **Date:** 2026-06-02
- **Status:** Approved (design) — pending implementation plan
- **Branch:** `feat/docs-search`

## Problem

`claude-grep` searches Claude Code session history (conversations). A repo's
*curated* knowledge — gotchas, solutions, decisions — lives in markdown under
`learnings/` or `docs/`. Today an agent must run a **second** tool (`grep -riE
"a|b" learnings/`) to consult that canon. Two problems:

1. **Extra step.** Two separate search commands for "what did we discuss" and
   "what's the documented gotcha."
2. **grep is keyword-only.** It misses synonyms (query "cold start", doc says
   "P1001") and floods context (a common word returns a dozen whole 60–90 KB
   files instead of the one relevant section).

Goal: **one command** that also returns curated-doc hits, with **better-than-grep
results** (semantic, section-level) — without conflating timeless canon with
time-scoped chat.

## Goals / Non-goals

**Goals**
- A plain `claude-grep "x"` returns session matches *and* a curated-docs block.
- Semantic (`-s`) docs search that beats grep on recall and locality.
- Generic: works in any repo with `learnings/` or `docs/` — nothing
  project-specific hardcoded (claude-grep is a global tool).
- A committable pre/post benchmark vs grep that doubles as a CI verifier.

**Non-goals**
- No single blended ranked list (canon vs chat are different shapes — kept as
  separate labeled blocks).
- No config file (env override + discovery defaults only).
- No cron/git-hook for the docs index (it self-heals on use).
- Not touching the session lane or the `searchWithRecovery` ladder.

## Approach (C: auto doc block)

Every search runs two **independent lanes**. The session lane is unchanged. The
docs lane is computed separately and appended as a labeled block. Regex docs
search needs no index (markdown is plain text); semantic docs search uses a
per-repo vector index.

## Design

### Doc discovery (generic, repo-local)

- Resolve the repo via `git rev-parse --show-toplevel` on **cwd** — the current
  worktree, *not* the `mainRepoPath` substitution used for sessions. (Your branch
  may have edited `learnings/`; you want those edits, not main's.)
- Discovery order: `learnings/` if present, else `docs/`.
- Override: env `CLAUDE_GREP_DOCS` = colon-separated relative dirs
  (e.g. `learnings:docs/runbooks`).
- No match, or not in a git repo → docs lane is silent (Rule of Silence), exactly
  like session search when a project has no history.

### Chunking

- Split each `.md` on heading boundaries (`#`/`##`/`###`). A chunk = heading
  path + body, capped at `maxEmbedChars` (2048, existing const).
- A hit is identified as `file § heading` (e.g. `vercel.md § Cron auth`). This is
  what makes a 91 KB file useful — you land on the section, not the file.

### Index schema (minimal extension of `IndexEntry`)

Add one field; repurpose existing fields for doc chunks (documented in a comment):

| Field | Session | Docs chunk |
|-------|---------|------------|
| `Source` (**new**) | `"session"` | `"docs"` |
| `FilePath` | `.jsonl` path | `.md` path |
| `Preview` | first 200 chars | first 200 chars of chunk |
| `MsgIndex` | message index | chunk ordinal in file |
| `Role` | user/assistant | `"doc"` |
| `Timestamp` | message ts | file mtime (informational; **never** used to filter) |
| `Vector` | message embedding | chunk embedding |

- Stored in a **sibling gob**: `<repo-encoded>.docs.gob` (via the existing
  `encodePath`), separate from the session `<project>.gob`. Keeps session
  indexing/cron untouched; lets `--index --docs` run independently.
- The gob filename already disambiguates storage, so `Source` exists purely to
  tag merged in-memory results and the JSON output (`"source":"docs"`). Existing
  session gobs predate the field and decode it as `""` — harmless, since the docs
  lane only ever reads `.docs.gob`.
- Reusing `IndexEntry` (vs a new `DocEntry` type) is a deliberate KISS call:
  one entry type → one loader, one scorer. Cost: a few repurposed field meanings.

### Search lanes

**Regex mode** (`claude-grep "x"`):
- Walk discovered `.md` files, apply the pattern (reuse the literal pre-filter),
  attribute each hit to its enclosing heading → doc matches. No index dependency,
  always live (no staleness).

**Semantic mode** (`-s`):
- Load `<repo>.docs.gob`, cosine similarity against chunks, same `0.55`
  threshold. No index/ollama → docs lane prints nothing and suggests
  `claude-grep --index --docs` once on stderr (Rule of Repair).

**Cross-cutting rules:**
- **Time:** docs ignore `-d`/`-H` entirely. `-d 30` widens sessions only.
- **Scope:** docs always come from the **cwd repo**, independent of `-a`. `-a`
  only widens session scope across `~/.claude/projects/`.
- **Role:** `-p`/`-r` filter sessions only; a role-filtered search skips the docs
  lane (a doc is neither prompt nor response).
- **Cap:** docs block is capped at `min(5, -n)` hits — surface the 1–3 canonical
  sections, never a doc dump.
- **BM25:** the existing terminal compression applies to doc chunk bodies too.

### Output

Terminal — session matches first (as today), then:
```
=== curated docs (learnings/) ===
vercel.md § Cron auth   [0.82]
  <BM25-compressed chunk>
pipeline.md § Content Auto-Publish Loop   [0.79]
  ...
```
`--no-docs` suppresses the block. If sessions return zero but docs hit, the docs
block still prints (often the better answer).

JSON (`--json`) — doc hits are normal entries carrying `"source":"docs"`,
`"file"`, `"heading"`. No separate envelope:
```
claude-grep --json "x" | jq 'map(select(.source=="docs"))'
```

### Flags (all additive)

| Flag | Meaning |
|------|---------|
| `--no-docs` | Suppress the docs block on a search |
| `--index --docs` | Build/refresh the cwd repo's `<repo>.docs.gob` (`--all` = full rebuild) |
| `--index --docs --status` | Repo, files, chunks, size |
| `--bench-docs FILE` | Run the labeled docs benchmark (see below) |
| env `CLAUDE_GREP_DOCS` | Discovery override |

### Indexing strategy — no cron, self-healing

- **Regex lane** reads live files → never stale, no index.
- **Semantic lane** does **lazy incremental refresh**: on each `-s`, re-embed only
  the doc files whose mtime is newer than the gob entry (same incremental pattern
  as session indexing). ~30 small files → milliseconds. ollama down → fall back to
  the existing gob (or skip semantic docs). This removes the need for a cron job
  or git hook — the index self-heals on next use.

### Telemetry

Extend `UsageEvent` with `DocsResults int` + `DocsEngine string`
(`"regex"`/`"semantic"`/`"none"`). Lets `--usage` later report docs hit-rate.
Two fields, nothing more.

## Benchmark (`--bench-docs`)

The existing `--bench` records found/not-found + layer + latency over *unlabeled*
queries. Measuring "better than grep" needs a **labeled** corpus and an engine
axis.

### Corpus — `bench/docs-queries.json`

```json
[
  {"query": "how do we authenticate cron handlers",
   "expect_file": "vercel.md", "expect_heading": "Cron auth"}
]
```

~30 hand-labeled queries (matches the existing corpus size), seeded from:
(1) mining `usage.jsonl` for past searches about documented topics; (2)
hand-picking against known `learnings/` headings. Committed as the gold set.
`expect_heading` is optional (enables section-level scoring).

### Engines (same corpus)

1. **`grep`** (baseline) — the realistic CLAUDE.md invocation:
   `grep -rIli -E "word1|word2|…"` over the docs dir. Unranked.
2. **`cg-regex`** — claude-grep docs regex lane.
3. **`cg-semantic`** — claude-grep docs semantic lane.

### Metrics (target grep's two failure modes)

- **Recall** — `hit@any`: did `expect_file` appear at all? Catches grep's synonym
  blindness.
- **Precision / locality** — `hit@1`/`hit@3` + **MRR** for ranked engines, and
  **avg files returned** for grep. Catches grep's noise (whole-file dump vs ranked
  `file § heading`).

### Output (mirrors `baseline.json` / `after.json` convention)

Bare JSON array on stdout, summary footer on stderr:
```
docs-bench: grep hit@any 18/30 (avg 6.2 files) | cg-regex hit@3 22/30 \
  | cg-semantic hit@3 28/30 mrr 0.81 | p50 40ms p95 120ms
```
Commit a `bench/docs-baseline.json` (grep) and `bench/docs-after.json` (docs lane)
pair — the quotable pre/post numbers.

### CI verifier

Assert **`cg-semantic MRR ≥ grep effective-MRR`** (never worse than grep *at
ranking the right doc*) and print the relabel/rebuild fix command on failure.
grep is unranked, so a found doc sits uniformly within its returned pile →
effective reciprocal rank `2/(files+1)`; grep's noise (more files returned)
lowers it. This is the honest comparison: an earlier `hit@3 ≥ grep hit@any`
formulation **saturated** on small/homogeneous corpora (grep "hits" by dumping
the whole corpus, so hit@any → 100% and the gate could never pass regardless of
semantic quality). Degrades gracefully: no ollama/index → report grep + cg-regex
only, note semantic skipped (don't block the bench).

## Testing (deterministic, TDD-first)

- **Chunker** — markdown → heading chunks (table-driven).
- **Discovery** — `learnings/` vs `docs/` vs env override vs not-a-repo.
- **Regex docs lane** — temp repo, assert hit + correct `§ heading` attribution.
- **Merge/format** — docs block appended; cap `min(5,-n)`; `--no-docs` suppresses;
  `-p`/`-r` skip docs; `-d` ignored for docs.
- **Semantic merge logic** — against a fake in-memory index (isolate the single
  ollama-dependent call, `embed`).
- **Bench** — labeled corpus → per-engine `hit@k` records.

## Sequencing / rollout

- ax-recovery is already merged to `main`, so this branches fresh off `main` — no
  PR stack.
- Land behind no flag gate (additive; `--no-docs` is the only escape hatch).
- After merge, add the two-line docs note to `README.md` and the
  `claude-grep`/`learnings` guidance in the user's global `CLAUDE.md` (the manual
  `grep learnings/` instruction can be relaxed once the docs lane ships).

## Risks / open questions

- **Doc index size.** ~30 files × few chunks each = small. If a repo has a huge
  `docs/` tree, lazy refresh could be slow on first `-s`; the `min(5,-n)` cap and
  per-file mtime skip keep steady-state cheap. Revisit only if a real repo hurts.
- **Heading granularity.** `###`-level chunks may be too fine for some docs;
  start at `##`+`###`, tune from the benchmark.
- **`expect_file` ambiguity.** A query may legitimately match two files; the gold
  set should pick the single canonical one, and the benchmark scores top-k so a
  near-miss still counts at `hit@3`.

## Outcome

_(Fill after implementation: measured grep vs cg-semantic numbers, any design
deviations, follow-ups.)_
