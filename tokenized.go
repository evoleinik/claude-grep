package main

import (
	"bytes"
	"regexp"
	"strings"
)

// wordTokenRe matches contiguous runs of letters, digits, underscore, hyphen.
// Keeps symbol-y identifiers whole: "sp-ucp-manifest" is one token.
var wordTokenRe = regexp.MustCompile(`[\p{L}\p{N}_-]+`)

// extractWordTokens pulls de-duplicated, lowercased word tokens (length >= 2)
// from a pattern, discarding regex metacharacters. Order is preserved.
func extractWordTokens(pattern string) []string {
	raw := wordTokenRe.FindAllString(pattern, -1)
	seen := map[string]bool{}
	var tokens []string
	for _, w := range raw {
		w = strings.ToLower(strings.Trim(w, "-"))
		if len(w) < 2 || seen[w] {
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
