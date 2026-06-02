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

// keep imports used until later tasks add their tests
var _ = strings.TrimSpace
var _ = time.Now
