package main

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// ANSI colors
const (
	blue     = "\033[34m"
	green    = "\033[32m"
	yellow   = "\033[33;1m"
	bold     = "\033[1m"
	dim      = "\033[2m"
	reset    = "\033[0m"
	cyan     = "\033[36m"
)

func formatTerminal(matches []Match, opts SearchOpts, highlightRe ...*regexp.Regexp) {
	var re *regexp.Regexp
	if len(highlightRe) > 0 {
		re = highlightRe[0]
	}
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
		fmt.Printf("%s--- %s/%s ---%s\n", bold, g.project, g.sessionID, reset)

		printed := make(map[int]bool)
		for mi, m := range g.matches {
			// Context before
			for _, ctx := range m.ContextBefore {
				if !printed[ctx.MsgIndex] {
					printMessage(ctx, false, m.Similarity, re)
					printed[ctx.MsgIndex] = true
				}
			}

			// The match itself
			if !printed[m.Message.MsgIndex] {
				printMessage(m.Message, true, m.Similarity, re)
				printed[m.Message.MsgIndex] = true
			}

			// Context after
			for _, ctx := range m.ContextAfter {
				if !printed[ctx.MsgIndex] {
					printMessage(ctx, false, m.Similarity, re)
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

func printMessage(msg Message, isMatch bool, similarity float32, highlightRe *regexp.Regexp) {
	tag := blue + "YOU" + reset
	if msg.Role == "assistant" {
		tag = green + "AI " + reset
	}

	text := msg.Text
	maxLen := 200
	if isMatch {
		maxLen = 500
	}

	if len(text) > maxLen {
		if isMatch && highlightRe != nil {
			loc := highlightRe.FindStringIndex(text)
			if loc != nil {
				start := loc[0] - 150
				if start < 0 {
					start = 0
				}
				end := loc[1] + 350
				if end > len(text) {
					end = len(text)
				}
				prefix := ""
				suffix := ""
				if start > 0 {
					prefix = "..."
				}
				if end < len(text) {
					suffix = "..."
				}
				text = prefix + text[start:end] + suffix
			} else {
				text = text[:maxLen] + "..."
			}
		} else {
			text = text[:maxLen] + "..."
		}
	}

	// Highlight match in yellow (for regex matches) or show as-is for semantic
	if isMatch && highlightRe != nil {
		text = highlightRe.ReplaceAllString(text, yellow+"${0}"+reset)
	}

	// Replace newlines with spaces for compact display
	text = strings.ReplaceAll(text, "\n", " ")

	if !isMatch {
		text = dim + text + reset
	}

	marker := " "
	if isMatch {
		marker = ">"
	}

	simStr := ""
	if isMatch && similarity > 0 {
		simStr = fmt.Sprintf(" %s[%.2f]%s", cyan, similarity, reset)
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
