package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// UsageEvent is one line in the usage log.
type UsageEvent struct {
	Timestamp string `json:"ts"`
	Pattern   string `json:"pattern"`
	Mode      string `json:"mode"` // "regex" or "semantic"
	Flags     string `json:"flags"`
	Results   int    `json:"results"`
	Files     int    `json:"files"`
	Days      int    `json:"days"`
	Scope     string `json:"scope"` // "project" or "all"
	BRE       bool   `json:"bre,omitempty"`
	ExtraArgs bool   `json:"extra_args,omitempty"`
	DurationMs int64 `json:"ms"`
}

func usageLogPath() string {
	return filepath.Join(indexDir(), "usage.jsonl")
}

func logUsage(ev UsageEvent) {
	dir := indexDir()
	os.MkdirAll(dir, 0755)

	f, err := os.OpenFile(usageLogPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return // silent — telemetry must never break the tool
	}
	defer f.Close()

	ev.Timestamp = time.Now().Format(time.RFC3339)
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	f.Write(data)
	f.Write([]byte("\n"))
}

func printUsageStats() {
	f, err := os.Open(usageLogPath())
	if err != nil {
		fmt.Println("no usage data yet")
		return
	}
	defer f.Close()

	var events []UsageEvent
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var ev UsageEvent
		if json.Unmarshal(scanner.Bytes(), &ev) == nil {
			events = append(events, ev)
		}
	}

	if len(events) == 0 {
		fmt.Println("no usage data yet")
		return
	}

	// Filter to last 30 days
	cutoff := time.Now().AddDate(0, 0, -30).Format(time.RFC3339)
	var recent []UsageEvent
	for _, ev := range events {
		if ev.Timestamp >= cutoff {
			recent = append(recent, ev)
		}
	}

	if len(recent) == 0 {
		fmt.Printf("no searches in last 30 days (%d total all-time)\n", len(events))
		return
	}

	// Compute stats
	total := len(recent)
	found := 0
	empty := 0
	breCount := 0
	extraArgCount := 0
	var totalMs int64
	flagCounts := make(map[string]int)
	modeCounts := make(map[string]int)
	var emptyPatterns []string

	for _, ev := range recent {
		if ev.Results > 0 {
			found++
		} else {
			empty++
			emptyPatterns = append(emptyPatterns, ev.Pattern)
		}
		if ev.BRE {
			breCount++
		}
		if ev.ExtraArgs {
			extraArgCount++
		}
		totalMs += ev.DurationMs
		modeCounts[ev.Mode]++
		for _, f := range strings.Fields(ev.Flags) {
			flagCounts[f]++
		}
	}

	fmt.Printf("Last 30 days: %d searches (%d found, %d empty)\n", total, found, empty)
	if total > 0 {
		fmt.Printf("Hit rate: %d%%\n", found*100/total)
		fmt.Printf("Avg latency: %dms\n", totalMs/int64(total))
	}

	// Mode breakdown
	fmt.Println()
	for mode, count := range modeCounts {
		fmt.Printf("  %s: %d (%d%%)\n", mode, count, count*100/total)
	}

	// Agent issues
	if breCount > 0 || extraArgCount > 0 {
		fmt.Println()
		fmt.Println("Agent issues:")
		if breCount > 0 {
			fmt.Printf("  BRE patterns (auto-fixed): %d\n", breCount)
		}
		if extraArgCount > 0 {
			fmt.Printf("  Extra positional args (ignored): %d\n", extraArgCount)
		}
	}

	// Empty patterns (deduped, top 10)
	if len(emptyPatterns) > 0 {
		fmt.Println()
		fmt.Println("Empty search patterns (improvement candidates):")
		counts := make(map[string]int)
		for _, p := range emptyPatterns {
			counts[p]++
		}
		type pc struct {
			pattern string
			count   int
		}
		var sorted []pc
		for p, c := range counts {
			sorted = append(sorted, pc{p, c})
		}
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].count > sorted[j].count })
		for i, s := range sorted {
			if i >= 10 {
				break
			}
			suffix := ""
			if s.count > 1 {
				suffix = fmt.Sprintf(" (%dx)", s.count)
			}
			fmt.Printf("  %q%s\n", s.pattern, suffix)
		}
	}

	// Top flags
	if len(flagCounts) > 0 {
		fmt.Println()
		fmt.Println("Flag frequency:")
		type fc struct {
			flag  string
			count int
		}
		var sorted []fc
		for f, c := range flagCounts {
			sorted = append(sorted, fc{f, c})
		}
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].count > sorted[j].count })
		for i, s := range sorted {
			if i >= 8 {
				break
			}
			fmt.Printf("  %s %d%%\n", s.flag, s.count*100/total)
		}
	}

	// Retry chains: consecutive empty→found within same minute
	chains := 0
	totalRetries := 0
	for i := 1; i < len(recent); i++ {
		if recent[i-1].Results == 0 && recent[i].Results > 0 {
			// Check if within 2 minutes (likely same agent session)
			t1, e1 := time.Parse(time.RFC3339, recent[i-1].Timestamp)
			t2, e2 := time.Parse(time.RFC3339, recent[i].Timestamp)
			if e1 == nil && e2 == nil && t2.Sub(t1) < 2*time.Minute {
				chains++
				// Count how many empties preceded this success
				retries := 1
				for j := i - 2; j >= 0; j-- {
					if recent[j].Results == 0 {
						t0, e0 := time.Parse(time.RFC3339, recent[j].Timestamp)
						if e0 == nil && t1.Sub(t0) < 2*time.Minute {
							retries++
							continue
						}
					}
					break
				}
				totalRetries += retries
			}
		}
	}
	if chains > 0 {
		fmt.Println()
		fmt.Printf("Retry chains: %d detected (avg %.1f retries before success)\n",
			chains, float64(totalRetries)/float64(chains))
	}
}
