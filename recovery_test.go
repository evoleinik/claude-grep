package main

import (
	"reflect"
	"testing"
	"time"
)

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

// stubSemantic swaps the ladder's Ollama seams for the duration of a test.
func stubSemantic(t *testing.T, up bool, ret []Match) *int {
	t.Helper()
	calls := 0
	oldReach, oldSem := ollamaReachableFn, semanticSearchFn
	ollamaReachableFn = func() bool { return up }
	semanticSearchFn = func(string, string, SearchOpts) ([]Match, error) {
		calls++
		return ret, nil
	}
	t.Cleanup(func() { ollamaReachableFn, semanticSearchFn = oldReach, oldSem })
	return &calls
}

func TestExtractWordTokensDropsStopWords(t *testing.T) {
	got := extractWordTokens("the field that wipes the accuracy column")
	want := []string{"field", "wipes", "accuracy", "column"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// An all-stop-word query leaves nothing for the AND gate to work with.
	if got := extractWordTokens("what is this about"); len(got) != 0 {
		t.Errorf("all-stop-word query should yield no tokens, got %v", got)
	}
}

// ladderFixture: three sessions all containing both tokens, so a MaxResults=2
// search saturates the cap.
func ladderFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ts := time.Now().AddDate(0, 0, -1).Format("2006-01-02T15:04:05")
	for _, n := range []string{"aaa", "bbb", "ccc"} {
		writeSession(t, dir, n+"00000-1111-2222-3333-444444444444.jsonl", ts, "assistant",
			"widget notes recorded during the "+n+" calibration run")
	}
	return dir
}

func TestLadderHoldsSaturatedTokenizedForSemantic(t *testing.T) {
	dir := ladderFixture(t)
	want := []Match{{Message: Message{Text: "from the vector layer", SessionID: "vec"}}}
	calls := stubSemantic(t, true, want)

	// MaxResults=2 with 3 matching sessions ⇒ the tokenized layer fills the cap.
	_, _, layer, err := searchWithRecovery("widget calibration",
		dir, SearchOpts{Role: "both", MaxResults: 2, MaxDays: 3650}, true)
	if err != nil {
		t.Fatal(err)
	}
	if layer != "semantic" {
		t.Errorf("saturated tokenized must not pre-empt the vector layer, got layer=%q", layer)
	}
	if *calls != 1 {
		t.Errorf("expected the vector layer to be consulted once, got %d calls", *calls)
	}
}

func TestLadderKeepsSaturatedTokenizedWhenSemanticUnavailable(t *testing.T) {
	dir := ladderFixture(t)
	stubSemantic(t, false, nil) // Ollama down

	matches, _, layer, err := searchWithRecovery("widget calibration",
		dir, SearchOpts{Role: "both", MaxResults: 2, MaxDays: 3650}, true)
	if err != nil {
		t.Fatal(err)
	}
	if layer != "tokenized" || len(matches) == 0 {
		t.Errorf("without Ollama the keyword pile still beats nothing, got layer=%q n=%d", layer, len(matches))
	}
}

func TestLadderKeepsDiscriminatingTokenized(t *testing.T) {
	dir := ladderFixture(t)
	calls := stubSemantic(t, true, []Match{{Message: Message{Text: "vector"}}})

	// Cap of 100 is never reached by 3 sessions ⇒ the result discriminated.
	_, _, layer, err := searchWithRecovery("widget calibration",
		dir, SearchOpts{Role: "both", MaxResults: 100, MaxDays: 3650}, true)
	if err != nil {
		t.Fatal(err)
	}
	if layer != "tokenized" {
		t.Errorf("an under-cap tokenized answer should win, got layer=%q", layer)
	}
	if *calls != 0 {
		t.Errorf("vector layer should not have been consulted, got %d calls", *calls)
	}
}
