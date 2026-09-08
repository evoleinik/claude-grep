package main

// Seams so the ladder's ROUTING can be tested without Ollama, mirroring the
// semanticDocsBenchFn seam on the docs side.
var (
	ollamaReachableFn = ollamaReachable
	semanticSearchFn  = semanticSearch
)

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
	//
	// A tokenized answer that FILLS the result cap did not discriminate: the AND
	// gate admitted more sessions than we can show, so what comes back is ordered
	// by whichever token hit, not by relevance. Hold that as a fallback and let
	// the vector layer answer first. Returning it immediately is what made a
	// natural-language query score hit@10 = 0 while the vector layer, asked
	// directly, found the session.
	var weakMatches []Match
	var weakStats SearchStats
	tokens := extractWordTokens(pattern)
	if len(tokens) >= 2 {
		if tm, ts, terr := tokenizedSearch(tokens, searchPath, opts); terr == nil && len(tm) > 0 {
			if len(tm) < opts.MaxResults {
				return tm, ts, "tokenized", nil
			}
			weakMatches, weakStats = tm, ts
		}
	}

	// Layer 3: semantic (Ollama). Safety net for AND-misses / conceptual queries.
	if allowSemantic && ollamaReachableFn() {
		if sm, serr := semanticSearchFn(pattern, searchPath, opts); serr == nil && len(sm) > 0 {
			return sm, SearchStats{FilesTotal: stats.FilesTotal, TotalMatches: len(sm)}, "semantic", nil
		}
	}

	// Semantic was unavailable or empty — a saturated keyword pile still beats nothing.
	if len(weakMatches) > 0 {
		return weakMatches, weakStats, "tokenized", nil
	}

	return nil, stats, "none", nil
}
