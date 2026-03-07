package main

import (
	"math"
	"strings"
	"unicode"
)

// bm25Compress extracts the most query-relevant paragraphs from text,
// preserving document order. Returns compressed text within maxLen chars.
func bm25Compress(text, query string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}

	paras := splitParagraphs(text)
	if len(paras) == 0 {
		return text[:maxLen]
	}

	queryTokens := tokenize(query)
	if len(queryTokens) == 0 {
		return text[:maxLen]
	}

	// Tokenize all paragraphs
	docs := make([][]string, len(paras))
	for i, p := range paras {
		docs[i] = tokenize(p)
	}

	scores := bm25Score(docs, queryTokens)

	// Select top-scoring paragraphs that fit in budget, preserving order
	type scored struct {
		idx   int
		score float64
		text  string
	}
	var ranked []scored
	for i, s := range scores {
		if s > 0 {
			ranked = append(ranked, scored{idx: i, score: s, text: paras[i]})
		}
	}

	if len(ranked) == 0 {
		// No relevant paragraphs — fall back to head truncation
		return text[:maxLen]
	}

	// Sort by score descending
	for i := 0; i < len(ranked)-1; i++ {
		for j := i + 1; j < len(ranked); j++ {
			if ranked[j].score > ranked[i].score {
				ranked[i], ranked[j] = ranked[j], ranked[i]
			}
		}
	}

	// Greedily select paragraphs within budget
	budget := maxLen
	selected := make([]int, 0, len(ranked))
	used := 0
	for _, r := range ranked {
		cost := len(r.text) + 2 // paragraph + separator
		if used+cost > budget {
			continue
		}
		selected = append(selected, r.idx)
		used += cost
	}

	if len(selected) == 0 {
		// Even the top paragraph is too large — take a prefix of it
		if len(ranked[0].text) > maxLen {
			return ranked[0].text[:maxLen]
		}
		return ranked[0].text
	}

	// Restore document order
	for i := 0; i < len(selected)-1; i++ {
		for j := i + 1; j < len(selected); j++ {
			if selected[j] < selected[i] {
				selected[i], selected[j] = selected[j], selected[i]
			}
		}
	}

	var b strings.Builder
	for i, idx := range selected {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(paras[idx])
	}

	result := b.String()
	if len(selected) < len(ranked) {
		result += " [...]"
	}
	return result
}

// splitParagraphs splits text on double-newlines or single newlines for
// messages that don't have paragraph structure.
func splitParagraphs(text string) []string {
	// Try double-newline split first
	paras := strings.Split(text, "\n\n")
	if len(paras) > 1 {
		var out []string
		for _, p := range paras {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	}

	// Fall back to single-newline split for flat text
	lines := strings.Split(text, "\n")
	if len(lines) <= 3 {
		return []string{text}
	}
	var out []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// tokenize splits text into lowercase word tokens.
func tokenize(text string) []string {
	var tokens []string
	var word strings.Builder
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			word.WriteRune(r)
		} else {
			if word.Len() > 0 {
				tokens = append(tokens, word.String())
				word.Reset()
			}
		}
	}
	if word.Len() > 0 {
		tokens = append(tokens, word.String())
	}
	return tokens
}

// bm25Score computes BM25 scores for each document given query tokens.
// Uses standard parameters: k1=1.2, b=0.75
func bm25Score(docs [][]string, query []string) []float64 {
	k1 := 1.2
	b := 0.75
	n := len(docs)

	// Average document length
	totalLen := 0
	for _, d := range docs {
		totalLen += len(d)
	}
	avgDL := float64(totalLen) / float64(n)
	if avgDL == 0 {
		avgDL = 1
	}

	// Document frequency for each query term
	df := make(map[string]int)
	for _, qt := range query {
		seen := make(map[int]bool)
		for i, d := range docs {
			if seen[i] {
				continue
			}
			for _, t := range d {
				if t == qt {
					df[qt]++
					seen[i] = true
					break
				}
			}
		}
	}

	scores := make([]float64, n)
	for i, d := range docs {
		// Term frequency in this doc
		tf := make(map[string]int)
		for _, t := range d {
			tf[t]++
		}

		dl := float64(len(d))
		for _, qt := range query {
			f := float64(tf[qt])
			if f == 0 {
				continue
			}
			// IDF: log((N - df + 0.5) / (df + 0.5) + 1)
			idf := math.Log((float64(n)-float64(df[qt])+0.5)/(float64(df[qt])+0.5) + 1)
			// BM25 term score
			scores[i] += idf * (f * (k1 + 1)) / (f + k1*(1-b+b*dl/avgDL))
		}
	}

	return scores
}
