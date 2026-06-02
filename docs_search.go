package main

import (
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
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			for _, c := range chunkMarkdown(data) {
				if re.MatchString(c.Heading) || re.MatchString(c.Body) {
					out = append(out, DocMatch{File: path, Heading: c.Heading, Text: c.Body})
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
			File: c.e.FilePath, Heading: c.e.Heading, Text: c.e.Preview, Similarity: c.sim,
		})
	}
	return out, nil
}

// collectDocs is the dispatcher main calls. Returns (nil, "none") on any miss
// (not in a repo, no doc dir, lane error/empty) — the docs block is simply absent.
func collectDocs(cwd, pattern string, semantic bool, maxResults int) ([]DocMatch, string) {
	root, dirs, ok := discoverDocsDir(cwd)
	if !ok {
		return nil, "none"
	}
	cap := docsCap(maxResults)
	if semantic {
		docs, err := semanticDocsSearch(pattern, root, dirs, cap)
		if err != nil || len(docs) == 0 {
			return nil, "none"
		}
		return docs, "semantic"
	}
	docs, err := regexDocsSearch("(?i)"+pattern, dirs, cap)
	if err != nil || len(docs) == 0 {
		return nil, "none"
	}
	return docs, "regex"
}
