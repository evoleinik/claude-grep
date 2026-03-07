package main

import (
	"strings"
	"testing"
)

func TestTokenize(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"hello world", []string{"hello", "world"}},
		{"the quick brown fox", []string{"quick", "brown", "fox"}}, // "the" is stop word
		{"Deploy deployed deploying", []string{"deploy", "deploy", "deploy"}}, // stemming
		{"a b c", nil}, // all filtered (single char + stop)
		{"BM25 scoring", []string{"bm25", "scor"}},
	}
	for _, tt := range tests {
		got := tokenize(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("tokenize(%q) = %v, want %v", tt.input, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("tokenize(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestStem(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"deploy", "deploy"},
		{"deployed", "deploy"},
		{"deploying", "deploy"},
		{"deployment", "deploy"},
		{"deploys", "deploy"},
		{"running", "runn"},
		{"fixes", "fix"},
		{"configuration", "configura"},
		{"go", "go"}, // too short to stem
	}
	for _, tt := range tests {
		got := stem(tt.input)
		if got != tt.want {
			t.Errorf("stem(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestTokenizeWithBigrams(t *testing.T) {
	got := tokenizeWithBigrams("pip install scrapling")
	// unigrams: pip, install, scrap (scrapling→scrap via -ling suffix)
	// bigrams: pip_install, install_scrap
	want := map[string]bool{
		"pip": true, "install": true, "scrap": true,
		"pip_install": true, "install_scrap": true,
	}
	for _, tok := range got {
		if !want[tok] {
			t.Errorf("unexpected token %q in tokenizeWithBigrams", tok)
		}
		delete(want, tok)
	}
	for tok := range want {
		t.Errorf("missing token %q from tokenizeWithBigrams", tok)
	}
}

func TestBm25Score(t *testing.T) {
	docs := [][]string{
		{"deploy", "config", "server"},
		{"test", "unit", "mock"},
		{"deploy", "rollback", "deploy"},
	}
	query := []string{"deploy"}
	scores := bm25Score(docs, query)

	// Doc 2 (two "deploy") should score highest
	if scores[2] <= scores[0] {
		t.Errorf("doc with 2x 'deploy' should score higher: scores=%v", scores)
	}
	// Doc 1 (no "deploy") should score 0
	if scores[1] != 0 {
		t.Errorf("doc without query term should score 0, got %f", scores[1])
	}
}

func TestBm25Compress(t *testing.T) {
	// Build a long text with one relevant paragraph buried in the middle
	var parts []string
	for i := 0; i < 10; i++ {
		parts = append(parts, "Lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor incididunt")
	}
	parts[5] = "The deployment pipeline uses kubernetes to deploy containers across the cluster reliably"
	text := strings.Join(parts, "\n\n")

	compressed := bm25Compress(text, "deploy kubernetes", 300)

	// The compressed text should contain the relevant paragraph
	if !strings.Contains(compressed, "deployment pipeline") {
		t.Errorf("BM25 compress should extract relevant paragraph, got: %s", compressed)
	}
	if len(compressed) > 300 {
		t.Errorf("compressed text exceeds budget: %d > 300", len(compressed))
	}
}

func TestBm25CompressShortText(t *testing.T) {
	short := "just a short message"
	got := bm25Compress(short, "query", 500)
	if got != short {
		t.Errorf("short text should pass through unchanged, got: %s", got)
	}
}

func TestBm25CompressNoRelevant(t *testing.T) {
	text := strings.Repeat("alpha beta gamma delta epsilon\n\n", 20)
	got := bm25Compress(text, "zzzznotfound", 200)
	// Should fall back to head truncation
	if len(got) > 200 {
		t.Errorf("fallback should respect budget: %d > 200", len(got))
	}
}

func TestSplitChunks(t *testing.T) {
	// Double newline split
	text := "paragraph one\n\nparagraph two\n\nparagraph three"
	chunks := splitChunks(text)
	if len(chunks) != 3 {
		t.Errorf("expected 3 chunks, got %d: %v", len(chunks), chunks)
	}

	// Single line fallback (needs >3 lines)
	chunks2 := splitChunks("one\ntwo\nthree\nfour\nfive")
	if len(chunks2) != 5 {
		t.Errorf("single-line split should produce 5 chunks, got %d: %v", len(chunks2), chunks2)
	}
}

func TestSplitSentences(t *testing.T) {
	text := "First sentence here is definitely long enough to be split apart cleanly. Second sentence is also quite long enough for the splitter to work. Third sentence rounds it out nicely here."
	sents := splitSentences(text)
	if len(sents) < 2 {
		t.Errorf("expected multiple sentences, got %d: %v", len(sents), sents)
	}
}

func TestBigramBoost(t *testing.T) {
	// Two docs: one has "pip install" adjacent, other has "pip" and "install" far apart
	docs := [][]string{
		tokenizeWithBigrams("run pip install scrapling from the terminal"),
		tokenizeWithBigrams("install the package and then pip will handle deps"),
	}
	query := tokenizeWithBigrams("pip install")
	scores := bm25Score(docs, query)

	// Doc 0 should score higher because bigram "pip_install" matches
	if scores[0] <= scores[1] {
		t.Errorf("adjacent phrase should score higher: doc0=%f doc1=%f", scores[0], scores[1])
	}
}
