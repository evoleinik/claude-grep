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
