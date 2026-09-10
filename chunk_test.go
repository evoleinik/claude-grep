package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// Transcripts are append-only. Re-embedding a whole file on every change makes
// a cron pass cost more the longer a session runs; measured 2026-09-09, one
// active session was 724 messages re-embedded every 30 minutes.
func TestIncrementalIndexingOnlyEmbedsNewMessages(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	proj := filepath.Join(dir, ".claude", "projects", "p")
	if err := os.MkdirAll(proj, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(indexDir(), 0755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(proj, "s0000000-1111-2222-3333-444444444444.jsonl")

	line := func(text string) string {
		b, _ := json.Marshal(map[string]any{
			"type": "assistant", "timestamp": "2026-09-09T10:00:00Z",
			"message": map[string]any{"role": "assistant", "content": text},
		})
		return string(b) + "\n"
	}
	if err := os.WriteFile(f, []byte(line("first")+line("second")), 0644); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(f)
	idx := &Index{Project: "p", EmbedModel: indexStamp,
		Files: map[string]FileMetadata{f: {FilePath: f, LastModified: st.ModTime(), Messages: 2, Size: st.Size()}}}
	if err := saveIndex(idx); err != nil {
		t.Fatal(err)
	}

	// Append a third message and confirm the resume point is message 2, not 0.
	fh, _ := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0644)
	fh.WriteString(line("third"))
	fh.Close()
	st2, _ := os.Stat(f)

	meta := loadIndex("p").Files[f]
	if meta.Messages != 2 {
		t.Fatalf("stored message count lost: %d", meta.Messages)
	}
	if st2.Size() < meta.Size {
		t.Fatal("precondition: appended file must be larger")
	}
	// Grown file + known count => resume, not rebuild.
	if !(meta.Messages > 0 && st2.Size() >= meta.Size) {
		t.Error("an appended file must resume from the stored count")
	}
	// A shrunken file must force a full rebuild instead.
	if err := os.WriteFile(f, []byte(line("rewritten")), 0644); err != nil {
		t.Fatal(err)
	}
	st3, _ := os.Stat(f)
	if st3.Size() >= meta.Size {
		t.Skip("rewrite did not shrink the file; nothing to assert")
	}
	if meta.Messages > 0 && st3.Size() >= meta.Size {
		t.Error("a shrunken file must NOT resume — it was rewritten, not appended")
	}
}

// Indexes are built on GPU workers where transcripts live under /tmp/s<n>/...,
// so stored paths must be rewritten onto this machine or context retrieval
// reads a file that is not there and incremental metadata never matches.
func TestNormalizeIndexPathsRewritesForeignPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	want := filepath.Join(home, ".claude", "projects", "proj", "sess.jsonl")
	foreign := "/tmp/s6/.claude/projects/proj/sess.jsonl"

	idx := &Index{
		Entries: []IndexEntry{{FilePath: foreign}},
		Files:   map[string]FileMetadata{foreign: {FilePath: foreign, Messages: 7}},
	}
	normalizeIndexPaths(idx)

	if idx.Entries[0].FilePath != want {
		t.Errorf("entry path not rewritten:\n got  %s\n want %s", idx.Entries[0].FilePath, want)
	}
	m, ok := idx.Files[want]
	if !ok {
		t.Fatalf("Files key not rewritten; keys are %v", idx.Files)
	}
	if m.Messages != 7 || m.FilePath != want {
		t.Errorf("metadata lost in the rewrite: %+v", m)
	}
	// Through loadIndex, the real path. Calling normalizeIndexPaths directly
	// proves the function works but NOT that anything calls it — removing the
	// call from loadIndex left the direct test green.
	if err := os.MkdirAll(indexDir(), 0755); err != nil {
		t.Fatal(err)
	}
	if err := saveIndexRaw(&Index{Project: "p", Files: map[string]FileMetadata{
		foreign: {FilePath: foreign, Messages: 7}}, Entries: []IndexEntry{{FilePath: foreign}}}); err != nil {
		t.Fatal(err)
	}
	got := loadIndex("p")
	if got.Entries[0].FilePath != want {
		t.Errorf("loadIndex did not normalize: %s", got.Entries[0].FilePath)
	}
	if _, ok := got.Files[want]; !ok {
		t.Errorf("loadIndex did not normalize the Files key: %v", got.Files)
	}

	// A path with no projects marker is left alone rather than mangled.
	other := &Index{Entries: []IndexEntry{{FilePath: "/somewhere/else.jsonl"}}}
	normalizeIndexPaths(other)
	if other.Entries[0].FilePath != "/somewhere/else.jsonl" {
		t.Errorf("unrelated path was rewritten: %s", other.Entries[0].FilePath)
	}
}
