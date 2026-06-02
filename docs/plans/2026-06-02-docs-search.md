# Curated-Docs Search Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `claude-grep "x"` also return a separate "curated docs" block from the current repo's `learnings/`/`docs/`, with semantic section-level hits that beat plain grep.

**Architecture:** Two independent lanes. The session lane is unchanged. A new docs lane discovers the cwd repo's doc dir, chunks markdown by heading, and searches it — regex reads `.md` live (no index), semantic uses a per-repo `<repo>.docs.gob` that self-heals on use. Docs are merged at the output layer only (never through `Match`), so the session path is untouched.

**Tech Stack:** Go (stdlib + existing ollama embed via HTTP), gob index, BM25 compression. No new dependencies.

**Spec:** `docs/specs/2026-06-02-docs-search-design.md`

---

## File Structure

**New files:**
- `docs.go` — discovery (`discoverDocsDir`), chunking (`chunkMarkdown`), doc index (`docsIndexPath`, `loadDocsIndex`, `saveDocsIndex`, `buildDocEntries`, `refreshDocsIndex`).
- `docs_search.go` — `DocMatch`, the two lanes (`regexDocsSearch`, `semanticDocsSearch`), the dispatcher (`collectDocs`, `docsCap`).
- `docs_format.go` — terminal block (`printDocsBlock`) + JSON conversion (`docsToJSON`).
- `docs_bench.go` — labeled benchmark (`--bench-docs`): corpus types, three engines, metrics.
- Tests: `docs_test.go`, `docs_search_test.go`, `docs_format_test.go`, `docs_bench_test.go`.
- `bench/docs-queries.json` — gold corpus.

**Modified files:**
- `index.go` — add `Source` + `Heading` fields to `IndexEntry`.
- `format.go` — add `Source`/`File`/`Heading` (omitempty) to `JSONMatch`; extract `buildJSONMatches` + `encodeJSON` so docs can be appended to one array.
- `telemetry.go` — add `DocsResults` + `DocsEngine` to `UsageEvent`.
- `main.go` — flags (`--no-docs`, `--docs` with `--index`, `--bench-docs`); wire `collectDocs` into the regex + semantic output paths; doc index build branch.

## Shared signatures (consistent across all tasks)

```go
// docs.go
type DocChunk struct { Heading string; Body string; Ordinal int }
func chunkMarkdown(data []byte) []DocChunk
func discoverDocsDir(cwd string) (repoRoot string, dirs []string, ok bool) // dirs absolute
func docsIndexPath(repoRoot string) string
func loadDocsIndex(repoRoot string) *Index
func saveDocsIndex(repoRoot string, idx *Index) error
func buildDocEntries(file string, chunks []DocChunk, embedFn func(string) ([]float32, error)) ([]IndexEntry, error)
func refreshDocsIndex(repoRoot string, dirs []string, embedFn func(string) ([]float32, error)) error

// docs_search.go
type DocMatch struct { File string; Heading string; Text string; Similarity float32 }
func docsCap(maxResults int) int                                  // min(5, maxResults)
func regexDocsSearch(pattern string, dirs []string, cap int) ([]DocMatch, error)
func semanticDocsSearch(query, repoRoot string, dirs []string, cap int) ([]DocMatch, error)
func collectDocs(cwd, pattern string, semantic bool, maxResults int) (docs []DocMatch, engine string)

// docs_format.go
func printDocsBlock(label string, docs []DocMatch)                // label e.g. "learnings/"
func docsToJSON(docs []DocMatch) []JSONMatch

// docs_bench.go
type DocsBenchQuery struct { Query, ExpectFile, ExpectHeading string }
type EngineResult struct { HitRank int; Files int }               // HitRank 0=miss, else 1-based
type DocsBenchRecord struct { Query, ExpectFile string; Grep, CgRegex, CgSemantic EngineResult }
func runDocsBenchRecords(corpusPath, repoRoot string, dirs []string) []DocsBenchRecord
```

---

### Task 1: Extend IndexEntry with Source + Heading

**Files:**
- Modify: `index.go:11-20` (the `IndexEntry` struct)
- Test: `docs_test.go` (new)

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIndexEntryGobRoundTripWithDocFields(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir) // indexDir() = $HOME/.claude/search-index
	idx := &Index{
		Files:   map[string]FileMetadata{},
		Project: "x.docs",
		Entries: []IndexEntry{{
			Source: "docs", Heading: "Cron auth", FilePath: "/r/learnings/vercel.md",
			Preview: "every cron handler must compare", Vector: []float32{0.1, 0.2},
		}},
	}
	// saveDocsIndex/loadDocsIndex arrive in Task 4; here assert the fields persist
	// through the existing gob path by writing to a known project name.
	if err := os.MkdirAll(indexDir(), 0755); err != nil { t.Fatal(err) }
	if err := saveIndex(idx); err != nil { t.Fatal(err) }
	got := loadIndex("x.docs")
	if len(got.Entries) != 1 { t.Fatalf("want 1 entry, got %d", len(got.Entries)) }
	e := got.Entries[0]
	if e.Source != "docs" || e.Heading != "Cron auth" {
		t.Errorf("doc fields lost: Source=%q Heading=%q", e.Source, e.Heading)
	}
	_ = filepath.Join // keep import if unused after edits
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestIndexEntryGobRoundTrip ./...`
Expected: FAIL — `unknown field 'Source' in struct literal of type IndexEntry`.

- [ ] **Step 3: Add the fields**

In `index.go`, add two fields to `IndexEntry` (after `Preview`):

```go
type IndexEntry struct {
	SessionID string
	MsgIndex  int
	Role      string
	Timestamp string
	Preview   string // first 200 chars of text
	FilePath  string
	Vector    []float32
	Source    string // "" or "session" for chat; "docs" for markdown chunks
	Heading   string // markdown heading path for doc chunks; "" for chat
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestIndexEntryGobRoundTrip ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add index.go docs_test.go
git commit -m "feat: add Source + Heading fields to IndexEntry"
```

---

### Task 2: Doc directory discovery

**Files:**
- Create: `docs.go`
- Test: `docs_test.go` (append)

`discoverDocsDir` resolves the cwd repo root via `git rev-parse --show-toplevel`, then picks doc dirs: env `CLAUDE_GREP_DOCS` (colon-separated, relative to root) overrides; else `learnings/` then `docs/`. Returns absolute dirs that exist.

- [ ] **Step 1: Write the failing test**

```go
func TestDiscoverDocsDir(t *testing.T) {
	root := t.TempDir()
	mustInitGitRepo(t, root) // helper below
	os.MkdirAll(filepath.Join(root, "learnings"), 0755)
	os.MkdirAll(filepath.Join(root, "docs"), 0755)

	// Default: learnings/ wins over docs/
	gotRoot, dirs, ok := discoverDocsDir(root)
	if !ok || gotRoot != resolveSymlinks(root) { t.Fatalf("ok=%v root=%q", ok, gotRoot) }
	if len(dirs) != 1 || filepath.Base(dirs[0]) != "learnings" {
		t.Fatalf("want [learnings], got %v", dirs)
	}

	// Env override
	t.Setenv("CLAUDE_GREP_DOCS", "docs")
	_, dirs, _ = discoverDocsDir(root)
	if len(dirs) != 1 || filepath.Base(dirs[0]) != "docs" {
		t.Fatalf("env override failed, got %v", dirs)
	}
}

func TestDiscoverDocsDirNotARepo(t *testing.T) {
	dir := t.TempDir() // no git init
	if _, _, ok := discoverDocsDir(dir); ok {
		t.Error("expected ok=false outside a git repo")
	}
}

// helpers (put in docs_test.go)
func mustInitGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q", dir)
	if err := cmd.Run(); err != nil { t.Skipf("git unavailable: %v", err) }
}
func resolveSymlinks(p string) string { r, _ := filepath.EvalSymlinks(p); return r }
```

Add imports `os/exec` to the test file.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestDiscoverDocsDir ./...`
Expected: FAIL — `undefined: discoverDocsDir`.

- [ ] **Step 3: Implement `discoverDocsDir` in `docs.go`**

```go
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// discoverDocsDir resolves the cwd repo and its curated-doc dirs.
// Uses the CURRENT worktree root (not the session mainRepoPath substitution):
// a feature branch may have edited learnings/, and those edits are what we want.
func discoverDocsDir(cwd string) (repoRoot string, dirs []string, ok bool) {
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", nil, false
	}
	repoRoot = strings.TrimSpace(string(out))
	if repoRoot == "" {
		return "", nil, false
	}

	var candidates []string
	if env := os.Getenv("CLAUDE_GREP_DOCS"); env != "" {
		for _, rel := range strings.Split(env, ":") {
			if rel = strings.TrimSpace(rel); rel != "" {
				candidates = append(candidates, rel)
			}
		}
	} else {
		candidates = []string{"learnings", "docs"}
	}

	for _, rel := range candidates {
		abs := filepath.Join(repoRoot, rel)
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			dirs = append(dirs, abs)
			if os.Getenv("CLAUDE_GREP_DOCS") == "" {
				break // default mode: first existing dir only (learnings beats docs)
			}
		}
	}
	return repoRoot, dirs, len(dirs) > 0
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestDiscoverDocsDir ./...`
Expected: PASS (or SKIP if git is unavailable in the sandbox).

- [ ] **Step 5: Commit**

```bash
git add docs.go docs_test.go
git commit -m "feat: discover cwd-repo doc dirs (learnings/ then docs/, env override)"
```

---

### Task 3: Markdown chunker

**Files:**
- Modify: `docs.go` (append `chunkMarkdown`)
- Test: `docs_test.go` (append)

A chunk starts at a heading line (`#`/`##`/`###`) and runs until the next heading of any level. Content before the first heading is one chunk labeled `(intro)`. Body capped at `maxEmbedChars` (existing const, 2048).

- [ ] **Step 1: Write the failing test**

```go
func TestChunkMarkdown(t *testing.T) {
	md := []byte("intro line\n\n# Title\nbody a\n\n## Cron auth\nguard if !secret\n### nested\ndeep\n")
	chunks := chunkMarkdown(md)
	// (intro), Title, Cron auth, nested
	if len(chunks) != 4 {
		t.Fatalf("want 4 chunks, got %d: %+v", len(chunks), chunks)
	}
	if chunks[0].Heading != "(intro)" || !strings.Contains(chunks[0].Body, "intro line") {
		t.Errorf("intro chunk wrong: %+v", chunks[0])
	}
	if chunks[2].Heading != "Cron auth" || !strings.Contains(chunks[2].Body, "guard if !secret") {
		t.Errorf("cron chunk wrong: %+v", chunks[2])
	}
	if chunks[3].Ordinal != 3 {
		t.Errorf("ordinal not sequential: %+v", chunks[3])
	}
}

func TestChunkMarkdownNoHeadings(t *testing.T) {
	chunks := chunkMarkdown([]byte("just a paragraph\nmore text\n"))
	if len(chunks) != 1 || chunks[0].Heading != "(intro)" {
		t.Fatalf("want single intro chunk, got %+v", chunks)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestChunkMarkdown ./...`
Expected: FAIL — `undefined: chunkMarkdown`.

- [ ] **Step 3: Implement `chunkMarkdown`**

```go
// chunkMarkdown splits markdown into one chunk per heading section.
// Content before the first heading becomes an "(intro)" chunk (dropped if blank).
func chunkMarkdown(data []byte) []DocChunk {
	lines := strings.Split(string(data), "\n")
	var chunks []DocChunk
	heading := "(intro)"
	var body strings.Builder

	flush := func() {
		text := strings.TrimSpace(body.String())
		body.Reset()
		if text == "" && heading == "(intro)" {
			return // skip empty preamble
		}
		if len(text) > maxEmbedChars {
			text = text[:maxEmbedChars]
		}
		chunks = append(chunks, DocChunk{Heading: heading, Body: text, Ordinal: len(chunks)})
	}

	for _, ln := range lines {
		if h := headingText(ln); h != "" {
			flush()
			heading = h
			continue
		}
		body.WriteString(ln)
		body.WriteString("\n")
	}
	flush()
	return chunks
}

// headingText returns the text of a markdown ATX heading (#/##/###...), or "".
func headingText(line string) string {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "#") {
		return ""
	}
	s = strings.TrimLeft(s, "#")
	if s == "" || (len(s) > 0 && s[0] != ' ') {
		return "" // e.g. "#nottag" or bare "#"
	}
	return strings.TrimSpace(s)
}
```

Add `DocChunk` to `docs.go` (top, after imports):

```go
type DocChunk struct {
	Heading string
	Body    string
	Ordinal int
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestChunkMarkdown ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add docs.go docs_test.go
git commit -m "feat: chunk markdown by heading section"
```

---

### Task 4: Doc index — path, load/save, build entries, lazy refresh

**Files:**
- Modify: `docs.go` (append)
- Test: `docs_test.go` (append)

Reuses the existing `Index` struct + gob loader. Sibling gob `<repo-encoded>.docs.gob`. `buildDocEntries` and `refreshDocsIndex` take an `embedFn` so tests inject a fake (the real call is `embed` from `index.go`, ollama-dependent).

- [ ] **Step 1: Write the failing test**

```go
func TestBuildDocEntries(t *testing.T) {
	fake := func(s string) ([]float32, error) { return []float32{float32(len(s))}, nil }
	chunks := []DocChunk{{Heading: "Cron auth", Body: "guard if !secret", Ordinal: 0}}
	entries, err := buildDocEntries("/r/learnings/vercel.md", chunks, fake)
	if err != nil { t.Fatal(err) }
	if len(entries) != 1 { t.Fatalf("want 1, got %d", len(entries)) }
	e := entries[0]
	if e.Source != "docs" || e.Heading != "Cron auth" || e.FilePath != "/r/learnings/vercel.md" {
		t.Errorf("bad entry: %+v", e)
	}
	if e.Role != "doc" || len(e.Vector) != 1 {
		t.Errorf("bad entry meta: %+v", e)
	}
}

func TestRefreshDocsIndexIncremental(t *testing.T) {
	home := t.TempDir(); t.Setenv("HOME", home)
	root := t.TempDir()
	ldir := filepath.Join(root, "learnings"); os.MkdirAll(ldir, 0755)
	f := filepath.Join(ldir, "a.md")
	os.WriteFile(f, []byte("# H\nalpha beta\n"), 0644)

	calls := 0
	fake := func(s string) ([]float32, error) { calls++; return []float32{1}, nil }

	if err := refreshDocsIndex(root, []string{ldir}, fake); err != nil { t.Fatal(err) }
	first := calls
	if first == 0 { t.Fatal("expected embeds on first build") }

	// Unchanged file → no re-embed
	if err := refreshDocsIndex(root, []string{ldir}, fake); err != nil { t.Fatal(err) }
	if calls != first { t.Errorf("re-embedded unchanged file: %d -> %d", first, calls) }

	// Touch file → re-embed
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(f, []byte("# H\nalpha beta gamma\n"), 0644)
	if err := refreshDocsIndex(root, []string{ldir}, fake); err != nil { t.Fatal(err) }
	if calls == first { t.Error("expected re-embed after file change") }

	idx := loadDocsIndex(root)
	if len(idx.Entries) == 0 { t.Error("index empty after refresh") }
}
```

Add `time` to the test imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run 'TestBuildDocEntries|TestRefreshDocsIndex' ./...`
Expected: FAIL — `undefined: buildDocEntries`.

- [ ] **Step 3: Implement in `docs.go`**

```go
func docsIndexPath(repoRoot string) string {
	return filepath.Join(indexDir(), encodePath(repoRoot)+".docs.gob")
}

func loadDocsIndex(repoRoot string) *Index {
	idx := &Index{Files: make(map[string]FileMetadata), Project: encodePath(repoRoot) + ".docs"}
	f, err := os.Open(docsIndexPath(repoRoot))
	if err != nil {
		return idx
	}
	defer f.Close()
	if err := gob.NewDecoder(f).Decode(idx); err != nil {
		return &Index{Files: make(map[string]FileMetadata), Project: idx.Project}
	}
	return idx
}

func saveDocsIndex(repoRoot string, idx *Index) error {
	if err := os.MkdirAll(indexDir(), 0755); err != nil {
		return err
	}
	f, err := os.Create(docsIndexPath(repoRoot))
	if err != nil {
		return err
	}
	defer f.Close()
	return gob.NewEncoder(f).Encode(idx)
}

func buildDocEntries(file string, chunks []DocChunk, embedFn func(string) ([]float32, error)) ([]IndexEntry, error) {
	info, _ := os.Stat(file)
	var ts string
	if info != nil {
		ts = info.ModTime().Format("2006-01-02T15:04:05")
	}
	var entries []IndexEntry
	for _, c := range chunks {
		vec, err := embedFn(c.Heading + "\n" + c.Body)
		if err != nil {
			return nil, err
		}
		preview := c.Body
		if len(preview) > previewLen {
			preview = preview[:previewLen]
		}
		entries = append(entries, IndexEntry{
			Source: "docs", Heading: c.Heading, FilePath: file,
			MsgIndex: c.Ordinal, Role: "doc", Timestamp: ts,
			Preview: preview, Vector: vec,
		})
	}
	return entries, nil
}

// refreshDocsIndex re-embeds only doc files whose mtime is newer than the index.
func refreshDocsIndex(repoRoot string, dirs []string, embedFn func(string) ([]float32, error)) error {
	idx := loadDocsIndex(repoRoot)
	changed := false
	for _, dir := range dirs {
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
				return nil
			}
			if meta, ok := idx.Files[path]; ok && !info.ModTime().After(meta.LastModified) {
				return nil // unchanged
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			entries, err := buildDocEntries(path, chunkMarkdown(data), embedFn)
			if err != nil {
				return nil // skip this file; ollama hiccup shouldn't abort the walk
			}
			idx.Entries = removeEntriesForFile(idx.Entries, path)
			idx.Entries = append(idx.Entries, entries...)
			idx.Files[path] = FileMetadata{FilePath: path, LastModified: info.ModTime()}
			changed = true
			return nil
		})
	}
	if changed {
		return saveDocsIndex(repoRoot, idx)
	}
	return nil
}
```

Add `"encoding/gob"` to `docs.go` imports. (`removeEntriesForFile`, `previewLen`, `encodePath`, `indexDir`, `FileMetadata`, `Index` already exist.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run 'TestBuildDocEntries|TestRefreshDocsIndex' ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add docs.go docs_test.go
git commit -m "feat: per-repo docs gob index with lazy incremental refresh"
```

---

### Task 5: Regex docs lane

**Files:**
- Create: `docs_search.go`
- Test: `docs_search_test.go` (new)

Walks `.md` files, chunks each, regex-matches chunk bodies (and headings), attributes hits to `file § heading`. No index. Capped. `Similarity` stays 0.

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegexDocsSearch(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "vercel.md"),
		[]byte("# Cron auth\nevery handler must guard if !secret\n## Deploy\nuse vx not vercel\n"), 0644)
	os.WriteFile(filepath.Join(dir, "db.md"),
		[]byte("# Neon\nbranch per worktree\n"), 0644)

	docs, err := regexDocsSearch("(?i)guard", []string{dir}, 5)
	if err != nil { t.Fatal(err) }
	if len(docs) != 1 { t.Fatalf("want 1 hit, got %d: %+v", len(docs), docs) }
	if filepath.Base(docs[0].File) != "vercel.md" || docs[0].Heading != "Cron auth" {
		t.Errorf("bad attribution: %+v", docs[0])
	}
}

func TestRegexDocsSearchCap(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.md"),
		[]byte("# A\nx\n## B\nx\n### C\nx\n#### D\nx\n##### E\nx\n###### F\nx\n"), 0644)
	docs, _ := regexDocsSearch("(?i)x", []string{dir}, 3)
	if len(docs) != 3 { t.Fatalf("cap not applied: got %d", len(docs)) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestRegexDocsSearch ./...`
Expected: FAIL — `undefined: regexDocsSearch`.

- [ ] **Step 3: Implement in `docs_search.go`**

```go
package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type DocMatch struct {
	File       string
	Heading    string
	Text       string
	Similarity float32
}

func docsCap(maxResults int) int {
	if maxResults < 5 {
		return maxResults
	}
	return 5
}

func regexDocsSearch(pattern string, dirs []string, cap int) ([]DocMatch, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	var out []DocMatch
	for _, dir := range dirs {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			for _, c := range chunkMarkdown(data) {
				if re.MatchString(c.Heading) || re.MatchString(c.Body) {
					out = append(out, DocMatch{File: path, Heading: c.Heading, Text: c.Body})
					if len(out) >= cap {
						return filepath.SkipAll
					}
				}
			}
			return nil
		})
		if err != nil {
			return out, nil
		}
		if len(out) >= cap {
			break
		}
	}
	return out, nil
}
```

Note: `filepath.SkipAll` requires Go 1.20+ (go.mod is fine; verify with `go version`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestRegexDocsSearch ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add docs_search.go docs_search_test.go
git commit -m "feat: regex docs lane (live .md scan, heading-attributed, capped)"
```

---

### Task 6: Semantic docs lane + dispatcher

**Files:**
- Modify: `docs_search.go` (append)
- Test: `docs_search_test.go` (append)

`semanticDocsSearch` lazily refreshes the index (via real `embed`), embeds the query, ranks chunks by cosine ≥ 0.55, caps. `collectDocs` is the top-level dispatch main calls: discover → pick lane → cap. Both degrade to empty (never error to the user) when ollama/index/repo is missing.

- [ ] **Step 1: Write the failing test**

The embedding call is the only ollama dependency. Test the ranking/cap/threshold against a pre-seeded `.docs.gob` and stub the query embed via a package var seam.

```go
func TestSemanticDocsSearchRanksAndCaps(t *testing.T) {
	home := t.TempDir(); t.Setenv("HOME", home)
	root := "/fake/repo"
	// Seed a docs index directly (bypass ollama).
	idx := &Index{Files: map[string]FileMetadata{}, Project: encodePath(root) + ".docs",
		Entries: []IndexEntry{
			{Source: "docs", Heading: "Cron auth", FilePath: "/r/learnings/vercel.md",
				Preview: "guard", Vector: []float32{1, 0}},
			{Source: "docs", Heading: "Neon", FilePath: "/r/learnings/db.md",
				Preview: "branch", Vector: []float32{0, 1}},
		}}
	if err := saveDocsIndex(root, idx); err != nil { t.Fatal(err) }

	// Stub query embedding to point at the first entry.
	embedQueryFn = func(string) ([]float32, error) { return []float32{1, 0}, nil }
	refreshDocsFn = func(string, []string, func(string) ([]float32, error)) error { return nil }
	defer func() { embedQueryFn = embed; refreshDocsFn = refreshDocsIndex }()

	docs, err := semanticDocsSearch("how to auth cron", root, []string{"/fake/repo/learnings"}, 5)
	if err != nil { t.Fatal(err) }
	if len(docs) == 0 || docs[0].Heading != "Cron auth" {
		t.Fatalf("expected Cron auth ranked first, got %+v", docs)
	}
	if docs[0].Similarity < 0.99 {
		t.Errorf("similarity not computed: %v", docs[0].Similarity)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestSemanticDocsSearch ./...`
Expected: FAIL — `undefined: semanticDocsSearch` / `embedQueryFn`.

- [ ] **Step 3: Implement in `docs_search.go`**

```go
import "sort"

// seams for testing (default to the real functions)
var embedQueryFn = embed
var refreshDocsFn = refreshDocsIndex

func semanticDocsSearch(query, repoRoot string, dirs []string, cap int) ([]DocMatch, error) {
	if ollamaReachable() {
		_ = refreshDocsFn(repoRoot, dirs, embed) // best-effort; ignore errors
	}
	idx := loadDocsIndex(repoRoot)
	if len(idx.Entries) == 0 {
		return nil, nil
	}
	qv, err := embedQueryFn(query)
	if err != nil {
		return nil, err
	}
	type scored struct {
		e   IndexEntry
		sim float32
	}
	var cands []scored
	for _, e := range idx.Entries {
		if e.Source != "docs" {
			continue
		}
		if sim := cosineSimilarity(qv, e.Vector); sim > 0.55 {
			cands = append(cands, scored{e, sim})
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].sim > cands[j].sim })
	if len(cands) > cap {
		cands = cands[:cap]
	}
	var out []DocMatch
	for _, c := range cands {
		out = append(out, DocMatch{
			File: c.e.FilePath, Heading: c.e.Heading, Text: c.e.Preview, Similarity: c.sim,
		})
	}
	return out, nil
}

// collectDocs is the dispatcher main calls. Returns (nil, "none") on any miss
// (not in a repo, no doc dir, lane error) — the docs block is simply absent.
func collectDocs(cwd, pattern string, semantic bool, maxResults int) ([]DocMatch, string) {
	root, dirs, ok := discoverDocsDir(cwd)
	if !ok {
		return nil, "none"
	}
	cap := docsCap(maxResults)
	if semantic {
		docs, err := semanticDocsSearch(pattern, root, dirs, cap)
		if err != nil || len(docs) == 0 {
			return nil, "none"
		}
		return docs, "semantic"
	}
	docs, err := regexDocsSearch("(?i)"+pattern, dirs, cap)
	if err != nil || len(docs) == 0 {
		return nil, "none"
	}
	return docs, "regex"
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestSemanticDocsSearch ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add docs_search.go docs_search_test.go
git commit -m "feat: semantic docs lane + collectDocs dispatcher"
```

---

### Task 7: JSON output — one array, docs tagged

**Files:**
- Modify: `format.go:158-207` (`JSONMatch`, `formatJSON`)
- Create: helper in `docs_format.go`
- Test: `docs_format_test.go` (new)

Add `Source`/`File`/`Heading` (omitempty) to `JSONMatch`. Refactor `formatJSON` so docs can be appended to the same array via `docsToJSON`.

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestDocsToJSONAndFormat(t *testing.T) {
	docs := []DocMatch{{File: "/r/learnings/vercel.md", Heading: "Cron auth", Text: "guard", Similarity: 0.82}}
	jms := docsToJSON(docs)
	if len(jms) != 1 || jms[0].Source != "docs" || jms[0].Heading != "Cron auth" {
		t.Fatalf("bad docsToJSON: %+v", jms)
	}

	// Session entries must omit the doc-only fields.
	var buf bytes.Buffer
	formatJSON([]Match{{Message: Message{SessionID: "s", Role: "user", Text: "hi"}}}, &buf)
	if bytes.Contains(buf.Bytes(), []byte(`"source"`)) {
		t.Error("session JSON should omit source field")
	}

	// Doc entries serialize source/file/heading.
	var out []map[string]any
	b2, _ := json.Marshal(jms)
	json.Unmarshal(b2, &out)
	if out[0]["source"] != "docs" || out[0]["file"] == nil {
		t.Errorf("doc JSON missing fields: %v", out[0])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestDocsToJSON ./...`
Expected: FAIL — `undefined: docsToJSON` / unknown field `Source`.

- [ ] **Step 3a: Extend `JSONMatch` in `format.go`**

```go
type JSONMatch struct {
	Session       string    `json:"session,omitempty"`
	Project       string    `json:"project,omitempty"`
	Timestamp     string    `json:"timestamp,omitempty"`
	Role          string    `json:"role,omitempty"`
	Text          string    `json:"text"`
	Similarity    float32   `json:"similarity,omitempty"`
	Source        string    `json:"source,omitempty"` // "docs" for doc hits
	File          string    `json:"file,omitempty"`
	Heading       string    `json:"heading,omitempty"`
	ContextBefore []JSONCtx `json:"context_before,omitempty"`
	ContextAfter  []JSONCtx `json:"context_after,omitempty"`
}
```

(Session/Project/Timestamp/Role gain `,omitempty` so doc-only rows stay clean — session rows always set them, so output is unchanged for sessions.)

- [ ] **Step 3b: Add `docsToJSON` in `docs_format.go`**

```go
package main

func docsToJSON(docs []DocMatch) []JSONMatch {
	var out []JSONMatch
	for _, d := range docs {
		out = append(out, JSONMatch{
			Source: "docs", File: d.File, Heading: d.Heading,
			Text: d.Text, Similarity: d.Similarity,
		})
	}
	return out
}
```

(`formatJSON` keeps its existing signature; main appends `docsToJSON(docs)` — see Task 9. No refactor of `formatJSON`'s body needed; it already builds `[]JSONMatch` then encodes. To allow appending, change its final lines to a shared encoder — but simplest: main builds the combined slice. See Task 9 Step 3.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestDocsToJSON ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add format.go docs_format.go docs_format_test.go
git commit -m "feat: tag doc hits in JSON output (source/file/heading, omitempty)"
```

---

### Task 8: Terminal docs block

**Files:**
- Modify: `docs_format.go` (append `printDocsBlock`)
- Test: `docs_format_test.go` (append)

Prints the `=== curated docs (<label>) ===` header, then one line per hit `basename § heading [sim]`, with a BM25-compressed body line. Uses the existing `searchQuery` + `bm25Compress` for compression.

- [ ] **Step 1: Write the failing test**

```go
func TestPrintDocsBlock(t *testing.T) {
	out := captureStdout(t, func() {
		searchQuery = "cron auth"
		printDocsBlock("learnings/", []DocMatch{
			{File: "/r/learnings/vercel.md", Heading: "Cron auth",
				Text: "every cron handler must guard if !secret to avoid Bearer undefined bypass", Similarity: 0.82},
		})
	})
	if !strings.Contains(out, "=== curated docs (learnings/) ===") {
		t.Errorf("missing header:\n%s", out)
	}
	if !strings.Contains(out, "vercel.md § Cron auth") {
		t.Errorf("missing file § heading:\n%s", out)
	}
	if !strings.Contains(out, "[0.82]") {
		t.Errorf("missing similarity:\n%s", out)
	}
}

// captureStdout helper (add to docs_format_test.go)
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var b bytes.Buffer
	io.Copy(&b, r)
	return b.String()
}
```

Add imports `os`, `io`, `strings` to the test file.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestPrintDocsBlock ./...`
Expected: FAIL — `undefined: printDocsBlock`.

- [ ] **Step 3: Implement in `docs_format.go`**

```go
import (
	"fmt"
	"path/filepath"
	"strings"
)

// printDocsBlock renders the trailing curated-docs section. label is the
// human dir name (e.g. "learnings/").
func printDocsBlock(label string, docs []DocMatch) {
	if len(docs) == 0 {
		return
	}
	fmt.Printf("\n=== curated docs (%s) ===\n", label)
	for _, d := range docs {
		sim := ""
		if d.Similarity > 0 {
			sim = fmt.Sprintf("   [%.2f]", d.Similarity)
		}
		fmt.Printf("%s § %s%s\n", filepath.Base(d.File), d.Heading, sim)

		body := d.Text
		const budget = 400
		if len(body) > budget {
			if searchQuery != "" {
				body = bm25Compress(body, searchQuery, budget)
			} else {
				body = body[:budget] + "..."
			}
		}
		fmt.Printf("  %s\n", strings.ReplaceAll(strings.TrimSpace(body), "\n", " "))
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestPrintDocsBlock ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add docs_format.go docs_format_test.go
git commit -m "feat: terminal curated-docs block (file § heading + BM25 body)"
```

---

### Task 9: Wire the docs lane into main + telemetry

**Files:**
- Modify: `telemetry.go:14-30` (`UsageEvent`)
- Modify: `format.go:176-207` (extract `buildJSONMatches` + `encodeJSON`)
- Modify: `main.go` (flags + both output paths)
- Test: `docs_search_test.go` (append a `collectDocs` integration test)

- [ ] **Step 1: Write the failing test** (drives the telemetry field + label helper)

```go
func TestDocsLabel(t *testing.T) {
	if got := docsLabel("/r/learnings"); got != "learnings/" {
		t.Errorf("want learnings/, got %q", got)
	}
}

func TestUsageEventHasDocsFields(t *testing.T) {
	ev := UsageEvent{DocsResults: 3, DocsEngine: "semantic"}
	b, _ := json.Marshal(ev)
	if !bytes.Contains(b, []byte(`"docs_results":3`)) || !bytes.Contains(b, []byte(`"docs_engine":"semantic"`)) {
		t.Errorf("docs telemetry not serialized: %s", b)
	}
}
```

Add `encoding/json` + `bytes` to the test imports if absent.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run 'TestDocsLabel|TestUsageEventHasDocsFields' ./...`
Expected: FAIL — `undefined: docsLabel`; unknown field `DocsResults`.

- [ ] **Step 3a: Add telemetry fields** in `telemetry.go` `UsageEvent` (after `RegexSearched`):

```go
	DocsResults   int    `json:"docs_results,omitempty"`
	DocsEngine    string `json:"docs_engine,omitempty"` // "regex"/"semantic"/"none"
```

- [ ] **Step 3b: Add `docsLabel` to `docs_format.go`:**

```go
func docsLabel(dir string) string { return filepath.Base(dir) + "/" }
```

- [ ] **Step 3c: Extract JSON helpers in `format.go`** so docs append to one array. Replace the body of `formatJSON` with:

```go
func formatJSON(matches []Match, w io.Writer) {
	encodeJSON(buildJSONMatches(matches), w)
}

func buildJSONMatches(matches []Match) []JSONMatch {
	var out []JSONMatch
	for _, m := range matches {
		jm := JSONMatch{
			Session: m.Message.SessionID, Project: m.Message.Project,
			Timestamp: m.Message.Timestamp, Role: m.Message.Role,
			Text: m.Message.Text, Similarity: m.Similarity,
		}
		for _, ctx := range m.ContextBefore {
			jm.ContextBefore = append(jm.ContextBefore, JSONCtx{ctx.Timestamp, ctx.Role, ctx.Text})
		}
		for _, ctx := range m.ContextAfter {
			jm.ContextAfter = append(jm.ContextAfter, JSONCtx{ctx.Timestamp, ctx.Role, ctx.Text})
		}
		out = append(out, jm)
	}
	return out
}

func encodeJSON(out []JSONMatch, w io.Writer) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(out)
}
```

(`JSONCtx` is a 3-field struct; positional literal matches its `Timestamp,Role,Text` order.)

- [ ] **Step 3d: Add flags in `main.go`** (after line 38, with the other `flag.Bool`s):

```go
	noDocs := flag.Bool("no-docs", false, "suppress the curated-docs block")
	docsIndex := flag.Bool("docs", false, "with --index: (re)build the cwd repo's docs index")
	benchDocsPath := flag.String("bench-docs", "", "run the labeled docs benchmark over a JSON corpus")
```

- [ ] **Step 3e: Handle `--bench-docs` and `--index --docs`** in `main.go`.

After the `if *benchPath != "" { runBench(...) }` block (line ~98-101), add:

```go
	if *benchDocsPath != "" {
		runDocsBench(*benchDocsPath) // Task 10
		return
	}
```

In the index branch (`if *index {`), before `runIndex(...)`, add:

```go
		if *docsIndex {
			runDocsIndexCmd(*indexStatus) // Task 10 helper; builds cwd repo docs index
			return
		}
```

- [ ] **Step 3f: Compute docs once, after the search path is resolved** (insert after the role filter is known and `searchQuery = pattern` is set, ~line 222). The docs lane uses cwd, ignores `-a`/`-d`, and only runs for `role == "both"`:

```go
	var docMatches []DocMatch
	docsEngine := "none"
	if !*noDocs && role == "both" {
		cwd, _ := os.Getwd()
		docMatches, docsEngine = collectDocs(cwd, pattern, *semantic, *maxResults)
	}
```

- [ ] **Step 3g: Emit docs in BOTH output paths.**

*Semantic path* — replace the existing terminal/JSON emit (lines ~242-249) with:

```go
		if *jsonOut {
			combined := append(buildJSONMatches(matches), docsToJSON(docMatches)...)
			encodeJSON(combined, os.Stdout)
		} else {
			formatTerminal(matches, opts)
			if _, dirs, ok := discoverDocsDir(mustGetwd()); ok {
				printDocsBlock(docsLabel(dirs[0]), docMatches)
			}
		}
```

*Regex path* — replace the final emit (lines ~305-309) with the identical block.

Add a tiny helper near the bottom of `main.go`:

```go
func mustGetwd() string { d, _ := os.Getwd(); return d }
```

- [ ] **Step 3h: Add docs fields to the regex + semantic `logUsage` calls.** In the `case "regex":`, `"semantic"` (fallback), and the top semantic-mode `logUsage`, set:

```go
			DocsResults: len(docMatches), DocsEngine: docsEngine,
```

- [ ] **Step 4: Build + run tests**

Run: `go build ./... && go test -run 'TestDocsLabel|TestUsageEventHasDocsFields|TestCollect' ./...`
Expected: PASS. Then full `go test ./...` — all green.

- [ ] **Step 5: Manual smoke test** (in this repo, which now has `docs/`)

```bash
go build -o claude-grep . && ./claude-grep -a "curated docs" ; echo "exit=$?"
# Expect: session hits (if any) + a "=== curated docs (docs/) ===" block.
./claude-grep --json -a "docs" | jq 'map(select(.source=="docs")) | length'
```

- [ ] **Step 6: Commit**

```bash
git add main.go format.go telemetry.go docs_format.go docs_search_test.go
git commit -m "feat: wire curated-docs lane into search output + telemetry"
```

---

### Task 10: Labeled benchmark + `--index --docs` command

**Files:**
- Create: `docs_bench.go`
- Test: `docs_bench_test.go` (new)

Three engines over one labeled corpus. `grep` and `cg-regex` receive the **OR-of-words** form of the query (the realistic keyword invocation); `cg-semantic` receives the raw NL query. `EngineResult.HitRank`: 0 = miss, ≥1 = 1-based rank of `expect_file`, -1 = engine skipped (no ollama). The verdict gate is **cg-semantic hit@3 ≥ grep hit@any**.

- [ ] **Step 1: Write the failing test** (verdict math + grep/cg-regex engines, deterministic, no ollama)

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOrWords(t *testing.T) {
	if got := orWords("how do we authenticate cron handlers"); got != "authenticate|cron|handlers" {
		t.Errorf("orWords dropped/kept wrong tokens: %q", got)
	}
}

func TestBenchVerdict(t *testing.T) {
	// semantic hit@3 (2/2) >= grep hit@any (1/2) → pass
	recs := []DocsBenchRecord{
		{Grep: EngineResult{HitRank: 1}, CgSemantic: EngineResult{HitRank: 2}},
		{Grep: EngineResult{HitRank: 0}, CgSemantic: EngineResult{HitRank: 1}},
	}
	if ok, _ := benchVerdict(recs); !ok {
		t.Error("expected pass: semantic >= grep")
	}
	// semantic worse than grep → fail
	bad := []DocsBenchRecord{
		{Grep: EngineResult{HitRank: 1}, CgSemantic: EngineResult{HitRank: 0}},
		{Grep: EngineResult{HitRank: 1}, CgSemantic: EngineResult{HitRank: 0}},
	}
	if ok, _ := benchVerdict(bad); ok {
		t.Error("expected fail: semantic < grep")
	}
	// semantic skipped (-1) → cannot judge → pass (don't block CI without ollama)
	skip := []DocsBenchRecord{{Grep: EngineResult{HitRank: 1}, CgSemantic: EngineResult{HitRank: -1}}}
	if ok, _ := benchVerdict(skip); !ok {
		t.Error("skipped semantic should not fail the gate")
	}
}

func TestRunDocsBenchRecordsRegexEngine(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "vercel.md"),
		[]byte("# Cron auth\nguard if !secret\n"), 0644)
	corpus := filepath.Join(dir, "q.json")
	os.WriteFile(corpus, []byte(`[{"query":"guard secret cron","expect_file":"vercel.md"}]`), 0644)

	// stub semantic so the test is ollama-free
	semanticDocsBenchFn = func(string, string, []string, int) ([]DocMatch, error) { return nil, errSkip }
	defer func() { semanticDocsBenchFn = nil }()

	recs := runDocsBenchRecords(corpus, dir, []string{dir})
	if len(recs) != 1 { t.Fatalf("want 1 record, got %d", len(recs)) }
	if recs[0].CgRegex.HitRank < 1 { t.Errorf("cg-regex should hit vercel.md: %+v", recs[0].CgRegex) }
	if recs[0].CgSemantic.HitRank != -1 { t.Errorf("semantic should be skipped: %+v", recs[0].CgSemantic) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run 'TestOrWords|TestBenchVerdict|TestRunDocsBenchRecords' ./...`
Expected: FAIL — `undefined: orWords`.

- [ ] **Step 3: Implement `docs_bench.go`**

```go
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

var errSkip = errors.New("engine skipped")

// seam: lets tests stub the semantic engine (ollama-free).
var semanticDocsBenchFn func(query, root string, dirs []string, cap int) ([]DocMatch, error)

// orWords builds grep's realistic "term1|term2" form: lowercase tokens >=3 chars.
func orWords(query string) string {
	var toks []string
	for _, w := range strings.Fields(strings.ToLower(query)) {
		w = strings.Trim(w, ".,?!:;\"'()")
		if len(w) >= 3 {
			toks = append(toks, w)
		}
	}
	return strings.Join(toks, "|")
}

func grepEngine(query string, dirs []string) EngineResult {
	pat := orWords(query)
	if pat == "" {
		return EngineResult{}
	}
	args := append([]string{"-rIliE", pat}, dirs...)
	out, _ := exec.Command("grep", args...).Output() // exit 1 (no match) is fine
	files := map[string]bool{}
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if ln != "" {
			files[filepath.Base(ln)] = true
		}
	}
	return EngineResult{Files: len(files), HitRank: 0} // HitRank set by caller vs expect
}

func rankOf(expect string, docs []DocMatch) (rank, files int) {
	seen := map[string]bool{}
	for i, d := range docs {
		b := filepath.Base(d.File)
		seen[b] = true
		if rank == 0 && b == expect {
			rank = i + 1
		}
	}
	return rank, len(seen)
}

func runDocsBenchRecords(corpusPath, root string, dirs []string) []DocsBenchRecord {
	data, err := os.ReadFile(corpusPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bench: cannot read %s: %v\n", corpusPath, err)
		os.Exit(2)
	}
	var queries []DocsBenchQuery
	if err := json.Unmarshal(data, &queries); err != nil {
		fmt.Fprintf(os.Stderr, "bench: %s must be a JSON array of {query,expect_file}: %v\n", corpusPath, err)
		os.Exit(2)
	}

	semFn := semanticDocsBenchFn
	if semFn == nil {
		semFn = func(q, r string, d []string, c int) ([]DocMatch, error) {
			if !ollamaReachable() {
				return nil, errSkip
			}
			return semanticDocsSearch(q, r, d, c)
		}
	}

	recs := make([]DocsBenchRecord, 0, len(queries))
	for _, q := range queries {
		rec := DocsBenchRecord{Query: q.Query, ExpectFile: q.ExpectFile}

		g := grepEngine(q.Query, dirs)
		// grep is unranked: HitRank=1 if expect appears at all.
		gout, _ := exec.Command("grep", append([]string{"-rIliE", orWords(q.Query)}, dirs...)...).Output()
		if strings.Contains(strings.ToLower(string(gout)), strings.ToLower(q.ExpectFile)) {
			g.HitRank = 1
		}
		rec.Grep = g

		rdocs, _ := regexDocsSearch("(?i)"+orWords(q.Query), dirs, 100)
		rrank, rfiles := rankOf(q.ExpectFile, rdocs)
		rec.CgRegex = EngineResult{HitRank: rrank, Files: rfiles}

		sdocs, serr := semFn(q.Query, root, dirs, 100)
		if serr == errSkip {
			rec.CgSemantic = EngineResult{HitRank: -1}
		} else {
			srank, sfiles := rankOf(q.ExpectFile, sdocs)
			rec.CgSemantic = EngineResult{HitRank: srank, Files: sfiles}
		}
		recs = append(recs, rec)
	}
	return recs
}

// benchVerdict gates CI: cg-semantic hit@3 >= grep hit@any. Skipped semantic
// (all -1) cannot be judged → pass (don't block CI without ollama).
func benchVerdict(recs []DocsBenchRecord) (bool, string) {
	var grepAny, semAt3, judged int
	for _, r := range recs {
		if r.Grep.HitRank >= 1 {
			grepAny++
		}
		if r.CgSemantic.HitRank == -1 {
			continue
		}
		judged++
		if r.CgSemantic.HitRank >= 1 && r.CgSemantic.HitRank <= 3 {
			semAt3++
		}
	}
	if judged == 0 {
		return true, "semantic skipped (no ollama) — gate not evaluated"
	}
	ok := semAt3 >= grepAny
	return ok, fmt.Sprintf("cg-semantic hit@3=%d vs grep hit@any=%d", semAt3, grepAny)
}

func runDocsBench(corpusPath string) {
	cwd, _ := os.Getwd()
	root, dirs, ok := discoverDocsDir(cwd)
	if !ok {
		fmt.Fprintln(os.Stderr, "bench: no learnings/ or docs/ dir in this repo")
		os.Exit(2)
	}
	recs := runDocsBenchRecords(corpusPath, root, dirs)

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(recs)

	// Summary → stderr
	n := len(recs)
	var grepAny, grepFiles, rAt1, rAt3, sAt1, sAt3, judged int
	var mrr float64
	for _, r := range recs {
		if r.Grep.HitRank >= 1 {
			grepAny++
		}
		grepFiles += r.Grep.Files
		if r.CgRegex.HitRank == 1 {
			rAt1++
		}
		if r.CgRegex.HitRank >= 1 && r.CgRegex.HitRank <= 3 {
			rAt3++
		}
		if r.CgSemantic.HitRank != -1 {
			judged++
			if r.CgSemantic.HitRank == 1 {
				sAt1++
			}
			if r.CgSemantic.HitRank >= 1 && r.CgSemantic.HitRank <= 3 {
				sAt3++
			}
			if r.CgSemantic.HitRank >= 1 {
				mrr += 1.0 / float64(r.CgSemantic.HitRank)
			}
		}
	}
	avgFiles := 0.0
	if n > 0 {
		avgFiles = float64(grepFiles) / float64(n)
	}
	semMRR := 0.0
	if judged > 0 {
		semMRR = mrr / float64(judged)
	}
	fmt.Fprintf(os.Stderr,
		"docs-bench: grep hit@any %d/%d (avg %.1f files) | cg-regex hit@3 %d/%d | cg-semantic hit@3 %d/%d mrr %.2f\n",
		grepAny, n, avgFiles, rAt3, n, sAt3, judged, semMRR)
	_ = rAt1
	_ = sAt1

	pass, msg := benchVerdict(recs)
	if !pass {
		fmt.Fprintf(os.Stderr, "FAIL: %s — docs lane is WORSE than grep.\n", msg)
		fmt.Fprintln(os.Stderr, "  fix: rebuild index (claude-grep --index --docs) or relabel bench/docs-queries.json")
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "PASS: %s\n", msg)
}

// runDocsIndexCmd handles `--index --docs [--status]`.
func runDocsIndexCmd(status bool) {
	cwd, _ := os.Getwd()
	root, dirs, ok := discoverDocsDir(cwd)
	if !ok {
		fmt.Fprintln(os.Stderr, "no learnings/ or docs/ dir in this repo")
		os.Exit(1)
	}
	if status {
		idx := loadDocsIndex(root)
		fmt.Printf("docs index: %s\nfiles: %d\nchunks: %d\n", docsIndexPath(root), len(idx.Files), len(idx.Entries))
		return
	}
	if !ollamaReachable() {
		fmt.Fprintln(os.Stderr, "error: ollama not running — start with: ollama serve")
		os.Exit(2)
	}
	if err := refreshDocsIndex(root, dirs, embed); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	idx := loadDocsIndex(root)
	fmt.Fprintf(os.Stderr, "docs indexed: %d files, %d chunks\n", len(idx.Files), len(idx.Entries))
}

// sort import kept for future ranking tweaks; remove if unused.
var _ = sort.Ints
```

> If `go vet` flags the unused `sort`/`rAt1`/`sAt1`, delete those lines — they're scaffolding for a richer summary and not load-bearing.

- [ ] **Step 4: Run tests**

Run: `go test -run 'TestOrWords|TestBenchVerdict|TestRunDocsBenchRecords' ./... && go build ./...`
Expected: PASS, build clean.

- [ ] **Step 5: Commit**

```bash
git add docs_bench.go docs_bench_test.go
git commit -m "feat: labeled docs benchmark (grep vs cg-regex vs cg-semantic) + CI verdict"
```

---

### Task 11: Gold corpus, docs, and full verification

**Files:**
- Create: `bench/docs-queries.json`
- Modify: `README.md`
- Test: full suite + live bench

- [ ] **Step 1: Write the gold corpus** `bench/docs-queries.json`

Seed ~30 entries from this repo's own `docs/` headings (use real headings; `expect_file` is the basename). Start with these, then expand to ~30 by skimming `docs/specs/*` and `learnings/`-style files if present:

```json
[
  {"query": "how do we authenticate cron handlers", "expect_file": "2026-06-02-docs-search-design.md", "expect_heading": "Search lanes"},
  {"query": "where does the docs index live on disk", "expect_file": "2026-06-02-docs-search-design.md", "expect_heading": "Indexing strategy — no cron, self-healing"},
  {"query": "recovery ladder regex tokenized semantic", "expect_file": "2026-06-02-ax-recovery-design.md", "expect_heading": ""}
]
```

> For a richer corpus, run this in the repo to list candidate headings, then hand-write NL queries that should map to each:
> `grep -rn '^#\{1,3\} ' docs/ | sed 's/:[0-9]*:/ /' | head -40`

- [ ] **Step 2: Run the live benchmark** (requires ollama for the semantic engine)

```bash
ollama serve >/dev/null 2>&1 &   # if not already running
go build -o claude-grep .
./claude-grep --index --docs                 # build this repo's docs index
./claude-grep --bench-docs bench/docs-queries.json > bench/docs-after.json
echo "exit=$?"   # 0 = semantic not worse than grep
```

Expected stderr: `docs-bench: grep hit@any N/30 … | cg-semantic hit@3 M/30 mrr 0.xx` then `PASS: …`.

- [ ] **Step 3: Capture the grep baseline** for the quotable pre/post pair

```bash
# baseline = same corpus, grep column only (already in docs-after.json records);
# extract for a clean artifact:
jq 'map({query, expect_file, grep: .Grep})' bench/docs-after.json > bench/docs-baseline.json
```

- [ ] **Step 4: Update `README.md`** — add a "Curated docs" subsection under Usage:

```markdown
### Curated docs

Searches also surface a repo's curated docs (`learnings/` or `docs/`):

    claude-grep "cron auth"          # sessions + a "=== curated docs ===" block
    claude-grep -s "cold start"      # semantic doc hits (section-level)
    claude-grep --no-docs "x"        # sessions only
    claude-grep --index --docs       # build/refresh this repo's docs index
    claude-grep --bench-docs bench/docs-queries.json   # grep-vs-semantic benchmark

Docs come from the current repo (`learnings/` then `docs/`, or `CLAUDE_GREP_DOCS`),
ignore `-d`/`-a`, and the semantic index self-heals on use (no cron).
```

- [ ] **Step 5: Full suite + race**

Run: `go test ./... && go vet ./... && go build ./...`
Expected: all PASS, no vet warnings.

- [ ] **Step 6: Commit**

```bash
git add bench/docs-queries.json bench/docs-after.json bench/docs-baseline.json README.md
git commit -m "feat: gold docs-bench corpus + README; capture grep-vs-semantic baseline"
```

---

## Self-Review

- **Spec coverage:** discovery (T2), chunking (T3), index+self-heal (T4), regex lane (T5), semantic lane + dispatch (T6), JSON one-array (T7), terminal block (T8), flags+telemetry+wiring (T9), labeled bench + verdict + `--index --docs` (T10), corpus+README+verification (T11). All spec sections map to a task.
- **Out of scope (deliberate):** the user's global `CLAUDE.md` relaxation of the manual `grep learnings/` rule is a doc edit in a *different* repo — do it in a follow-up, not this branch.
- **Cross-task signatures:** `runDocsIndexCmd(status bool)` (T10) matches the call wired in T9 step 3e. `collectDocs` returns `([]DocMatch, string)` — used in T9 step 3f. `formatJSON` keeps its signature; `buildJSONMatches`/`encodeJSON` extracted in T9 step 3c are used by the combined-array emit in T9 step 3g.

## Execution Handoff

Plan complete and saved to `docs/plans/2026-06-02-docs-search.md`. Two execution options:

1. **Subagent-Driven (recommended)** — fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — execute tasks in this session with checkpoints.

Which approach?
