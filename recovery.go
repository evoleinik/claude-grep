package main

// searchWithRecovery runs the escalation ladder for a single query and reports
// which layer produced the results: "regex" | "tokenized" | "semantic" | "none".
// It performs NO telemetry logging or printing — callers own that policy, so the
// benchmark harness can reuse the exact live path without polluting usage.jsonl.
// Scope is never widened here; opts is honored as-is (see spec "Prior art").
func searchWithRecovery(pattern, searchPath string, opts SearchOpts, allowSemantic bool) ([]Match, SearchStats, string, error) {
	norm := normalizeBRE(pattern)
	matches, stats, err := regexSearch(norm, searchPath, opts)
	if err != nil {
		return nil, stats, "", err // invalid regex — caller exits 2 (preserve current behavior)
	}
	if len(matches) > 0 {
		return matches, stats, "regex", nil
	}

	// Layer 2: tokenized AND-of-terms (no Ollama).
	tokens := extractWordTokens(pattern)
	if len(tokens) >= 2 {
		if tm, ts, terr := tokenizedSearch(tokens, searchPath, opts); terr == nil && len(tm) > 0 {
			return tm, ts, "tokenized", nil
		}
	}

	// Layer 3: semantic (Ollama). Safety net for AND-misses / conceptual queries.
	if allowSemantic && ollamaReachable() {
		if sm, serr := semanticSearch(pattern, searchPath, opts); serr == nil && len(sm) > 0 {
			return sm, SearchStats{FilesTotal: stats.FilesTotal, TotalMatches: len(sm)}, "semantic", nil
		}
	}

	return nil, stats, "none", nil
}
