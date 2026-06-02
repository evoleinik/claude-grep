package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// recentDistinctQueries returns up to maxN distinct search patterns from the
// usage log, most-recent first.
func recentDistinctQueries(maxN int) []string {
	f, err := os.Open(usageLogPath())
	if err != nil {
		return nil
	}
	defer f.Close()

	var all []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		var ev UsageEvent
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Pattern != "" {
			all = append(all, ev.Pattern)
		}
	}

	seen := map[string]bool{}
	var out []string
	for i := len(all) - 1; i >= 0; i-- {
		p := all[i]
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
		if maxN > 0 && len(out) >= maxN {
			break
		}
	}
	return out
}

// runMineDocsQueries proposes labeled bench cases from real usage. For each
// recent query that THIS repo's docs can answer, it emits the top-hit file as a
// candidate expect_file. Output is review-ready JSON — the human verifies labels
// before appending to bench/docs-queries.json. Non-interactive, AX-friendly.
func runMineDocsQueries(limit int) {
	cwd, _ := os.Getwd()
	if _, _, ok := discoverDocsDir(cwd); !ok {
		fmt.Fprintln(os.Stderr, "no learnings/ or docs/ dir in this repo")
		os.Exit(1)
	}

	type candidate struct {
		Query      string  `json:"query"`
		ExpectFile string  `json:"expect_file"`
		Similarity float32 `json:"similarity,omitempty"`
		Note       string  `json:"note"`
	}

	var out []candidate
	for _, q := range recentDistinctQueries(300) {
		if len(out) >= limit {
			break
		}
		if len(tokenize(q)) < 2 {
			continue // skip single-token / junk patterns
		}
		docs, engine := collectDocs(cwd, q, true, 100)
		if engine == "none" || len(docs) == 0 {
			continue // not answerable by this repo's docs — skip
		}
		out = append(out, candidate{
			Query:      q,
			ExpectFile: filepath.Base(docs[0].File),
			Similarity: docs[0].Similarity,
			Note:       "auto-proposed top hit — VERIFY expect_file before keeping",
		})
	}

	// High-confidence proposals first; low-signal (lexical-only, sim 0) sink to
	// the bottom for the human reviewer.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Similarity > out[j].Similarity })

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(out)
	fmt.Fprintf(os.Stderr,
		"%d candidates proposed from usage.jsonl — review expect_file, then merge into bench/docs-queries.json\n",
		len(out))
}
