package main

import (
	"encoding/json"
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

func TestExtractTextIncludesToolBlocks(t *testing.T) {
	raw := func(s string) map[string]json.RawMessage {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	// tool_use input (object) and tool_result content (array of blocks).
	got := extractText(raw(`{"message":{"content":[
		{"type":"text","text":"checking the config"},
		{"type":"tool_use","input":{"command":"grep decisionPack schema.prisma"}},
		{"type":"tool_result","content":[{"type":"text","text":"decisionPack Json?"}]}
	]}}`))
	for _, want := range []string{"checking the config", "grep decisionPack schema.prisma", "decisionPack Json?"} {
		if !strings.Contains(got, want) {
			t.Errorf("indexed text is missing %q\ngot: %s", want, got)
		}
	}
	// A plain-string tool_result must survive too.
	if got := extractText(raw(`{"message":{"content":[{"type":"tool_result","content":"exit status 1"}]}}`)); !strings.Contains(got, "exit status 1") {
		t.Errorf("string tool_result dropped, got %q", got)
	}
}

func TestFlattenToolPayloadIsDeterministic(t *testing.T) {
	// Go map iteration is random; an index that reordered between runs would
	// produce different vectors for identical input.
	in := json.RawMessage(`{"b":"beta","a":"alpha","c":"gamma"}`)
	first := flattenToolPayload(in)
	for i := 0; i < 20; i++ {
		if got := flattenToolPayload(in); got != first {
			t.Fatalf("run %d differs: %q vs %q", i, got, first)
		}
	}
}
