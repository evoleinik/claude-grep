package main

import (
	"reflect"
	"testing"
)

func TestExtractWordTokens(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"ax check generalize platform", []string{"ax", "check", "generalize", "platform"}},
		{"sp-ucp-manifest llms-txt", []string{"sp-ucp-manifest", "llms-txt"}},
		{"branded_accuracy|rank_for_merchant|sources dict",
			[]string{"branded_accuracy", "rank_for_merchant", "sources", "dict"}},
		{"foo foo foo", []string{"foo"}},          // dedup
		{"a x .*", nil},                            // all tokens <2 chars / metachars → none
		{"singleword", []string{"singleword"}},     // 1 token (caller decides ≥2)
	}
	for _, c := range cases {
		got := extractWordTokens(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("extractWordTokens(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestContainsAllTokens(t *testing.T) {
	data := []byte("The UCP manifest and the JSONLD presence check")
	all := [][]byte{[]byte("ucp"), []byte("jsonld")}
	if !containsAllTokens(data, all) {
		t.Error("expected all tokens present (case-insensitive)")
	}
	missing := [][]byte{[]byte("ucp"), []byte("missingtoken")}
	if containsAllTokens(data, missing) {
		t.Error("expected false when a token is absent")
	}
}

func TestTokenizedSearchAndSemantics(t *testing.T) {
	dir := t.TempDir()
	// session A: contains BOTH tokens → should match
	writeSession(t, dir, "a.jsonl", "2026-06-01T10:00:00", "assistant",
		"we fixed the ucp-manifest and the jsonld presence check")
	// session B: contains only ONE token → AND gate excludes it
	writeSession(t, dir, "b.jsonl", "2026-06-01T11:00:00", "assistant",
		"only talked about jsonld here")

	matches, stats, err := tokenizedSearch([]string{"ucp", "jsonld"}, dir,
		SearchOpts{Role: "both", MaxResults: 100, MaxDays: 3650})
	if err != nil {
		t.Fatalf("tokenizedSearch error: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match (session A only), got %d", len(matches))
	}
	if matches[0].Message.SessionID == "" {
		t.Error("expected a populated SessionID on the match")
	}
	if stats.TotalMatches != 1 {
		t.Errorf("expected TotalMatches=1, got %d", stats.TotalMatches)
	}
}
