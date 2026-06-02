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

// keep imports used until later tasks add their tests
var _ = time.Now
