package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecentDistinctQueries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(indexDir(), 0755); err != nil {
		t.Fatal(err)
	}
	lines := `{"pattern":"alpha","mode":"regex"}
{"pattern":"beta","mode":"regex"}
{"pattern":"alpha","mode":"semantic"}
{"pattern":"gamma","mode":"regex"}
`
	if err := os.WriteFile(filepath.Join(indexDir(), "usage.jsonl"), []byte(lines), 0644); err != nil {
		t.Fatal(err)
	}

	got := recentDistinctQueries(10)
	want := []string{"gamma", "alpha", "beta"} // most-recent-first, deduped
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("at %d: want %q, got %q", i, want[i], got[i])
		}
	}
}
