package main

import "testing"

func TestSearchWithRecoveryEscalatesToTokenized(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "a.jsonl", "2026-06-01T10:00:00", "assistant",
		"discussion of ucp manifest plus jsonld presence and product identifiers")

	// Literal phrase matches nothing; recovery should escalate to tokenized.
	matches, _, layer, err := searchWithRecovery(
		"ucp manifest jsonld presence", dir,
		SearchOpts{Role: "both", MaxResults: 100, MaxDays: 3650}, false /* allowSemantic */)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if layer != "tokenized" {
		t.Fatalf("expected layer=tokenized, got %q", layer)
	}
	if len(matches) == 0 {
		t.Fatal("expected tokenized recovery to surface the session")
	}
}

func TestSearchWithRecoveryDirectRegexHit(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "a.jsonl", "2026-06-01T10:00:00", "user", "the worktree alias")
	matches, _, layer, err := searchWithRecovery(
		"worktree", dir, SearchOpts{Role: "both", MaxResults: 100, MaxDays: 3650}, false)
	if err != nil || layer != "regex" || len(matches) == 0 {
		t.Fatalf("expected direct regex hit, got layer=%q matches=%d err=%v", layer, len(matches), err)
	}
}
