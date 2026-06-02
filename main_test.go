package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// capHintString captures printCapHint's stderr output for assertions.
func capHintString(t *testing.T, opts SearchOpts, total int) string {
	t.Helper()
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	printCapHint(opts, total)
	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	return buf.String()
}

func TestPrintCapHintShowsTotal(t *testing.T) {
	out := capHintString(t, SearchOpts{MaxResults: 100, MaxDays: 30}, 437)
	if !strings.Contains(out, "showing 100 of 437") {
		t.Errorf("expected 'showing 100 of 437', got %q", out)
	}
	if strings.Contains(out, "-n 100") {
		t.Errorf("must not suggest the current cap value, got %q", out)
	}
}

func noMatchHintString(t *testing.T, pattern string, opts SearchOpts, stats SearchStats) string {
	t.Helper()
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	printNoMatchHint(pattern, "/tmp/x/.claude/projects", opts, false, stats)
	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	return buf.String()
}

func TestNoMatchHintPhraseLeadsWithNarrow(t *testing.T) {
	out := noMatchHintString(t, "alpha beta gamma",
		SearchOpts{MaxDays: 30}, SearchStats{FilesTotal: 50, PrefilterSkipped: 50})
	if !strings.Contains(out, "narrow") {
		t.Errorf("phrase hint should suggest narrowing to a distinctive token, got %q", out)
	}
}

func TestNoMatchHintSingleLiteralOffersWiderScope(t *testing.T) {
	out := noMatchHintString(t, "Xyzzy123",
		SearchOpts{MaxDays: 7}, SearchStats{FilesTotal: 50})
	if !strings.Contains(out, "-a -d 30") {
		t.Errorf("single-literal miss should offer wider scope, got %q", out)
	}
}
