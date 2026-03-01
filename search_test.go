package main

import (
	"testing"
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
