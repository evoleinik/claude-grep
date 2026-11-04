package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeOllama answers /api/embed with one fixed unit vector and records every
// request body, so tests can run the real index and search paths offline.
type fakeOllama struct {
	mu     sync.Mutex
	bodies []embedRequest
}

func startFakeOllama(t *testing.T) *fakeOllama {
	t.Helper()
	f := &fakeOllama{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			w.WriteHeader(200) // ollamaReachable
			return
		}
		b, _ := io.ReadAll(r.Body)
		var req embedRequest
		json.Unmarshal(b, &req)
		f.mu.Lock()
		f.bodies = append(f.bodies, req)
		f.mu.Unlock()
		vec := make([]float32, 8)
		vec[0] = 1
		json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{vec}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CLAUDE_GREP_OLLAMA_URL", srv.URL)
	return f
}

func writeTranscript(t *testing.T, path, ts string, texts ...string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0755)
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	for _, text := range texts {
		b, _ := json.Marshal(map[string]any{
			"type": "assistant", "timestamp": ts,
			"message": map[string]any{"role": "assistant", "content": text},
		})
		fh.Write(append(b, '\n'))
	}
}

// An index run must rewrite only the projects it changed. It used to rewrite
// every gob on every run: 4.35 GB every 30 minutes on box.
func TestIndexRewritesOnlyChangedProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	startFakeOllama(t)
	projects := filepath.Join(home, ".claude", "projects")
	a := filepath.Join(projects, "a", "aaaaaaaa-1111-2222-3333-444444444444.jsonl")
	b := filepath.Join(projects, "b", "bbbbbbbb-1111-2222-3333-444444444444.jsonl")
	writeTranscript(t, a, "2026-11-04T10:00:00Z", "first in a")
	writeTranscript(t, b, "2026-11-04T10:00:00Z", "first in b")
	os.MkdirAll(indexDir(), 0755)

	runIndex(false)
	if len(loadIndex("a").Entries) != 1 || len(loadIndex("b").Entries) != 1 {
		t.Fatal("first run must index both projects")
	}
	// Age both gobs so a rewrite is visible as a fresh mtime.
	past := time.Now().Add(-48 * time.Hour)
	for _, p := range []string{"a", "b"} {
		os.Chtimes(indexPath(p), past, past)
	}

	writeTranscript(t, a, "2026-11-04T11:00:00Z", "second in a")
	// File mtimes tick at kernel-jiffy granularity, so an append in the same
	// tick as the first write looks unchanged. Move it forward explicitly.
	later := time.Now().Add(time.Minute)
	os.Chtimes(a, later, later)
	runIndex(false)

	if got := len(loadIndex("a").Entries); got != 2 {
		t.Errorf("changed project a: want 2 entries saved, got %d", got)
	}
	stA, _ := os.Stat(indexPath("a"))
	stB, _ := os.Stat(indexPath("b"))
	if !stA.ModTime().After(past.Add(time.Hour)) {
		t.Error("changed project a was not saved")
	}
	if stB.ModTime().After(past.Add(time.Hour)) {
		t.Error("unchanged project b was rewritten")
	}
}

// A project whose newest transcript predates the window cannot hold a message
// inside it, so a dated search must not decode its gob. The old project's gob
// is planted with an IN-window timestamp: it shows up only if the gob is read.
func TestSemanticSearchSkipsProjectsOutsideTheWindow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	startFakeOllama(t)
	projects := filepath.Join(home, ".claude", "projects")
	os.MkdirAll(indexDir(), 0755)
	vec := []float32{1, 0, 0, 0, 0, 0, 0, 0}
	recent := time.Now().UTC().Add(-time.Hour).Format("2006-01-02T15:04:05")

	plant := func(project, session string, mtime time.Time, withTranscript bool) {
		f := filepath.Join(projects, project, session+".jsonl")
		if withTranscript {
			writeTranscript(t, f, recent+"Z", "text")
			os.Chtimes(f, mtime, mtime)
		}
		saveIndex(&Index{Project: project, EmbedModel: indexStamp,
			Files: map[string]FileMetadata{},
			Entries: []IndexEntry{{SessionID: session, Role: "assistant", Timestamp: recent,
				Preview: "text", FilePath: f, Vector: vec}}})
	}
	plant("old", "old00000", time.Now().Add(-30*24*time.Hour), true)
	plant("new", "new00000", time.Now(), true)
	plant("gone", "gone0000", time.Time{}, false) // archived: no transcript left

	sessions := func(opts SearchOpts) map[string]bool {
		got, err := semanticSearch("q", projects, opts)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]bool{}
		for _, g := range got {
			m[g.Message.SessionID] = true
		}
		return m
	}

	week := sessions(SearchOpts{Role: "both", MaxResults: 10, MaxDays: 7, IncludeArchived: true})
	if week["old00000"] {
		t.Error("project with only month-old transcripts was decoded for a 7-day search")
	}
	if !week["new00000"] {
		t.Error("project with a fresh transcript was skipped")
	}
	if !week["gone0000"] {
		t.Error("archived project with in-window entries was skipped under --archived")
	}

	all := sessions(SearchOpts{Role: "both", MaxResults: 10, IncludeArchived: true})
	if !all["old00000"] {
		t.Error("an undated search must still read every project")
	}
}
