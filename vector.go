package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// simFloor is the minimum cosine for a candidate. It is MODEL-SPECIFIC and the
// scales are not comparable: 0.55 was derived for nomic-embed-text, mxbai sits
// near 0.62, embeddinggemma near 0.35. Carrying a floor across a model change
// silently guts recall or floods it with noise, so re-measure on every change.
//
// Measured for embeddinggemma over 1200 query/distractor pairs plus 4 known-good
// golds on the real index:
//
//	distractors p50 0.186, p90 0.278, p99 0.400, max 0.533
//	gold        min 0.373, max 0.503
//
// Gold overlaps the distractor tail here, so the floor cannot separate on its own
// — ranking does that, and the floor only stops a no-match query returning junk.
// 0.35 sits under every measured gold and above the p90 of noise.
const simFloor = 0.35

type scored struct {
	entry      IndexEntry
	similarity float32
}

func semanticSearch(query, searchPath string, opts SearchOpts) ([]Match, error) {
	// Check ollama
	if !ollamaReachable() {
		return nil, fmt.Errorf("ollama not running — start with: ollama serve")
	}

	// Embed the query
	queryVec, err := embedQuery(query)
	if err != nil {
		return nil, fmt.Errorf("failed to embed query: %w", err)
	}

	// Load relevant indexes
	dir := indexDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("no index — run: claude-grep --index")
	}

	// Determine which projects to search
	home, _ := os.UserHomeDir()
	projectsBase := filepath.Join(home, ".claude", "projects")
	searchAll := searchPath == projectsBase

	// Time filter
	var cutoff time.Time
	if opts.MaxDays > 0 {
		cutoff = time.Now().AddDate(0, 0, -opts.MaxDays)
	}

	var candidates []scored
	staleModel := false
	archivedSkipped := 0

	// Find the current session file to exclude
	var excludeFile string
	if opts.ExcludeSelf {
		excludeFile = findNewestSessionFile(searchPath)
	}

	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".gob" {
			continue
		}
		project := e.Name()[:len(e.Name())-4]

		// Filter by project if not searching all
		if !searchAll {
			projectDir := filepath.Join(projectsBase, project)
			if !strings.HasPrefix(projectDir, searchPath) && projectDir != searchPath {
				continue
			}
		}

		if !opts.IncludeArchived {
			if _, statErr := os.Stat(filepath.Join(projectsBase, project)); statErr != nil {
				archivedSkipped++
				continue
			}
		}
		idx := loadIndex(project)
		if len(idx.Entries) == 0 {
			continue
		}
		// Vectors from another model are not comparable. nomic-embed-text and
		// embeddinggemma are BOTH 768-dim, so cosine does not error or return
		// 0 — it returns plausible-looking garbage. That is worse than a hard
		// failure, which is why this stamp is load-bearing rather than a nicety.
		if idx.EmbedModel != indexStamp {
			staleModel = true
			continue
		}

		for _, entry := range idx.Entries {
			// Skip current session
			if excludeFile != "" && entry.FilePath == excludeFile {
				continue
			}
			// Role filter
			if opts.Role != "both" && entry.Role != opts.Role {
				continue
			}

			// Time filter
			if !cutoff.IsZero() && entry.Timestamp != "" {
				t, err := time.Parse("2006-01-02T15:04:05", entry.Timestamp)
				if err == nil && t.Before(cutoff) {
					continue
				}
			}

			sim := cosineSimilarity(queryVec, entry.Vector)
			if sim > simFloor {
				candidates = append(candidates, scored{entry: entry, similarity: sim})
			}
		}
	}

	if len(candidates) == 0 {
		if archivedSkipped > 0 {
			fmt.Fprintf(os.Stderr, "no live matches — %d archived projects (transcripts deleted) were not searched; add --archived\n", archivedSkipped)
		}
		if staleModel {
			return nil, fmt.Errorf("index was built with a different embedding model — rebuild: claude-grep --index --all")
		}
		return nil, nil
	}
	// Some projects answered and others are still on the old model, i.e. a rebuild
	// is in flight. Say so: partial results otherwise read as "not in my history".
	if staleModel {
		fmt.Fprintln(os.Stderr, "note: index rebuild in progress — searching only the projects already rebuilt")
	}

	// Sort by similarity descending
	// Same reason as searchCore: equal similarities must not be ordered by
	// index-iteration order, or the cap cuts arbitrarily between runs.
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].similarity != candidates[j].similarity {
			return candidates[i].similarity > candidates[j].similarity
		}
		if candidates[i].entry.FilePath != candidates[j].entry.FilePath {
			return candidates[i].entry.FilePath < candidates[j].entry.FilePath
		}
		return candidates[i].entry.MsgIndex < candidates[j].entry.MsgIndex
	})

	// Rank SESSIONS by the aggregate of their best chunks, not by their single
	// best chunk. One lucky outlier chunk from an unrelated session otherwise
	// outranks a session that is relevant throughout — and the reader scans
	// sessions, not chunks. Sum of the top-3 rewards sustained relevance while
	// staying fair to short sessions that only have one or two messages.
	candidates = rankBySessionAggregate(candidates)

	// Limit results
	limit := opts.MaxResults
	if limit <= 0 {
		limit = 10
	}
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	// Convert to matches, with lazy context retrieval
	var matches []Match
	for _, c := range candidates {
		msg := Message{
			Role:      c.entry.Role,
			Type:      c.entry.Role,
			Text:      c.entry.Preview,
			Timestamp: c.entry.Timestamp,
			SessionID: c.entry.SessionID,
			Project:   extractProject(c.entry.FilePath),
			FilePath:  c.entry.FilePath,
			MsgIndex:  c.entry.MsgIndex,
		}

		m := Match{
			Message:    msg,
			Similarity: c.similarity,
		}

		// Only re-read file if context requested or preview is empty
		if (opts.Before > 0 || opts.After > 0) || c.entry.Preview == "" {
			if data, err := os.ReadFile(c.entry.FilePath); err == nil {
				allMsgs := parseJSONL(c.entry.FilePath, data)
				// Find matching message by index
				if c.entry.MsgIndex < len(allMsgs) {
					m.Message.Text = allMsgs[c.entry.MsgIndex].Text
				}

				// Context before
				if opts.Before > 0 {
					start := c.entry.MsgIndex - opts.Before
					if start < 0 {
						start = 0
					}
					for j := start; j < c.entry.MsgIndex; j++ {
						if j < len(allMsgs) {
							m.ContextBefore = append(m.ContextBefore, allMsgs[j])
						}
					}
				}

				// Context after
				if opts.After > 0 {
					end := c.entry.MsgIndex + opts.After + 1
					if end > len(allMsgs) {
						end = len(allMsgs)
					}
					for j := c.entry.MsgIndex + 1; j < end; j++ {
						m.ContextAfter = append(m.ContextAfter, allMsgs[j])
					}
				}
			}
		}

		matches = append(matches, m)
	}

	return matches, nil
}

// rankBySessionAggregate reorders chunks so that sessions appear in order of
// their aggregate relevance, best chunk first within each session. Input must
// already be sorted by similarity descending.
func rankBySessionAggregate(in []scored) []scored {
	const topK = 3
	type grp struct {
		key   string
		score float32
		items []scored
	}
	idx := map[string]int{}
	var groups []grp
	for _, c := range in {
		k := c.entry.FilePath + "\x00" + c.entry.SessionID
		i, ok := idx[k]
		if !ok {
			idx[k] = len(groups)
			groups = append(groups, grp{key: k})
			i = len(groups) - 1
		}
		g := &groups[i]
		if len(g.items) < topK {
			g.score += c.similarity
		}
		g.items = append(g.items, c)
	}
	sort.SliceStable(groups, func(a, b int) bool {
		if groups[a].score != groups[b].score {
			return groups[a].score > groups[b].score
		}
		return groups[a].key < groups[b].key // total order, see searchCore
	})
	// Emit at most topK chunks per session. Without this a few large sessions
	// eat the result cap and push whole sessions past it: measured, regrouping
	// alone moved four targets out of the top 100 entirely, one of them from
	// rank 1. The reader wants many sessions with their best lines, not one
	// session's every line.
	out := make([]scored, 0, len(in))
	for _, g := range groups {
		n := len(g.items)
		if n > topK {
			n = topK
		}
		out = append(out, g.items[:n]...)
	}
	return out
}

func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}

	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}

	return float32(dot / denom)
}
