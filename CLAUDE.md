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
| `format.go` | Terminal (colored) and JSON output |
| `index.go` | Ollama embedding, incremental indexer |
| `store.go` | Gob-based vector store on disk |
| `vector.go` | Cosine similarity, semantic search |

### Key design decisions

- Zero dependencies (stdlib only + ollama HTTP API)
- Pre-filter with `bytes.Contains` before JSON parse (10x faster)
- Gob encoding for index (fast serialize/deserialize in Go)
- Concurrent file search with fan-out/fan-in pattern
- Semantic search threshold: cosine similarity > 0.3

### Agent telemetry

Usage is logged to `~/.claude/search-index/usage.jsonl` (one JSONL line per search).
Run `claude-grep --usage` to see stats: hit rate, empty patterns, BRE usage, retry chains.

Use this data to improve the tool:
- Empty patterns → add hints or new features
- BRE patterns still appearing → CLAUDE.md needs stronger wording
- Retry chains → default scope may be too narrow
- Flag frequency → understand agent vs human usage patterns
