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
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("want 1 hit, got %d: %+v", len(docs), docs)
	}
	if filepath.Base(docs[0].File) != "vercel.md" || docs[0].Heading != "Cron auth" {
		t.Errorf("bad attribution: %+v", docs[0])
	}
}

func TestRegexDocsSearchCap(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.md"),
		[]byte("# A\nx\n## B\nx\n### C\nx\n#### D\nx\n##### E\nx\n###### F\nx\n"), 0644)
	docs, _ := regexDocsSearch("(?i)x", []string{dir}, 3)
	if len(docs) != 3 {
		t.Fatalf("cap not applied: got %d", len(docs))
	}
}
