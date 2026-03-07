package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// searchQuery holds the current search pattern for BM25 compression.
// Set by main before calling format functions.
var searchQuery string

func formatTerminal(matches []Match, opts SearchOpts) {
	// Group matches by session
	type sessionGroup struct {
		project   string
		sessionID string
		matches   []Match
	}

	groups := make(map[string]*sessionGroup)
	var order []string

	for _, m := range matches {
		key := m.Message.Project + "/" + m.Message.SessionID
		if _, ok := groups[key]; !ok {
			groups[key] = &sessionGroup{
				project:   m.Message.Project,
				sessionID: m.Message.SessionID,
			}
			order = append(order, key)
		}
		groups[key].matches = append(groups[key].matches, m)
	}

	if opts.ListOnly {
		for _, key := range order {
			g := groups[key]
			for _, m := range g.matches {
				fmt.Printf("%s  %s\n", m.Message.SessionID, m.Message.Timestamp)
			}
		}
		return
	}

	for i, key := range order {
		if i > 0 {
			fmt.Println()
		}
		g := groups[key]
		fmt.Printf("--- %s/%s ---\n", g.project, g.sessionID)

		printed := make(map[int]bool)
		for mi, m := range g.matches {
			// Context before
			for _, ctx := range m.ContextBefore {
				if !printed[ctx.MsgIndex] {
					printMessage(ctx, false, m.Similarity)
					printed[ctx.MsgIndex] = true
				}
			}

			// The match itself
			if !printed[m.Message.MsgIndex] {
				printMessage(m.Message, true, m.Similarity)
				printed[m.Message.MsgIndex] = true
			}

			// Context after
			for _, ctx := range m.ContextAfter {
				if !printed[ctx.MsgIndex] {
					printMessage(ctx, false, m.Similarity)
					printed[ctx.MsgIndex] = true
				}
			}

			// Separator between match groups
			if (opts.Before > 0 || opts.After > 0) && mi < len(g.matches)-1 {
				fmt.Println("  --")
			}
		}
	}
}

func printMessage(msg Message, isMatch bool, similarity float32) {
	tag := "YOU"
	if msg.Role == "assistant" {
		tag = "AI "
	}

	text := msg.Text
	maxLen := 200
	if isMatch {
		maxLen = 500
	}

	if len(text) > maxLen {
		if isMatch && searchQuery != "" {
			text = bm25Compress(text, searchQuery, maxLen)
		} else {
			text = text[:maxLen] + "..."
		}
	}

	// Replace newlines with spaces for compact display
	text = strings.ReplaceAll(text, "\n", " ")

	marker := " "
	if isMatch {
		marker = ">"
	}

	simStr := ""
	if isMatch && similarity > 0 {
		simStr = fmt.Sprintf(" [%.2f]", similarity)
	}

	fmt.Printf("  %s %s [%s]%s %s\n", marker, msg.Timestamp, tag, simStr, text)
}

// JSONMatch is the JSON output structure.
type JSONMatch struct {
	Session       string     `json:"session"`
	Project       string     `json:"project"`
	Timestamp     string     `json:"timestamp"`
	Role          string     `json:"role"`
	Text          string     `json:"text"`
	Similarity    float32    `json:"similarity,omitempty"`
	ContextBefore []JSONCtx  `json:"context_before,omitempty"`
	ContextAfter  []JSONCtx  `json:"context_after,omitempty"`
}

type JSONCtx struct {
	Timestamp string `json:"timestamp"`
	Role      string `json:"role"`
	Text      string `json:"text"`
}

func formatJSON(matches []Match, w io.Writer) {
	var out []JSONMatch
	for _, m := range matches {
		jm := JSONMatch{
			Session:    m.Message.SessionID,
			Project:    m.Message.Project,
			Timestamp:  m.Message.Timestamp,
			Role:       m.Message.Role,
			Text:       m.Message.Text,
			Similarity: m.Similarity,
		}
		for _, ctx := range m.ContextBefore {
			jm.ContextBefore = append(jm.ContextBefore, JSONCtx{
				Timestamp: ctx.Timestamp,
				Role:      ctx.Role,
				Text:      ctx.Text,
			})
		}
		for _, ctx := range m.ContextAfter {
			jm.ContextAfter = append(jm.ContextAfter, JSONCtx{
				Timestamp: ctx.Timestamp,
				Role:      ctx.Role,
				Text:      ctx.Text,
			})
		}
		out = append(out, jm)
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(out)
}
