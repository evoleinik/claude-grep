package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The dangerous mistake is touching a LIVE project: the indexer owns those, and
// rewriting one from 200-char previews would replace full-text vectors with
// truncated ones. Orphans are the only safe target.
func TestReembedOrphansSelectsOnlyDeadStaleProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude", "projects", "live-proj"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(indexDir(), 0755); err != nil {
		t.Fatal(err)
	}

	mk := func(project, model string) {
		idx := &Index{
			Project:    project,
			EmbedModel: model,
			Files:      map[string]FileMetadata{},
			Entries: []IndexEntry{{
				SessionID: "s1", Preview: "some recoverable text", Vector: []float32{0.1, 0.2},
			}},
		}
		if err := saveIndex(idx); err != nil {
			t.Fatal(err)
		}
	}
	mk("live-proj", "nomic-embed-text")  // on disk → indexer's job, must be skipped
	mk("dead-stale", "nomic-embed-text") // gone from disk, old model → the target
	mk("dead-fresh", embedModel)         // gone from disk, already migrated → skip

	got := orphanProjects()
	if len(got) != 1 || got[0] != "dead-stale" {
		t.Fatalf("want exactly [dead-stale], got %v", got)
	}
}
