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

func semanticSearch(query, searchPath string, opts SearchOpts) ([]Match, error) {
	// Check ollama
	if !ollamaReachable() {
		return nil, fmt.Errorf("ollama not running — start with: ollama serve")
	}

	// Embed the query
	queryVec, err := embed(query)
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

	type scored struct {
		entry      IndexEntry
		similarity float32
	}

	var candidates []scored

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

		idx := loadIndex(project)
		if len(idx.Entries) == 0 {
			continue
		}

		for _, entry := range idx.Entries {
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
			if sim > 0.3 { // minimum threshold
				candidates = append(candidates, scored{entry: entry, similarity: sim})
			}
		}
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	// Sort by similarity descending
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].similarity > candidates[j].similarity
	})

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

		// Retrieve full text and context from original file
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

		matches = append(matches, m)
	}

	return matches, nil
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
