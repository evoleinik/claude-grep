package main

import (
	"sort"
	"testing"
)

func TestCosineSimilarity(t *testing.T) {
	// Identical vectors → 1.0
	a := []float32{1, 0, 0}
	if sim := cosineSimilarity(a, a); sim < 0.99 {
		t.Errorf("identical vectors: got %f, want ~1.0", sim)
	}
	// Orthogonal → 0.0
	b := []float32{0, 1, 0}
	if sim := cosineSimilarity(a, b); sim > 0.01 {
		t.Errorf("orthogonal vectors: got %f, want ~0.0", sim)
	}
	// Empty → 0.0
	if sim := cosineSimilarity(nil, nil); sim != 0 {
		t.Errorf("empty vectors: got %f, want 0", sim)
	}
	// Mismatched lengths → 0.0
	c := []float32{1, 0}
	if sim := cosineSimilarity(a, c); sim != 0 {
		t.Errorf("mismatched lengths: got %f, want 0", sim)
	}
}

// A session relevant throughout must outrank one with a single lucky chunk.
func TestRankBySessionAggregatePrefersSustainedRelevance(t *testing.T) {
	mk := func(file string, sims ...float32) []scored {
		var out []scored
		for i, s := range sims {
			out = append(out, scored{entry: IndexEntry{FilePath: file, SessionID: "s", MsgIndex: i}, similarity: s})
		}
		return out
	}
	// "lucky" has the single highest chunk; "sustained" is better overall.
	in := append(mk("lucky.jsonl", 0.90), mk("sustained.jsonl", 0.88, 0.87, 0.86)...)
	sort.SliceStable(in, func(a, b int) bool { return in[a].similarity > in[b].similarity })

	got := rankBySessionAggregate(in)
	if len(got) == 0 || got[0].entry.FilePath != "sustained.jsonl" {
		t.Fatalf("sustained relevance should win; got %v", got[0].entry.FilePath)
	}
}
