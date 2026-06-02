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
	// grep saturates (returns 4 files → eff-RR 0.4); semantic ranks #1/#2 (MRR 0.75) → pass
	recs := []DocsBenchRecord{
		{Grep: EngineResult{HitRank: 1, Files: 4}, CgSemantic: EngineResult{HitRank: 1}},
		{Grep: EngineResult{HitRank: 1, Files: 4}, CgSemantic: EngineResult{HitRank: 2}},
	}
	if ok, msg := benchVerdict(recs); !ok {
		t.Errorf("expected pass (semantic ranks better than saturated grep): %s", msg)
	}
	// grep precise (1 file, eff-RR 1.0); semantic misses → semantic genuinely worse → fail
	bad := []DocsBenchRecord{
		{Grep: EngineResult{HitRank: 1, Files: 1}, CgSemantic: EngineResult{HitRank: 0}},
		{Grep: EngineResult{HitRank: 1, Files: 1}, CgSemantic: EngineResult{HitRank: 0}},
	}
	if ok, _ := benchVerdict(bad); ok {
		t.Error("expected fail: semantic MRR 0 < grep eff-MRR 1.0")
	}
	// semantic skipped (-1) → cannot judge → pass (don't block CI without ollama)
	skip := []DocsBenchRecord{{Grep: EngineResult{HitRank: 1, Files: 2}, CgSemantic: EngineResult{HitRank: -1}}}
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
	if len(recs) != 1 {
		t.Fatalf("want 1 record, got %d", len(recs))
	}
	if recs[0].CgRegex.HitRank < 1 {
		t.Errorf("cg-regex should hit vercel.md: %+v", recs[0].CgRegex)
	}
	if recs[0].CgSemantic.HitRank != -1 {
		t.Errorf("semantic should be skipped: %+v", recs[0].CgSemantic)
	}
}
