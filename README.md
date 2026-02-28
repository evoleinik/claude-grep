# claude-grep

Search Claude Code session history with regex or semantic (vector) search.

## Why

| Problem | Solution |
|---------|----------|
| Claude Code sessions are buried in JSONL files | `claude-grep` searches them like grep |
| Can't find "that conversation about X" | `-s` does meaning-based search via embeddings |
| Python startup adds ~200ms latency | Go binary starts in <10ms |
| No structured output for piping | `--json` outputs clean JSON |

## Install

```bash
# From source
go install github.com/evoleinik/claude-grep@latest

# Or build manually
git clone https://github.com/evoleinik/claude-grep.git
cd claude-grep
go build -o claude-grep .
```

### Semantic search (optional)

Requires [ollama](https://ollama.com) with `nomic-embed-text`:

```bash
curl -fsSL https://ollama.com/install.sh | sh
ollama pull nomic-embed-text
```

## Usage

```bash
# Regex search (default)
claude-grep "worktree"                  # current project, last 7 days
claude-grep -a -d 30 "deploy"          # all projects, last 30 days
claude-grep -p "database"              # your prompts only
claude-grep -r "error"                 # AI responses only
claude-grep -C 2 "migration"           # 2 messages context

# Semantic search
claude-grep --index                    # build vector index (run once)
claude-grep -s "that database fix"     # search by meaning
claude-grep -s -C 1 "notification"     # with context

# JSON output
claude-grep --json "test" | jq .       # pipe to jq
claude-grep -s --json "deploy" | jq '.[0].similarity'

# Session list
claude-grep -l "error"                 # list sessions, not content

# Index management
claude-grep --index                    # index new/changed files
claude-grep --index --all              # reindex everything
claude-grep --index --status           # show index stats

# Usage telemetry
claude-grep --usage                    # see how agents use the tool
```

## Flags

| Flag | Description | Default |
|------|-------------|---------|
| `-p` | Search only user prompts | both |
| `-r` | Search only AI responses | both |
| `-a` | Search all projects | current dir |
| `-l` | List sessions only | off |
| `-n N` | Max results | 20 |
| `-d N` | Max age in days | 7 |
| `-C N` | Context messages (before + after) | 0 |
| `-B N` | Context messages before | 0 |
| `-A N` | Context messages after | 0 |
| `-s` | Semantic search mode | regex |
| `--json` | JSON output | terminal |
| `--index` | Build/update vector index | - |
| `--status` | Show index stats | - |
| `--all` | Reindex everything | incremental |
| `--usage` | Show usage stats (agent telemetry) | - |

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Matches found |
| 1 | No matches |
| 2 | Error |

## How it works

**Regex mode**: Walks `~/.claude/projects/`, parses JSONL session files, matches text with Go regexp. Pre-filters files with `bytes.Contains` for speed. Concurrent file processing.

**Semantic mode**: Embeds query via ollama (`nomic-embed-text`, 768 dims), computes cosine similarity against pre-built index. Index stored as gob files in `~/.claude/search-index/`.

## Use with AI agents

Add to your `CLAUDE.md`:

```markdown
SESSION HISTORY:
- `claude-grep "pattern"` — regex search session history
- `claude-grep -s "query"` — semantic search by meaning
- `claude-grep --json "pattern" | jq .` — structured output
```

## Indexing

### First run

The initial index builds embeddings for all session history. This is slow on CPU (~0.5-1s per message via ollama). A session with 2000 messages takes ~30 minutes on CPU. After the first run, incremental updates only process new/changed files.

```bash
claude-grep --index              # incremental (skips unchanged files)
claude-grep --index --all        # full reindex
claude-grep --index --status     # check progress and stats
```

### Automatic indexing

Set up cron to keep the index fresh. A lockfile prevents concurrent runs — if the previous indexing is still going, the new cron invocation exits immediately.

```bash
(crontab -l; echo '*/30 * * * * $HOME/go/bin/claude-grep --index 2>&1 | logger -t claude-grep') | crontab -
```

### Caveats

- **CPU-only**: No GPU required, but initial indexing is slow. Budget 1-2 hours for a large history. Subsequent runs are fast (seconds).
- **Active sessions**: A session's JSONL file is modified on every message, so active sessions get re-indexed on each cron run. This re-embeds the entire file, not just the new messages.
- **Disk usage**: ~4.5 KB per message (768 float32 dims). 4000 vectors ≈ 17 MB.
- **ollama must be running**: Indexing and semantic search both call ollama's HTTP API. If ollama is stopped, indexing exits with a clear error.

## License

MIT
