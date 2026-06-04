package main

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// seams for testing (default to the real functions)
var embedQueryFn = embed
var refreshDocsFn = refreshDocsIndex

// DocMatch is a hit from the curated-docs lane.
type DocMatch struct {
	File       string
	Heading    string
	Text       string
	Similarity float32
	Line       int // 1-based line of the heading in File (0 if unknown)
}

// docsCap bounds the docs block: surface the few canonical sections, not a dump.
func docsCap(maxResults int) int {
	if maxResults < 5 {
		return maxResults
	}
	return 5
}

// regexDocsSearch scans live .md files, attributing each hit to its heading.
func regexDocsSearch(pattern string, dirs []string, cap int) ([]DocMatch, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	var out []DocMatch
	for _, dir := range dirs {
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
				return nil
			}
			if path != dir && isIndexDoc(path) {
				return nil // skip a README/MEMORY found inside a docs dir; keep explicit file entries
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			for _, c := range chunkMarkdown(data) {
				if re.MatchString(c.Heading) || re.MatchString(c.Body) {
					out = append(out, DocMatch{File: path, Heading: c.Heading, Text: c.Body, Line: c.Line})
					if len(out) >= cap {
						return filepath.SkipAll
					}
				}
			}
			return nil
		})
		if len(out) >= cap {
			break
		}
	}
	return out, nil
}

// semanticDocsSearch ranks doc chunks by cosine similarity to the query.
// Lazily refreshes the per-repo index first (best-effort; needs ollama).
func semanticDocsSearch(query, repoRoot string, dirs []string, cap int) ([]DocMatch, error) {
	if ollamaReachable() {
		_ = refreshDocsFn(repoRoot, dirs, embed) // best-effort; ignore errors
	}
	idx := loadDocsIndex(repoRoot)
	if len(idx.Entries) == 0 {
		return nil, nil
	}
	qv, err := embedQueryFn(query)
	if err != nil {
		return nil, err
	}
	type scored struct {
		e   IndexEntry
		sim float32
	}
	var cands []scored
	for _, e := range idx.Entries {
		if e.Source != "docs" {
			continue
		}
		if sim := cosineSimilarity(qv, e.Vector); sim > 0.55 {
			cands = append(cands, scored{e, sim})
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].sim > cands[j].sim })
	if len(cands) > cap {
		cands = cands[:cap]
	}
	var out []DocMatch
	for _, c := range cands {
		out = append(out, DocMatch{
			File: c.e.FilePath, Heading: c.e.Heading, Text: c.e.Preview, Similarity: c.sim, Line: c.e.Line,
		})
	}
	return out, nil
}

// lexicalDocsSearch ranks doc chunks by BM25 over the live .md files (no index).
// Catches exact-term / identifier queries (e.g. "design partner", "promptContext")
// that dense embeddings rank poorly. Reuses the shared tokenizer (stemming +
// stop-word filtering) so it matches the rest of the tool.
func lexicalDocsSearch(query string, dirs []string, limit int) []DocMatch {
	type chunkDoc struct {
		file, heading, body string
		line                int
		toks                []string
	}
	var docs []chunkDoc
	df := map[string]int{}
	for _, dir := range dirs {
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
				return nil
			}
			if path != dir && isIndexDoc(path) {
				return nil // skip a README/MEMORY found inside a docs dir; keep explicit file entries
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			for _, c := range chunkMarkdown(data) {
				toks := tokenize(c.Heading + " " + c.Body)
				docs = append(docs, chunkDoc{path, c.Heading, c.Body, c.Line, toks})
				seen := map[string]bool{}
				for _, t := range toks {
					if !seen[t] {
						df[t]++
						seen[t] = true
					}
				}
			}
			return nil
		})
	}
	qterms := tokenize(query)
	if len(docs) == 0 || len(qterms) == 0 {
		return nil
	}
	n := float64(len(docs))
	totalLen := 0
	for _, d := range docs {
		totalLen += len(d.toks)
	}
	avgdl := float64(totalLen) / n
	const k1, b = 1.5, 0.75

	type scored struct {
		d     chunkDoc
		score float64
	}
	var ranked []scored
	for _, d := range docs {
		tf := map[string]int{}
		for _, t := range d.toks {
			tf[t]++
		}
		var s float64
		for _, q := range qterms {
			f := float64(tf[q])
			if f == 0 {
				continue
			}
			idf := math.Log(1 + (n-float64(df[q])+0.5)/(float64(df[q])+0.5))
			dl := float64(len(d.toks))
			s += idf * (f * (k1 + 1)) / (f + k1*(1-b+b*dl/avgdl))
		}
		if s > 0 {
			ranked = append(ranked, scored{d, s})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	var out []DocMatch
	for _, r := range ranked {
		out = append(out, DocMatch{File: r.d.file, Heading: r.d.heading, Text: r.d.body, Line: r.d.line})
	}
	return out
}

// fuseRRF combines two ranked lists via reciprocal-rank fusion (k0=60).
// A chunk ranked highly in BOTH lanes wins. Dense matches are preferred as the
// representative so the displayed similarity stays a real cosine.
func fuseRRF(dense, lexical []DocMatch, cap int) []DocMatch {
	const k0 = 60.0
	score := map[string]float64{}
	rep := map[string]DocMatch{}
	var order []string
	add := func(list []DocMatch, isDense bool) {
		for i, d := range list {
			key := d.File + "\x00" + d.Heading
			if _, ok := rep[key]; !ok {
				rep[key] = d
				order = append(order, key)
			} else if isDense {
				rep[key] = d // dense overwrites lexical rep (carries cosine)
			}
			score[key] += 1.0 / (k0 + float64(i+1))
		}
	}
	add(lexical, false)
	add(dense, true)
	sort.SliceStable(order, func(i, j int) bool { return score[order[i]] > score[order[j]] })
	var out []DocMatch
	for _, key := range order {
		out = append(out, rep[key])
		if len(out) >= cap {
			break
		}
	}
	return out
}

// hybridDocsSearch fuses the dense (semantic) and lexical (BM25) lanes via RRF.
// Dense owns multi-word NL queries; lexical owns exact-term/identifier queries.
// Degrades to lexical-only when ollama/index is unavailable.
func hybridDocsSearch(query, repoRoot string, dirs []string, cap int) ([]DocMatch, error) {
	dense, err := semanticDocsSearch(query, repoRoot, dirs, 50)
	if err != nil {
		dense = nil // e.g. ollama down — fall back to lexical-only
	}
	lexical := lexicalDocsSearch(query, dirs, 50)
	return fuseRRF(dense, lexical, cap), nil
}

// collectDocs is the dispatcher main calls. Returns (nil, "none") on any miss
// (not in a repo, no doc dir, lane empty) — the docs block is simply absent.
func collectDocs(cwd, pattern string, semantic bool, maxResults int) ([]DocMatch, string) {
	root, dirs, ok := discoverDocsDir(cwd)
	if !ok {
		return nil, "none"
	}
	cap := docsCap(maxResults)
	if semantic {
		docs, _ := hybridDocsSearch(pattern, root, dirs, cap)
		if len(docs) == 0 {
			return nil, "none"
		}
		return docs, "hybrid"
	}
	docs, err := regexDocsSearch("(?i)"+pattern, dirs, cap)
	if err != nil || len(docs) == 0 {
		return nil, "none"
	}
	return docs, "regex"
}
