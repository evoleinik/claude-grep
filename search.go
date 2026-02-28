package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Message represents a parsed chat message from a JSONL session file.
type Message struct {
	Role      string
	Type      string // "user" or "assistant"
	Text      string
	Timestamp string
	SessionID string
	Project   string
	FilePath  string
	MsgIndex  int
}

// Match represents a search result with optional context.
type Match struct {
	Message       Message
	ContextBefore []Message
	ContextAfter  []Message
	Similarity    float32 // only for semantic search
}

// SearchOpts holds search parameters.
type SearchOpts struct {
	Role       string // "both", "user", "assistant"
	MaxResults int
	MaxDays    int
	Before     int
	After      int
	ListOnly   bool
}

// regexSearch finds matches across session files using regex.
func regexSearch(pattern, searchPath string, opts SearchOpts) ([]Match, error) {
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return nil, err
	}

	files, err := findSessionFiles(searchPath, opts.MaxDays)
	if err != nil {
		return nil, err
	}

	// Concurrent search with fan-in
	type fileResult struct {
		matches []Match
	}

	results := make(chan fileResult, len(files))
	var wg sync.WaitGroup

	// Limit concurrency
	sem := make(chan struct{}, 8)

	patternBytes := []byte(strings.ToLower(pattern))

	for _, f := range files {
		wg.Add(1)
		go func(fp string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			matches := searchFile(fp, re, patternBytes, opts)
			if len(matches) > 0 {
				results <- fileResult{matches: matches}
			}
		}(f)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var allMatches []Match
	for r := range results {
		allMatches = append(allMatches, r.matches...)
	}

	// Sort by timestamp descending (newest first)
	sort.Slice(allMatches, func(i, j int) bool {
		return allMatches[i].Message.Timestamp > allMatches[j].Message.Timestamp
	})

	// Limit results
	if len(allMatches) > opts.MaxResults {
		allMatches = allMatches[:opts.MaxResults]
	}

	return allMatches, nil
}

// findSessionFiles finds JSONL files modified within maxDays.
func findSessionFiles(searchPath string, maxDays int) ([]string, error) {
	cutoff := time.Now().AddDate(0, 0, -maxDays)
	var files []string

	err := filepath.Walk(searchPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			return nil
		}
		files = append(files, path)
		return nil
	})

	return files, err
}

// searchFile searches a single JSONL file for regex matches.
func searchFile(filepath string, re *regexp.Regexp, patternLower []byte, opts SearchOpts) []Match {
	data, err := os.ReadFile(filepath)
	if err != nil {
		return nil
	}

	// Quick check: does the file even contain the pattern?
	if !bytes.Contains(bytes.ToLower(data), patternLower) {
		return nil
	}

	messages := parseJSONL(filepath, data)
	if len(messages) == 0 {
		return nil
	}

	var matches []Match
	for i, msg := range messages {
		if opts.Role != "both" && msg.Type != opts.Role {
			continue
		}
		if !re.MatchString(msg.Text) {
			continue
		}

		m := Match{Message: msg}

		// Context before
		if opts.Before > 0 {
			start := i - opts.Before
			if start < 0 {
				start = 0
			}
			for j := start; j < i; j++ {
				m.ContextBefore = append(m.ContextBefore, messages[j])
			}
		}

		// Context after
		if opts.After > 0 {
			end := i + opts.After + 1
			if end > len(messages) {
				end = len(messages)
			}
			for j := i + 1; j < end; j++ {
				m.ContextAfter = append(m.ContextAfter, messages[j])
			}
		}

		matches = append(matches, m)
	}

	return matches
}

// parseJSONL parses a JSONL file into messages.
func parseJSONL(fpath string, data []byte) []Message {
	sessionID := extractSessionID(fpath)
	project := extractProject(fpath)

	var messages []Message
	idx := 0

	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(line) == 0 {
			continue
		}

		var raw map[string]json.RawMessage
		if err := json.Unmarshal(line, &raw); err != nil {
			continue
		}

		// Get type field
		var msgType string
		if t, ok := raw["type"]; ok {
			json.Unmarshal(t, &msgType)
		}
		if msgType != "user" && msgType != "assistant" {
			continue
		}

		// Get timestamp
		var timestamp string
		if ts, ok := raw["timestamp"]; ok {
			json.Unmarshal(ts, &timestamp)
			if len(timestamp) > 19 {
				timestamp = timestamp[:19]
			}
		}

		// Get message content
		text := extractText(raw)
		if text == "" {
			continue
		}

		msg := Message{
			Role:      msgType,
			Type:      msgType,
			Text:      text,
			Timestamp: timestamp,
			SessionID: sessionID,
			Project:   project,
			FilePath:  fpath,
			MsgIndex:  idx,
		}

		// Deduplicate: same timestamp+role → keep latest
		if len(messages) > 0 {
			prev := &messages[len(messages)-1]
			if prev.Timestamp == timestamp && prev.Role == msgType {
				*prev = msg
				continue
			}
		}

		messages = append(messages, msg)
		idx++
	}

	// Fix MsgIndex after dedup
	for i := range messages {
		messages[i].MsgIndex = i
	}

	return messages
}

// extractText pulls text content from the message field.
func extractText(raw map[string]json.RawMessage) string {
	// Try message.content first
	var msgObj map[string]json.RawMessage
	if m, ok := raw["message"]; ok {
		if err := json.Unmarshal(m, &msgObj); err != nil {
			// Try data.message
			if d, ok := raw["data"]; ok {
				var dataObj map[string]json.RawMessage
				if err := json.Unmarshal(d, &dataObj); err == nil {
					if dm, ok := dataObj["message"]; ok {
						json.Unmarshal(dm, &msgObj)
					}
				}
			}
		}
	}

	if msgObj == nil {
		return ""
	}

	contentRaw, ok := msgObj["content"]
	if !ok {
		return ""
	}

	// Try as string
	var strContent string
	if err := json.Unmarshal(contentRaw, &strContent); err == nil {
		return strings.TrimSpace(strContent)
	}

	// Try as array of content blocks
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(contentRaw, &blocks); err == nil {
		var texts []string
		for _, block := range blocks {
			var blockType string
			if t, ok := block["type"]; ok {
				json.Unmarshal(t, &blockType)
			}
			if blockType != "text" {
				continue
			}
			var text string
			if t, ok := block["text"]; ok {
				json.Unmarshal(t, &text)
			}
			if text != "" {
				texts = append(texts, text)
			}
		}
		return strings.TrimSpace(strings.Join(texts, " "))
	}

	return ""
}

func extractSessionID(fpath string) string {
	base := filepath.Base(fpath)
	ext := filepath.Ext(base)
	id := strings.TrimSuffix(base, ext)
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func extractProject(fpath string) string {
	return filepath.Base(filepath.Dir(fpath))
}
