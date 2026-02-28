package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	ollamaURL     = "http://localhost:11434/api/embed"
	embedModel    = "nomic-embed-text"
	maxEmbedChars = 2048
	previewLen    = 200
)

func runIndex(reindexAll bool) {
	// Check ollama is running
	if !ollamaReachable() {
		fmt.Fprintln(os.Stderr, "error: ollama not running — start with: ollama serve")
		os.Exit(2)
	}

	home, _ := os.UserHomeDir()
	projectsDir := filepath.Join(home, ".claude", "projects")

	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot read %s: %v\n", projectsDir, err)
		os.Exit(2)
	}

	totalNew := 0
	totalSkipped := 0

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		project := e.Name()
		projectPath := filepath.Join(projectsDir, project)

		idx := loadIndex(project)
		if reindexAll {
			idx = &Index{Files: make(map[string]FileMetadata), Project: project}
		}

		// Find JSONL files
		var files []string
		filepath.Walk(projectPath, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".jsonl") {
				return nil
			}
			files = append(files, path)
			return nil
		})

		for _, fpath := range files {
			info, err := os.Stat(fpath)
			if err != nil {
				continue
			}

			// Check if already indexed (and not modified)
			if meta, ok := idx.Files[fpath]; ok {
				if !info.ModTime().After(meta.LastModified) {
					totalSkipped++
					continue
				}
				// File modified — remove old entries for this file
				idx.Entries = removeEntriesForFile(idx.Entries, fpath)
			}

			// Parse and index
			data, err := os.ReadFile(fpath)
			if err != nil {
				continue
			}

			messages := parseJSONL(fpath, data)
			if len(messages) == 0 {
				continue
			}

			sessionID := extractSessionID(fpath)
			fmt.Fprintf(os.Stderr, "indexing: %s/%s (%d messages)\n", project, sessionID, len(messages))

			for _, msg := range messages {
				text := msg.Text
				if len(text) > maxEmbedChars {
					text = text[:maxEmbedChars]
				}

				vec, err := embed(text)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  embed error: %v\n", err)
					continue
				}

				preview := msg.Text
				if len(preview) > previewLen {
					preview = preview[:previewLen]
				}

				idx.Entries = append(idx.Entries, IndexEntry{
					SessionID: msg.SessionID,
					MsgIndex:  msg.MsgIndex,
					Role:      msg.Role,
					Timestamp: msg.Timestamp,
					Preview:   preview,
					FilePath:  fpath,
					Vector:    vec,
				})
			}

			idx.Files[fpath] = FileMetadata{
				FilePath:     fpath,
				LastModified: info.ModTime(),
			}
			totalNew++
		}

		if err := saveIndex(idx); err != nil {
			fmt.Fprintf(os.Stderr, "error saving index for %s: %v\n", project, err)
		}
	}

	fmt.Fprintf(os.Stderr, "done: %d files indexed, %d skipped (unchanged)\n", totalNew, totalSkipped)
}

func printIndexStatus(allProjects bool) {
	stats := getIndexStats()
	if stats.Projects == 0 {
		fmt.Println("no index — run: claude-grep --index")
		return
	}
	fmt.Printf("projects: %d\n", stats.Projects)
	fmt.Printf("files:    %d\n", stats.Files)
	fmt.Printf("vectors:  %d\n", stats.Vectors)
	fmt.Printf("size:     %s\n", formatSize(stats.SizeBytes))
}

func removeEntriesForFile(entries []IndexEntry, fpath string) []IndexEntry {
	var kept []IndexEntry
	for _, e := range entries {
		if e.FilePath != fpath {
			kept = append(kept, e)
		}
	}
	return kept
}

type embedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

func embed(text string) ([]float32, error) {
	body, err := json.Marshal(embedRequest{Model: embedModel, Input: text})
	if err != nil {
		return nil, err
	}

	resp, err := http.Post(ollamaURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("ollama returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result embedResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	if len(result.Embeddings) == 0 {
		return nil, fmt.Errorf("no embeddings returned")
	}

	return result.Embeddings[0], nil
}

func ollamaReachable() bool {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://localhost:11434/")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}
