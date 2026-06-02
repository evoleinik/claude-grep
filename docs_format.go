package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// printDocsBlock renders the trailing curated-docs section.
// label is the human dir name (e.g. "learnings/").
func printDocsBlock(label string, docs []DocMatch) {
	if len(docs) == 0 {
		return
	}
	fmt.Printf("\n=== curated docs (%s) ===\n", label)
	for _, d := range docs {
		sim := ""
		if d.Similarity > 0 {
			sim = fmt.Sprintf("   [%.2f]", d.Similarity)
		}
		fmt.Printf("%s § %s%s\n", filepath.Base(d.File), d.Heading, sim)

		body := d.Text
		const budget = 400
		if len(body) > budget {
			if searchQuery != "" {
				body = bm25Compress(body, searchQuery, budget)
			} else {
				body = body[:budget] + "..."
			}
		}
		fmt.Printf("  %s\n", strings.ReplaceAll(strings.TrimSpace(body), "\n", " "))
	}
}

// docsLabel turns an absolute doc dir into a display label, e.g. "learnings/".
func docsLabel(dir string) string { return filepath.Base(dir) + "/" }

// docsToJSON converts doc hits into JSONMatch entries tagged source="docs",
// so they live in the same output array as session matches.
func docsToJSON(docs []DocMatch) []JSONMatch {
	var out []JSONMatch
	for _, d := range docs {
		out = append(out, JSONMatch{
			Source: "docs", File: d.File, Heading: d.Heading,
			Text: d.Text, Similarity: d.Similarity,
		})
	}
	return out
}
