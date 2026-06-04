package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTrackedDocFilesDiscovery is the mechanical guard for the README/CLAUDE/MEMORY
// expansion: git-tracked root + nested docs become searchable, while a learnings/
// TOC README and untracked files stay excluded.
func TestTrackedDocFilesDiscovery(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v (%s)", err, out)
		}
	}
	git("init")

	write := func(rel, body string) {
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "# Root\nrootreadmemarker content\n")
	write("CLAUDE.md", "# Root rules\nrootclaudemarker content\n")
	write("MEMORY.md", "# Mem\nrootmemorymarker content\n")
	write("app/sub/CLAUDE.md", "# Sub\nnestedclaudemarker content\n")
	write("learnings/benchmarks.md", "# Bench\nrealcontentmarker here\n")
	write("learnings/README.md", "# TOC\ntocmarker do not surface\n") // index, must stay excluded
	write("vendor/lib/README.md", "# Vendor\nvendormarker\n")          // left untracked on purpose
	// Track everything except the vendor README.
	git("add", "README.md", "CLAUDE.md", "MEMORY.md", "app/sub/CLAUDE.md",
		"learnings/benchmarks.md", "learnings/README.md")

	_, dirs, ok := discoverDocsDir(repo)
	if !ok {
		t.Fatal("discovery failed")
	}
	hasSuffix := func(suf string) bool {
		for _, d := range dirs {
			if strings.HasSuffix(d, suf) {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"/learnings", "/README.md", "/CLAUDE.md", "/MEMORY.md", "app/sub/CLAUDE.md"} {
		if !hasSuffix(want) {
			t.Errorf("expected %q in dirs, got %v", want, dirs)
		}
	}
	for _, d := range dirs {
		if strings.HasSuffix(d, "learnings/README.md") {
			t.Errorf("learnings/README.md (TOC) must stay excluded, got %v", dirs)
		}
		if strings.Contains(d, "vendor/") {
			t.Errorf("untracked vendor README leaked: %v", dirs)
		}
	}

	// End-to-end through the regex lane: explicit docs are findable; the TOC is not.
	find := func(marker string) []DocMatch {
		d, _ := collectDocs(repo, marker, false, 5)
		return d
	}
	if d := find("rootclaudemarker"); len(d) != 1 || filepath.Base(d[0].File) != "CLAUDE.md" {
		t.Errorf("root CLAUDE.md not searchable: %+v", d)
	}
	if d := find("rootreadmemarker"); len(d) != 1 || filepath.Base(d[0].File) != "README.md" {
		t.Errorf("root README.md not searchable: %+v", d)
	}
	if d := find("rootmemorymarker"); len(d) != 1 || filepath.Base(d[0].File) != "MEMORY.md" {
		t.Errorf("root MEMORY.md not searchable: %+v", d)
	}
	if d := find("nestedclaudemarker"); len(d) != 1 {
		t.Errorf("nested CLAUDE.md not searchable: %+v", d)
	}
	if d := find("tocmarker"); len(d) != 0 {
		t.Errorf("learnings/README.md TOC leaked into results: %+v", d)
	}
}

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
