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

func TestSemanticDocsSearchRanksAndCaps(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := "/fake/repo"
	idx := &Index{Files: map[string]FileMetadata{}, Project: encodePath(root) + ".docs",
		Entries: []IndexEntry{
			{Source: "docs", Heading: "Cron auth", FilePath: "/r/learnings/vercel.md",
				Preview: "guard", Vector: []float32{1, 0}},
			{Source: "docs", Heading: "Neon", FilePath: "/r/learnings/db.md",
				Preview: "branch", Vector: []float32{0, 1}},
		}}
	if err := saveDocsIndex(root, idx); err != nil {
		t.Fatal(err)
	}

	embedQueryFn = func(string) ([]float32, error) { return []float32{1, 0}, nil }
	refreshDocsFn = func(string, []string, func(string) ([]float32, error)) error { return nil }
	defer func() { embedQueryFn = embed; refreshDocsFn = refreshDocsIndex }()

	docs, err := semanticDocsSearch("how to auth cron", root, []string{"/fake/repo/learnings"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) == 0 || docs[0].Heading != "Cron auth" {
		t.Fatalf("expected Cron auth ranked first, got %+v", docs)
	}
	if docs[0].Similarity < 0.99 {
		t.Errorf("similarity not computed: %v", docs[0].Similarity)
	}
}

func TestCollectDocsNotARepo(t *testing.T) {
	dir := t.TempDir() // no git
	docs, engine := collectDocs(dir, "anything", false, 100)
	if len(docs) != 0 || engine != "none" {
		t.Errorf("expected no docs outside a repo, got %d/%q", len(docs), engine)
	}
}

func TestLexicalDocsSearch(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "stripe.md"),
		[]byte("# Design partner\ncheck subscriptionPlan equals Design Partner never infer from null fields\n"), 0644)
	os.WriteFile(filepath.Join(dir, "neon.md"),
		[]byte("# Neon\nbranch per worktree database endpoint\n"), 0644)

	docs := lexicalDocsSearch("how to check a design partner", []string{dir}, 5)
	if len(docs) == 0 || filepath.Base(docs[0].File) != "stripe.md" {
		t.Fatalf("expected stripe.md ranked first (exact-term lexical), got %+v", docs)
	}
}

func TestFuseRRF(t *testing.T) {
	dense := []DocMatch{{File: "a.md", Heading: "A"}, {File: "b.md", Heading: "B"}}
	lexical := []DocMatch{{File: "b.md", Heading: "B"}, {File: "c.md", Heading: "C"}}
	out := fuseRRF(dense, lexical, 5)
	if len(out) != 3 {
		t.Fatalf("want 3 unique fused, got %d: %+v", len(out), out)
	}
	if out[0].File != "b.md" {
		t.Errorf("b.md ranks #1 (present in both lanes), got %s", out[0].File)
	}
}
