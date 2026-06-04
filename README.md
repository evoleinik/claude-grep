# claude-grep

[![CI](https://github.com/evoleinik/claude-grep/actions/workflows/ci.yml/badge.svg)](https://github.com/evoleinik/claude-grep/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/evoleinik/claude-grep/branch/main/graph/badge.svg)](https://app.codecov.io/gh/evoleinik/claude-grep)
[![Go Report Card](https://goreportcard.com/badge/github.com/evoleinik/claude-grep)](https://goreportcard.com/report/github.com/evoleinik/claude-grep)
[![Go Reference](https://pkg.go.dev/badge/github.com/evoleinik/claude-grep.svg)](https://pkg.go.dev/github.com/evoleinik/claude-grep)

> grep for your Claude Code conversations.

Every Claude Code session is stored as JSONL under `~/.claude/projects/`. That's a
searchable record of every problem you've solved, every gotcha you've hit, and every
decision you've made — but it's invisible. `claude-grep` makes it greppable: regex or
semantic (vector) search over your entire history, with output tuned for both humans
at a terminal and agents with a finite context window.

It's a single Go binary with no runtime dependencies (ollama is optional, only for
semantic search).

## Why

| Problem | `claude-grep` |
|---------|---------------|
| Sessions are buried in JSONL files | searches them like grep |
| "Where's that conversation about X?" | `-s` finds it by meaning, not exact words |
| Python tooling adds ~200ms startup | Go binary starts in <10ms |
| Raw matches flood the context window | BM25 compression returns only query-relevant text |
| No structured output for piping | `--json` on every command |
| Empty results give an agent zero signal | prints scope + next-step suggestions on a miss |

## Install

```bash
go install github.com/evoleinik/claude-grep@latest
```

Or build from source:

```bash
git clone https://github.com/evoleinik/claude-grep.git
cd claude-grep
go build -o claude-grep .
```

**Semantic search** is optional and needs [ollama](https://ollama.com) with the
`nomic-embed-text` model:

```bash
curl -fsSL https://ollama.com/install.sh | sh
ollama pull nomic-embed-text
```

Regex search, doc search, and JSON output all work without ollama.

## Quick start

```console
$ claude-grep "worktree" -d 30
--- -home-you-src-myapp/b7467e6b-e56 ---
  > 2026-06-01T19:00:39 [YOU] how do I share a Neon branch across two git worktrees?
  > 2026-06-01T19:01:12 [AI ] Each `wt` worktree gets its own Neon branch via the post-checkout
    hook. To share one, point both .env.local files at the same DATABASE_URL [...]

--- -home-you-src-myapp/cb6b9852-9e5 ---
  > 2026-05-22T05:02:56 [AI ] The worktree was created with raw `git worktree add`, so it has no
    node_modules — run `bun install` before the build step [...]
```

Matches are query-focused (BM25), not blind truncation — searching `worktree` also
surfaces `worktrees`, and multi-word queries boost passages where the words appear together.

It's been load-bearing in real day-to-day agent use. A live `--usage` summary:

```console
$ claude-grep --usage
Last 30 days: 762 searches (536 found, 226 empty)
Hit rate: 70%
Avg latency: 1591ms

  regex: 492 (64%)
  semantic-fallback: 205 (26%)
  semantic: 35 (4%)
  tokenized-fallback: 11 (1%)
  docs-only: 19 (2%)

Agent issues:
  BRE patterns (auto-fixed): 12
  Cap-hit searches: 126 (16%)
```

## Usage

```bash
# Regex search (default) — current project, last 7 days
claude-grep "worktree"
claude-grep -a -d 30 "deploy"          # all projects, last 30 days
claude-grep -p "database"              # your prompts only
claude-grep -r "error"                 # AI responses only
claude-grep -C 2 "migration"           # 2 messages of surrounding context

# Semantic search (requires the index + ollama)
claude-grep --index                    # build the vector index (run once)
claude-grep -s "that database fix"     # search by meaning
claude-grep -s -C 1 "notification"     # with context

# JSON output (full, uncompressed text — safe to pipe)
claude-grep --json "test" | jq .
claude-grep -s --json "deploy" | jq '.[0].similarity'

# List matching sessions instead of message content
claude-grep -l "error"

# Index management
claude-grep --index                    # incremental (skips unchanged files)
claude-grep --index --all              # full reindex
claude-grep --index --status           # index stats

# Usage telemetry
claude-grep --usage                    # how agents are using the tool
```

## Flags

| Flag | Description | Default |
|------|-------------|---------|
| `-p` | Search only user prompts | both |
| `-r` | Search only AI responses | both |
| `-a` | Search all projects | current dir |
| `-l` | List sessions only | off |
| `-n N` | Max results | 100 |
| `-d N` | Max age in days | 7 |
| `-H N` | Max age in hours (overrides `-d`) | - |
| `-C N` | Context messages (before + after) | 0 |
| `-B N` | Context messages before | 0 |
| `-A N` | Context messages after | 0 |
| `-s` | Semantic search mode | regex |
| `--json` | JSON output | terminal |
| `--index` | Build/update vector index | - |
| `--status` | Show index stats | - |
| `--all` | Reindex everything | incremental |
| `--usage` | Show usage stats (agent telemetry) | - |
| `--no-docs` | Suppress the curated-docs block | off |
| `--docs-only` | Search ONLY curated docs — no session scan | off |
| `--docs` | With `--index`: (re)build the cwd repo's docs index | - |
| `--bench` | Run the recovery benchmark over a JSON array of queries | - |
| `--bench-docs FILE` | Run the labeled grep-vs-hybrid docs benchmark | - |
| `--mine-docs-queries` | Propose labeled bench cases from `usage.jsonl` | - |
| `--version` | Show version | - |

### Exit codes

| Code | Meaning |
|------|---------|
| 0 | Matches found |
| 1 | No matches |
| 2 | Error |

### Regex syntax

Go regexp (ERE-style), **not** grep BRE. Use `|` not `\|`, `(` not `\(`. BRE escapes
are auto-normalized but should be avoided.

## Searching curated docs

Searches also surface a repo's curated docs (`learnings/` or `docs/`) in a separate
block, so one command covers both *"what did we discuss"* (sessions) and *"what's the
documented gotcha"* (curated notes). Each doc hit is a navigable `file:line § heading`
pointer — e.g. `CLAUDE.md:312 § Python-to-Dashboard Contracts` — so you can jump
straight to it.

```bash
claude-grep "cron auth"          # sessions + a "=== curated docs ===" block
claude-grep -s "cold start"      # hybrid doc hits: dense + keyword, RRF-fused
claude-grep --no-docs "x"        # sessions only
claude-grep --docs-only "x"      # curated docs only — no session scan, head-safe
claude-grep --index --docs       # build/refresh this repo's docs index
```

**Heads up for `head`:** the curated block appends *after* session hits, so a wide
session search piped through `head -N` can truncate it. Split into two commands so the
docs (short — ≤5 hits) are never cut:

```bash
claude-grep -a -d 90 "x" | head -50   # sessions — truncate freely
claude-grep --docs-only "x"           # learnings — read in full
```

`--docs-only` skips the session scan and emits just the curated block (respects `-s`
and `--json`). Exit 1 + a one-line reason on no match or when the cwd has no
`learnings/`/`docs/` dir. The docs lane mirrors the session [recovery ladder](#recovery-ladder):
a regex that matches nothing (e.g. a natural-language multi-word query that compiles to
one literal phrase) auto-escalates to the hybrid lane, so plain `--docs-only "how does X
work"` no longer dead-ends — `-s` is no longer required for it to recover.

Docs come from the current repo (`learnings/` then `docs/`, or set
`CLAUDE_GREP_DOCS=dir1:dir2`) and ignore `-d`/`-a`. The default mode also includes
every git-tracked `README.md`, `CLAUDE.md`, and `MEMORY.md` at any depth (`git ls-files`
skips `node_modules` and gitignored paths). A `README.md`/`MEMORY.md` *inside*
`learnings/`/`docs/` is treated as a table-of-contents and excluded.

`-s` fuses a dense (embedding) lane with a BM25 keyword lane via reciprocal-rank
fusion — dense wins natural-language queries, keyword wins exact-term/identifier
queries. The dense index self-heals on use (re-embeds only changed files, no cron); the
keyword lane needs no index, so `-s` still returns hits when ollama is down.

## Recovery ladder

When a regex returns nothing, claude-grep escalates automatically — within your chosen
scope, never widening `-a`/`-d`:

1. **regex** as typed
2. **tokenized** — multi-word queries retry as AND-of-terms (sessions containing every
   word), surfacing the matching messages. No ollama needed.
3. **semantic** — embedding search (requires the index + ollama)

A stderr note tells you which layer answered. It also prints **near-miss hints**: if
`deploy.*rollback` finds nothing but 3 files contain the literal `deploy`, it suggests
the simpler pattern.

## Use with AI agents

`claude-grep` is built for agents first (see [Design](#design-for-agents) below). Add
this to your `CLAUDE.md` / `AGENTS.md`:

```markdown
SESSION HISTORY:
- `claude-grep "pattern"` — regex search session history
- `claude-grep -s "query"` — semantic search by meaning
- `claude-grep --json "pattern" | jq .` — structured output
- `claude-grep --usage` — check search health and hit rate
```

### Design for agents

Output is treated as a token budget — every line earns its place. The principles, in
priority order:

- **Minimize output.** BM25 compression returns query-relevant chunks, not blind `head`
  truncation. Budget adapts: 3 matches get ~2000 chars each, 100 matches get ~300 each.
- **`--json` everywhere**, preserving full uncompressed text.
- **stdout = data, stderr = diagnostics.** Piping stays clean.
- **Guide on empty results.** A miss prints what was searched (files, days, scope) and
  what to try next — `try: -d 30, -a, -s`.
- **Auto-escalation.** ≤5 session files in a project → widen to all projects. Regex
  finds nothing + ollama running → retry semantic. Eliminates manual retry loops.
- **Self-exclusion.** Skips the session file modified in the last 60s, so the agent
  searching for X doesn't match itself asking about X.
- **Log usage for yourself.** One JSONL line per search → `--usage` aggregates it. Every
  behavior above was discovered by reading real agent telemetry.

## How it works

**Regex mode** walks `~/.claude/projects/`, parses JSONL session files, and matches with
Go `regexp`. Files are pre-filtered with literal substring matching before any JSON
parse (alternation patterns like `(a|b|c)` are decomposed into individual literals and
OR-checked). 8 goroutines process files concurrently.

**Semantic mode** embeds the query via ollama (`nomic-embed-text`, 768 dims) and computes
cosine similarity against the pre-built index (threshold 0.55). Skips file re-reads when
no context is requested (~60× faster). The index lives as gob files in
`~/.claude/search-index/`.

**BM25 compression** uses Okapi BM25 (k1=1.2, b=0.75) to extract the most query-relevant
chunks from each match: split into sentences → tokenize with bigrams, stop-word
filtering, and suffix stemming → score against the query → select within an adaptive
budget → dedupe identical chunks across matches. So `deploy` also matches `deployed` /
`deploying` / `deployment`, and `pip install` boosts passages where the words are adjacent.

## Observability

Every search appends a structured event to `~/.claude/search-index/usage.jsonl`:

```json
{"ts":"...","pattern":"(a|b)","mode":"regex","results":5,"files":500,"ms":1200,"pf_skip":393,"pf_pass":107}
```

`pf_skip`/`pf_pass` are files rejected/passed by the pre-filter. `claude-grep --usage`
gives a 30-day summary: hit rate, latency, empty patterns (improvement candidates),
prefilter diagnostics, retry chains (consecutive searches <90s apart, with wasted time
and scope escalations), duplicate searches, and BRE-misuse warnings.

## Indexing (semantic search)

The first index builds embeddings for all history — slow on CPU (~0.5–1s per message via
ollama; a 2000-message project ≈ 30 min). After that, incremental runs only process
new/changed files.

```bash
claude-grep --index              # incremental
claude-grep --index --all        # full reindex
claude-grep --index --status     # progress and stats
```

Keep it fresh with cron (a lockfile prevents concurrent runs):

```bash
(crontab -l; echo '*/30 * * * * $HOME/go/bin/claude-grep --index 2>&1 | logger -t claude-grep') | crontab -
```

**Caveats:**

- **CPU-only**, no GPU required, but budget 1–2 hours for the first index of a large
  history. Subsequent runs are seconds.
- **Active sessions** are re-embedded in full on each cron run (their JSONL changes on
  every message).
- **Disk:** ~4.5 KB per message (768 float32 dims); 4000 vectors ≈ 17 MB.
- **ollama must be running** for indexing and semantic search; otherwise both exit with a
  clear error.

## Build from source

```bash
go build -o claude-grep .
go test ./...
go vet ./...
```

Benchmark the recovery ladder against the recorded baseline:

```bash
./bench/extract-corpus.sh                 # refresh corpus from telemetry (optional)
claude-grep --bench bench/queries.json    # JSON records to stdout, summary to stderr
```

`bench/baseline.json` is the pre-recovery-ladder measurement; compare after-runs against it.

## License

[MIT](LICENSE)
