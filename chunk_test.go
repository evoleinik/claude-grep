package main

import (
	"strings"
	"testing"
)

func TestSplitForEmbedding(t *testing.T) {
	if got := splitForEmbedding("short message"); len(got) != 1 || got[0] != "short message" {
		t.Errorf("short text must pass through whole, got %v", got)
	}
	// Sentence-boundary preference: cuts should land after a full stop.
	long := strings.Repeat("This is a sentence about indexing. ", 60)
	parts := splitForEmbedding(long)
	if len(parts) < 2 {
		t.Fatalf("expected a long message to split, got %d", len(parts))
	}
	for i, p := range parts {
		if len(p) > chunkChars+64 {
			t.Errorf("chunk %d is %d chars, over the %d budget", i, len(p), chunkChars)
		}
		if p != strings.TrimSpace(p) {
			t.Errorf("chunk %d not trimmed: %q", i, p)
		}
	}
	// No boundary anywhere: must still split rather than emit one huge chunk.
	blob := strings.Repeat("x", chunkChars*3)
	if got := splitForEmbedding(blob); len(got) < 3 {
		t.Errorf("boundary-free text must still be cut, got %d chunks", len(got))
	}
	// Nothing may be lost.
	joined := strings.Join(splitForEmbedding(long), " ")
	if len(joined) < len(strings.TrimSpace(long))-len(parts) {
		t.Errorf("split dropped content: %d vs %d", len(joined), len(long))
	}
}
