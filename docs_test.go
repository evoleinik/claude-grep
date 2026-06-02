package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if err := os.MkdirAll(indexDir(), 0755); err != nil {
		t.Fatal(err)
	}
	if err := saveIndex(idx); err != nil {
		t.Fatal(err)
	}
	got := loadIndex("x.docs")
	if len(got.Entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got.Entries))
	}
	e := got.Entries[0]
	if e.Source != "docs" || e.Heading != "Cron auth" {
		t.Errorf("doc fields lost: Source=%q Heading=%q", e.Source, e.Heading)
	}
}

// --- helpers shared by docs tests ---

func mustInitGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := exec.Command("git", "init", "-q", dir).Run(); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
}

func resolveSymlinks(p string) string { r, _ := filepath.EvalSymlinks(p); return r }

func TestDiscoverDocsDir(t *testing.T) {
	root := t.TempDir()
	mustInitGitRepo(t, root)
	os.MkdirAll(filepath.Join(root, "learnings"), 0755)
	os.MkdirAll(filepath.Join(root, "docs"), 0755)

	// Default: learnings/ wins over docs/
	gotRoot, dirs, ok := discoverDocsDir(root)
	if !ok || gotRoot != resolveSymlinks(root) {
		t.Fatalf("ok=%v root=%q want=%q", ok, gotRoot, resolveSymlinks(root))
	}
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

func TestChunkMarkdown(t *testing.T) {
	md := []byte("intro line\n\n# Title\nbody a\n\n## Cron auth\nguard if !secret\n### nested\ndeep\n")
	chunks := chunkMarkdown(md)
	if len(chunks) != 4 { // (intro), Title, Cron auth, nested
		t.Fatalf("want 4 chunks, got %d: %+v", len(chunks), chunks)
	}
	if chunks[0].Heading != "(intro)" || !strings.Contains(chunks[0].Body, "intro line") {
		t.Errorf("intro chunk wrong: %+v", chunks[0])
	}
	if chunks[2].Heading != "Title › Cron auth" || !strings.Contains(chunks[2].Body, "guard if !secret") {
		t.Errorf("cron chunk breadcrumb wrong: %+v", chunks[2])
	}
	if chunks[3].Heading != "Title › Cron auth › nested" {
		t.Errorf("nested chunk breadcrumb wrong: %+v", chunks[3])
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

func TestBuildDocEntries(t *testing.T) {
	fake := func(s string) ([]float32, error) { return []float32{float32(len(s))}, nil }
	chunks := []DocChunk{{Heading: "Cron auth", Body: "guard if !secret", Ordinal: 0}}
	entries, err := buildDocEntries("/r/learnings/vercel.md", chunks, fake)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1, got %d", len(entries))
	}
	e := entries[0]
	if e.Source != "docs" || e.Heading != "Cron auth" || e.FilePath != "/r/learnings/vercel.md" {
		t.Errorf("bad entry: %+v", e)
	}
	if e.Role != "doc" || len(e.Vector) != 1 {
		t.Errorf("bad entry meta: %+v", e)
	}

	// Full chunk body is stored (not truncated to a 200-char preview), so dense
	// hits can be BM25-compressed around the query at display time.
	long := strings.Repeat("x", 500)
	le, err := buildDocEntries("/r/x.md", []DocChunk{{Heading: "H", Body: long, Ordinal: 0}}, fake)
	if err != nil {
		t.Fatal(err)
	}
	if len(le[0].Preview) != 500 {
		t.Errorf("expected full 500-char body stored, got %d", len(le[0].Preview))
	}
}

func TestRefreshDocsIndexIncremental(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	ldir := filepath.Join(root, "learnings")
	os.MkdirAll(ldir, 0755)
	f := filepath.Join(ldir, "a.md")
	os.WriteFile(f, []byte("# H\nalpha beta\n"), 0644)

	calls := 0
	fake := func(s string) ([]float32, error) { calls++; return []float32{1}, nil }

	if err := refreshDocsIndex(root, []string{ldir}, fake); err != nil {
		t.Fatal(err)
	}
	first := calls
	if first == 0 {
		t.Fatal("expected embeds on first build")
	}

	// Unchanged file → no re-embed
	if err := refreshDocsIndex(root, []string{ldir}, fake); err != nil {
		t.Fatal(err)
	}
	if calls != first {
		t.Errorf("re-embedded unchanged file: %d -> %d", first, calls)
	}

	// Touch file → re-embed
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(f, []byte("# H\nalpha beta gamma\n"), 0644)
	if err := refreshDocsIndex(root, []string{ldir}, fake); err != nil {
		t.Fatal(err)
	}
	if calls == first {
		t.Error("expected re-embed after file change")
	}

	idx := loadDocsIndex(root)
	if len(idx.Entries) == 0 {
		t.Error("index empty after refresh")
	}
}

func TestRefreshDocsIndexVersionBump(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	ldir := filepath.Join(root, "learnings")
	os.MkdirAll(ldir, 0755)
	os.WriteFile(filepath.Join(ldir, "a.md"), []byte("# H\nalpha\n"), 0644)

	calls := 0
	fake := func(s string) ([]float32, error) { calls++; return []float32{1}, nil }

	if err := refreshDocsIndex(root, []string{ldir}, fake); err != nil {
		t.Fatal(err)
	}
	first := calls

	// Simulate a gob written by an older embed-logic version (mtime unchanged).
	idx := loadDocsIndex(root)
	idx.DocEmbedVersion = "v0-old"
	if err := saveDocsIndex(root, idx); err != nil {
		t.Fatal(err)
	}

	// Version mismatch must force a full re-embed despite unchanged mtimes.
	if err := refreshDocsIndex(root, []string{ldir}, fake); err != nil {
		t.Fatal(err)
	}
	if calls == first {
		t.Error("version mismatch should force re-embed even when mtime unchanged")
	}
}
