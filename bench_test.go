package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
