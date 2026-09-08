package main

import (
	"bytes"
	"regexp"
	"strings"
)

// wordTokenRe matches contiguous runs of letters, digits, underscore, hyphen.
// Keeps symbol-y identifiers whole: "sp-ucp-manifest" is one token.
var wordTokenRe = regexp.MustCompile(`[\p{L}\p{N}_-]+`)

// extractWordTokens pulls de-duplicated, lowercased CONTENT tokens (length >= 2)
// from a pattern, discarding regex metacharacters and stop words. Order is preserved.
//
// Stop words are dropped because every consumer AND-gates on these tokens at
// SESSION-FILE granularity. Session files average tens of KB, so a file contains
// "the"/"and"/"for" with near-certainty: keeping them made the gate admit almost
// every session, and the OR surface then ranked by whichever common word hit
// first. Reuses the BM25 stop list so the ladder and the hints tokenize alike.
func extractWordTokens(pattern string) []string {
	raw := wordTokenRe.FindAllString(pattern, -1)
	seen := map[string]bool{}
	var tokens []string
	for _, w := range raw {
		w = strings.ToLower(strings.Trim(w, "-"))
		if len(w) < 2 || seen[w] || stopWords[w] {
			continue
		}
		seen[w] = true
		tokens = append(tokens, w)
	}
	return tokens
}

// containsAllTokens reports whether data contains every token (case-insensitive).
// tokens must already be lowercased.
func containsAllTokens(data []byte, tokens [][]byte) bool {
	lower := bytes.ToLower(data)
	for _, t := range tokens {
		if !bytes.Contains(lower, t) {
			return false
		}
	}
	return true
}

// tokenizedSearch rescues a multi-word query that matched nothing as a literal
// phrase. It selects sessions containing ALL tokens (AND gate) and surfaces the
// messages matching ANY token (OR regex). No external dependencies.
func tokenizedSearch(tokens []string, searchPath string, opts SearchOpts) ([]Match, SearchStats, error) {
	quoted := make([]string, len(tokens))
	gate := make([][]byte, len(tokens))
	for i, t := range tokens {
		quoted[i] = regexp.QuoteMeta(t)
		gate[i] = []byte(strings.ToLower(t))
	}
	re, err := regexp.Compile("(?i)(" + strings.Join(quoted, "|") + ")")
	if err != nil {
		return nil, SearchStats{}, err
	}
	return searchCore(re, nil, gate, searchPath, opts)
}
