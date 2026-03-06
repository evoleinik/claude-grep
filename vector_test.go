package main

import (
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
