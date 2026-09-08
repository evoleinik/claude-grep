## claude-grep

Search Claude Code session history.

### Quick reference

```bash
claude-grep "pattern"              # regex search
claude-grep -s "meaning query"     # semantic search
claude-grep --json "x" | jq .      # JSON output
claude-grep -p -C 2 "error"        # prompts only, with context
claude-grep -a -d 30 "deploy"      # all projects, 30 days
claude-grep --index                # build/update index
claude-grep --index --status       # index stats
```

### Build

```bash
go build -o claude-grep .
go install .
go vet ./...
```

### Architecture

| File | Purpose |
|------|---------|
| `main.go` | CLI flags, routing |
| `search.go` | JSONL parsing, regex search, concurrent file processing |
| `bm25.go` | BM25 query-focused paragraph compression |
| `format.go` | Terminal (colored) and JSON output |
| `index.go` | Ollama embedding, incremental indexer |
| `store.go` | Gob-based vector store on disk |
| `vector.go` | Cosine similarity, semantic search |
| `bench.go` | Session recovery bench: rank scoring + regex-baseline gate |

### Key design decisions

- Zero dependencies (stdlib only + ollama HTTP API)
- Pre-filter with `bytes.Contains` before JSON parse (10x faster)
- Gob encoding for index (fast serialize/deserialize in Go)
- Concurrent file search with fan-out/fan-in pattern
- Embedding model: `embeddinggemma` with ASYMMETRIC prefixes (`embedQuery` / `embedDoc`).
  Never call `embedRaw` directly — an unprefixed query measured 0.433 gold-rank MRR
  against 0.708 prefixed. Changing `embedModel` invalidates every vector: the gob
  carries `EmbedModel`, a mismatch forces a rebuild, and search says so rather than
  silently returning nothing
- Semantic threshold `simFloor` is MODEL-SPECIFIC and must be re-measured on every
  model change (nomic ~0.55, mxbai ~0.62, embeddinggemma 0.35). Carrying one over
  guts recall or floods it with noise
- BM25 compression: terminal output shows query-relevant chunks, not blind head truncation
- Sentence-level splitting: large paragraphs broken into sentences for finer BM25 granularity
- Stop word filtering + suffix stemming: deploy matches deployed/deploying/deployment
- Adaptive budget: 15K total chars divided among matches (3 matches=2000 each, 50=300 each)
- Cross-match content dedup: identical compressed text across sessions is collapsed
- Near-miss hints: when regex finds nothing, extracts longest literal and does relaxed search to suggest simpler patterns
- Auto-fallback: when regex finds 0 results and Ollama is running, automatically retries with semantic search
- Ladder routing: regex → tokenized → semantic, but a tokenized result that FILLS
  the result cap is held as a fallback rather than returned, so the vector layer
  answers first. Returning it immediately scored hit@10 0/6 on paraphrase queries.
  Preferring semantic outright is worse, not better: measured hit@1 3→0 on keyword
  queries, so the cheap layer keeps winning when it actually discriminates
- Tokenized tokens are stop-word filtered. The AND gate is per session FILE, and
  files run tens of KB, so keeping "the"/"and"/"for" admitted nearly every session
- Short-pattern warning: patterns with longest literal ≤3 chars get a stderr hint to use `-s` instead
- Self-exclusion: automatically skips the current session file (most recently modified within 60s) to avoid self-referential results
- JSON output (`--json`) preserves full uncompressed text

### What gets indexed (decided 2026-09-08, do not re-open without a reason)

`extractText` keeps ONLY content blocks of type `text`. `tool_use` and
`tool_result` are deliberately never indexed and never regex-searchable, even
though they hold most of the bytes in a transcript.

That is a decision, not an oversight. Tool output is raw material — file
contents, command results, search hits — which already lives on disk and in git.
The conversation text is the reasoning about it, and reasoning is what you come
back for. Indexing tool blocks would multiply the index for duplicated content
and bury the decisions under their own inputs.

The cost is real and worth knowing: a topic mentioned only in tool output cannot
be found by any query. Measured on two bench sessions, 78% and 81% of a topic's
mentions were in tool blocks. Enough survived in text for both to remain
findable, but a topic that appears ONLY in tool output is invisible.

This is why bench labels carry `expect_topic` and are validated against indexed
text via `parseJSONL` rather than a raw grep of the jsonl.

### Benchmarking recall

`--bench` takes either form:

```bash
claude-grep --bench bench/queries-labeled.json   # labeled: scores rank, can exit 1
claude-grep --bench bench/queries.json           # unlabeled: found/layer only
```

An UNLABELED corpus only proves something came back. It reported 100% while
hit@10 was 0/6, because the tokenized layer answers first with the 100-result
cap and the ladder never reaches the vector layer. Label rows with
`expect_session` (any prefix of the session id) and/or `expect_project` to score
the 1-based rank of that session. The gate has no magic threshold: the ladder
must beat a plain OR-of-words regex over the same corpus.

### Choosing an embedding model

A full reindex is ~6h on box (CPU-bound, no GPU; concurrency plateaus at 4
workers / ~8.4 embeds per second, so there is no local speedup left). Comparing
models against the real index therefore costs most of a day each.

```bash
python3 bench/model-bakeoff.py --models embeddinggemma,mxbai-embed-large
```

It indexes a SUBSET — every labeled target session in full, plus N capped
distractor sessions — and scores the same session-rank metric as the real bench.
Minutes per model, and doc embeddings cache under ~/.cache/claude-grep-bakeoff
so re-running the metric is free. Absolute ranks are optimistic against the full
197k index because the subset is smaller; it is a RELATIVE comparison, which is
what picking a model needs. `claude-grep --bench` stays the gate.

Add a candidate as one row in that file's MODELS table: query prefix, doc
prefix, and its OWN similarity floor. Never reuse another model's floor.

### Agent telemetry

Usage is logged to `~/.claude/search-index/usage.jsonl` (one JSONL line per search).
Run `claude-grep --usage` to see stats: hit rate, empty patterns, BRE usage, retry chains.

Use this data to improve the tool:
- Empty patterns → add hints or new features
- BRE patterns still appearing → CLAUDE.md needs stronger wording
- Retry chains → default scope may be too narrow
- Flag frequency → understand agent vs human usage patterns
