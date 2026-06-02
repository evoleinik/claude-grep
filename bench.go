package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"
)

type BenchRecord struct {
	Query   string `json:"query"`
	Found   bool   `json:"found"`
	Layer   string `json:"layer"`
	Results int    `json:"results"`
	Ms      int64  `json:"ms"`
}

// runBenchRecords runs every corpus query through the live recovery ladder at a
// fixed scope. searchDir is the projects root (overridable for tests). No telemetry.
func runBenchRecords(corpusPath, searchDir string) []BenchRecord {
	data, err := os.ReadFile(corpusPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bench: cannot read %s: %v\n", corpusPath, err)
		os.Exit(2)
	}
	var queries []string
	if err := json.Unmarshal(data, &queries); err != nil {
		fmt.Fprintf(os.Stderr, "bench: %s must be a JSON array of strings: %v\n", corpusPath, err)
		os.Exit(2)
	}

	opts := SearchOpts{Role: "both", MaxResults: 100, MaxDays: 30, ExcludeSelf: false}
	recs := make([]BenchRecord, 0, len(queries))
	for _, q := range queries {
		start := time.Now()
		matches, _, layer, err := searchWithRecovery(q, searchDir, opts, true)
		ms := time.Since(start).Milliseconds()
		if err != nil {
			layer = "error"
		}
		recs = append(recs, BenchRecord{
			Query: q, Found: len(matches) > 0, Layer: layer,
			Results: len(matches), Ms: ms,
		})
	}
	return recs
}

// runBench is the CLI entry: records to stdout (bare JSON array), summary to stderr.
func runBench(corpusPath string) {
	searchDir, err := resolveSearchPath(true) // benchmark always runs at all-projects scope
	if err != nil {
		fmt.Fprintf(os.Stderr, "bench: %v\n", err)
		os.Exit(2)
	}
	recs := runBenchRecords(corpusPath, searchDir)

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(recs)

	// Aggregate footer → stderr (data stays clean on stdout).
	found, byLayer := 0, map[string]int{}
	lat := make([]int, 0, len(recs))
	for _, r := range recs {
		if r.Found {
			found++
		}
		byLayer[r.Layer]++
		lat = append(lat, int(r.Ms))
	}
	sort.Ints(lat)
	p := func(q float64) int {
		if len(lat) == 0 {
			return 0
		}
		return lat[int(q*float64(len(lat)-1))]
	}
	fmt.Fprintf(os.Stderr, "bench: %d/%d found (%d%%); layers=%v; p50=%dms p95=%dms\n",
		found, len(recs), found*100/max(1, len(recs)), byLayer, p(0.5), p(0.95))
}
