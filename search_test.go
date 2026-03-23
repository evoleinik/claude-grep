package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExtractPrefilterLiterals(t *testing.T) {
	tests := []struct {
		pattern string
		want    []string // nil means pre-filter disabled
	}{
		// Simple literal
		{"openclaw", []string{"openclaw"}},
		// Alternation in parens
		{"(openclaw|heartbeat)", []string{"openclaw", "heartbeat"}},
		// Alternation without parens
		{"openclaw|heartbeat", []string{"openclaw", "heartbeat"}},
		// Multi-branch
		{"(UPS|power supply|APC)", []string{"ups", "power supply", "apc"}},
		// Dot metachar — extracts longest literal run
		{"open.claw", []string{"open"}},
		// Non-capturing group
		{"(?:foo|bar)", []string{"foo", "bar"}},
		// Case-insensitive group
		{"(?i:foo|bar)", []string{"foo", "bar"}},
		// Pure metacharacters — can't pre-filter
		{".*", nil},
		// Single char alternatives — still valid
		{"(a|b)", []string{"a", "b"}},
		// One branch is pure metachar — disables pre-filter
		{"(openclaw|.*)", nil},
		// Escaped metachar treated as literal
		{"open\\.claw", []string{"open.claw"}},
	}

	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			got := extractPrefilterLiterals(tt.pattern)
			if tt.want == nil {
				if got != nil {
					t.Errorf("want nil, got %v", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("len: got %d, want %d (%v)", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if string(got[i]) != tt.want[i] {
					t.Errorf("[%d]: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestPrefilterMatch(t *testing.T) {
	data := []byte("The OpenClaw framework has a Heartbeat feature")

	tests := []struct {
		name     string
		literals [][]byte
		want     bool
	}{
		{"nil literals — always match", nil, true},
		{"empty literals — always match", [][]byte{}, true},
		{"single match", [][]byte{[]byte("openclaw")}, true},
		{"single no match", [][]byte{[]byte("zzzzz")}, false},
		{"alternation — first matches", [][]byte{[]byte("openclaw"), []byte("gateway")}, true},
		{"alternation — second matches", [][]byte{[]byte("gateway"), []byte("heartbeat")}, true},
		{"alternation — none match", [][]byte{[]byte("gateway"), []byte("zzzzz")}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := prefilterMatch(data, tt.literals)
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStripOuterGroup(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"(a|b)", "a|b"},
		{"(?:a|b)", "a|b"},
		{"(?i:a|b)", "a|b"},
		{"(a|b)(c|d)", "(a|b)(c|d)"}, // outer parens don't match
		{"abc", "abc"},                // no parens
		{"(abc)", "abc"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := stripOuterGroup(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsSuspiciousPattern(t *testing.T) {
	suspicious := []string{"-", ".", "*", ".*"}
	for _, p := range suspicious {
		if !isSuspiciousPattern(p) {
			t.Errorf("%q should be suspicious", p)
		}
	}
	valid := []string{"openclaw", "(a|b)", "deploy.*prod", "--flag"}
	for _, p := range valid {
		if isSuspiciousPattern(p) {
			t.Errorf("%q should NOT be suspicious", p)
		}
	}
}

func TestNormalizeBRE(t *testing.T) {
	tests := []struct{ in, want string }{
		{`\(a\|b\)`, "(a|b)"},
		{"(a|b)", "(a|b)"},
		{`open\.claw`, `open\.claw`},
	}
	for _, tt := range tests {
		got := normalizeBRE(tt.in)
		if got != tt.want {
			t.Errorf("normalizeBRE(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestExcludeNewestFile(t *testing.T) {
	// Create temp files with different mtimes
	dir := t.TempDir()

	old := filepath.Join(dir, "old.jsonl")
	mid := filepath.Join(dir, "mid.jsonl")
	fresh := filepath.Join(dir, "fresh.jsonl")

	for _, f := range []string{old, mid, fresh} {
		os.WriteFile(f, []byte("{}"), 0644)
	}

	// Set old and mid to past timestamps
	past := time.Now().Add(-5 * time.Minute)
	older := time.Now().Add(-10 * time.Minute)
	os.Chtimes(old, older, older)
	os.Chtimes(mid, past, past)
	// fresh keeps its current mtime (within 60s)

	files := []string{old, mid, fresh}
	result := excludeNewestFile(files)

	if len(result) != 2 {
		t.Fatalf("expected 2 files, got %d", len(result))
	}
	for _, f := range result {
		if f == fresh {
			t.Error("fresh file should have been excluded")
		}
	}
}

func TestExcludeNewestFileAllOld(t *testing.T) {
	// When all files are older than 60s, none should be excluded
	dir := t.TempDir()

	a := filepath.Join(dir, "a.jsonl")
	b := filepath.Join(dir, "b.jsonl")

	for _, f := range []string{a, b} {
		os.WriteFile(f, []byte("{}"), 0644)
	}

	past := time.Now().Add(-5 * time.Minute)
	os.Chtimes(a, past, past)
	os.Chtimes(b, past, past)

	files := []string{a, b}
	result := excludeNewestFile(files)

	if len(result) != 2 {
		t.Fatalf("expected 2 files (none excluded), got %d", len(result))
	}
}

func TestExcludeNewestFileSingleFile(t *testing.T) {
	// Single file should not be excluded (even if fresh)
	dir := t.TempDir()
	f := filepath.Join(dir, "only.jsonl")
	os.WriteFile(f, []byte("{}"), 0644)

	files := []string{f}
	result := excludeNewestFile(files)

	if len(result) != 1 {
		t.Fatalf("single file should not be excluded, got %d", len(result))
	}
}

func TestLongestLiteral(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"openclaw", "openclaw"},
		{"open.claw", "open"},           // dot splits it, "open" and "claw" are 4 each, "open" first
		{"a.*long_literal", "long_literal"},
		{".*", ""},
		{"abc\\.def", "abc.def"},        // escaped dot is literal
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := longestLiteral(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
