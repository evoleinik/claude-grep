package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunBenchProducesRecords(t *testing.T) {
	dir := t.TempDir()
	// A session that the phrase query can only reach via tokenized recovery.
	writeSession(t, dir, "a.jsonl", "2026-06-01T10:00:00", "assistant",
		"notes on ucp manifest and jsonld presence")
	corpus := filepath.Join(dir, "queries.json")
	body, _ := json.Marshal([]string{"ucp manifest jsonld presence", "nosuchtokenzzz"})
	os.WriteFile(corpus, body, 0644)

	recs := runBenchRecords(corpus, dir) // testable core (no stdout/exit)
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	if !recs[0].Found || recs[0].Layer != "tokenized" {
		t.Errorf("query 0 expected found/tokenized, got %+v", recs[0])
	}
	if recs[1].Found {
		t.Errorf("query 1 expected not-found, got %+v", recs[1])
	}
}

// writeBenchCorpus marshals rows (any shape) to a temp corpus file.
func writeBenchCorpus(t *testing.T, dir string, rows any) string {
	t.Helper()
	p := filepath.Join(dir, "corpus.json")
	body, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, body, 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseBenchCorpusAcceptsBothForms(t *testing.T) {
	dir := t.TempDir()

	legacy := parseBenchCorpus(writeBenchCorpus(t, dir, []string{"alpha", "beta"}))
	if len(legacy) != 2 || legacy[0].Query != "alpha" || legacy[0].labeled() {
		t.Fatalf("legacy string corpus mis-parsed: %+v", legacy)
	}

	sub := t.TempDir()
	labeled := parseBenchCorpus(writeBenchCorpus(t, sub, []BenchQuery{
		{Query: "alpha", ExpectSession: "abc123"},
	}))
	if len(labeled) != 1 || !labeled[0].labeled() || labeled[0].ExpectSession != "abc123" {
		t.Fatalf("labeled corpus mis-parsed: %+v", labeled)
	}
}

// The regression the old bench could not see: a query returns results (Found)
// while the session it was supposed to recover is absent. Found says true;
// hit_rank must say 0.
func TestBenchScoresRankNotJustFound(t *testing.T) {
	dir := t.TempDir()
	ts := time.Now().AddDate(0, 0, -2).Format("2006-01-02T15:04:05")
	writeSession(t, dir, "target00-1111-2222-3333-444444444444.jsonl", ts, "assistant",
		"the vertex endpoint kept dedicatedResources allocated while idle")
	writeSession(t, dir, "decoy000-1111-2222-3333-444444444444.jsonl", ts, "assistant",
		"unrelated notes about shipping labels and printers")

	corpus := writeBenchCorpus(t, dir, []BenchQuery{
		// Recovers the target session.
		{Query: "vertex dedicatedResources idle", ExpectSession: "target00"},
		// Matches the decoy, so results come back, but the labeled session is absent.
		{Query: "shipping labels printers", ExpectSession: "target00"},
	})

	recs := runBenchRecords(corpus, dir)
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	if recs[0].HitRank == nil || *recs[0].HitRank != 1 {
		t.Errorf("hit: want rank 1, got %v (layer %s)", recs[0].HitRank, recs[0].Layer)
	}
	if !recs[1].Found {
		t.Fatalf("precondition: query 1 should still return the decoy")
	}
	if recs[1].HitRank == nil || *recs[1].HitRank != 0 {
		t.Errorf("false green: found=true but wrong session — want rank 0, got %v", recs[1].HitRank)
	}
}

func TestSessionBenchVerdictRedAndGreen(t *testing.T) {
	r := func(hit, base int) BenchRecord {
		return BenchRecord{HitRank: &hit, BaseRank: &base}
	}
	if pass, msg := sessionBenchVerdict([]BenchRecord{r(0, 3), r(0, 5)}); pass {
		t.Errorf("ladder misses where regex hits — want FAIL, got pass (%s)", msg)
	}
	if pass, _ := sessionBenchVerdict([]BenchRecord{r(1, 3), r(2, 0)}); !pass {
		t.Errorf("ladder beats regex — want PASS, got fail")
	}
	// Unlabeled corpora must never gate.
	if pass, _ := sessionBenchVerdict([]BenchRecord{{Query: "x", Found: true}}); !pass {
		t.Errorf("unlabeled corpus must not be judged")
	}
}

func TestValidateLabelsCatchesRot(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "live0000-1111-2222-3333-444444444444.jsonl",
		time.Now().Format("2006-01-02T15:04:05"), "assistant", "a real session")

	bad := validateLabels([]BenchQuery{
		{Query: "ok", ExpectSession: "live0000"},
		{Query: "rotted", ExpectSession: "gone0000"},
		{Query: "unlabeled has nothing to validate"},
	}, dir)

	if len(bad) != 1 || !strings.Contains(bad[0], "gone0000") {
		t.Fatalf("expected exactly the rotted label, got %v", bad)
	}
}
