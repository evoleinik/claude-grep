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
