package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// runDocsOnly serves --docs-only: it skips the session scan and emits only the
// curated-docs block (≤ docsCap hits), keeping output small enough to survive the
// `head -N` an agent habitually pipes a session search through. Writes the block to
// stdout (or JSON), diagnostics to stderr, and returns the process exit code.
func runDocsOnly(cwd, pattern string, semantic, jsonOut, noDocs bool, maxResults int) int {
	if noDocs {
		fmt.Fprintln(os.Stderr, "error: --docs-only and --no-docs are contradictory")
		return 2
	}
	_, dirs, ok := discoverDocsDir(cwd)
	if !ok || len(dirs) == 0 {
		fmt.Fprintf(os.Stderr, "no curated docs in %s (no learnings//docs/ dir, README.md, CLAUDE.md, or MEMORY.md)\n", cwd)
		return 1
	}
	searchQuery = pattern // drives bm25Compress in printDocsBlock
	start := time.Now()
	docs, engine := collectDocs(cwd, pattern, semantic, maxResults)
	flags := "--docs-only"
	if semantic {
		flags += " -s"
	}
	if jsonOut {
		flags += " --json"
	}
	logUsage(UsageEvent{
		Pattern: pattern, Mode: "docs-only", Flags: flags,
		Scope: "docs", DurationMs: time.Since(start).Milliseconds(),
		DocsResults: len(docs), DocsEngine: engine,
	})
	if len(docs) == 0 {
		fmt.Fprintf(os.Stderr, "no curated-docs match for %q in %s\n", pattern, docsLabel(dirs[0]))
		return 1
	}
	if jsonOut {
		encodeJSON(docsToJSON(docs), os.Stdout)
	} else {
		printDocsBlock(docsLabel(dirs[0]), docs)
	}
	return 0
}

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
		loc := filepath.Base(d.File)
		if d.Line > 0 {
			loc = fmt.Sprintf("%s:%d", loc, d.Line) // navigable file:line pointer
		}
		fmt.Printf("%s § %s%s\n", loc, d.Heading, sim)

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
// An explicitly-listed file entry (root README/CLAUDE) has no dir, so label it
// generically rather than printing "CLAUDE.md/".
func docsLabel(dir string) string {
	if strings.HasSuffix(strings.ToLower(dir), ".md") {
		return "repo docs"
	}
	return filepath.Base(dir) + "/"
}

// docsToJSON converts doc hits into JSONMatch entries tagged source="docs",
// so they live in the same output array as session matches.
func docsToJSON(docs []DocMatch) []JSONMatch {
	var out []JSONMatch
	for _, d := range docs {
		out = append(out, JSONMatch{
			Source: "docs", File: d.File, Heading: d.Heading,
			Text: d.Text, Similarity: d.Similarity, Line: d.Line,
		})
	}
	return out
}
