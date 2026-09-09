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
	embedModel    = "embeddinggemma"
	maxEmbedChars = 2048
	previewLen    = 200

	// embeddinggemma is asymmetric: query and document take DIFFERENT prefixes.
	// Dropping them is not cosmetic, it costs most of the model's quality.
	//
	// Measured over 300 random distractors on 4 labeled queries — gold-rank MRR,
	// then embed throughput on this box (batching gives no speedup, ollama
	// serializes internally):
	//   nomic-embed-text, no prefix (the old setup)  0.433   3.6/s
	//   nomic-embed-text + its own prefixes          0.675   3.6/s
	//   embeddinggemma + its prefixes                0.708  11.2/s
	//   mxbai-embed-large + its prefix               0.786   1.8/s
	// mxbai ranks best but costs ~31h to reindex 197k messages and adds 0.56s to
	// every query. embeddinggemma is 64% better than the old setup, indexes in
	// ~5h, and keeps query embedding at 0.13s.
	embedQueryPrefix = "task: search result | query: "
	embedDocPrefix   = "title: none | text: "

	// chunkChars splits a message before embedding. A whole message embeds to
	// ONE vector, so a query matching a single sentence is diluted by the rest.
	// Swept on the 17-query corpus (bench/model-bakeoff.py --chunk-chars):
	//   whole message  hit@1  9  MRR 0.608
	//   256 chars      hit@1  9  MRR 0.664
	//   512 chars      hit@1 12  MRR 0.734   <- chosen
	//   1024 chars     hit@1 10  MRR 0.635
	// Re-sweep before changing it; the optimum is not monotonic.
	chunkChars = 512

	// indexVersion is appended to the model stamp so a CHUNKING change forces a
	// rebuild too, not just a model change. Without it, re-chunked and
	// whole-message vectors would silently coexist in one ranking.
	indexVersion = "c512t" // t = tool output indexed
)

// indexStamp identifies what produced the vectors. Model AND chunking, because
// either one changing makes existing vectors incomparable.
var indexStamp = embedModel + "/" + indexVersion

// splitForEmbedding cuts text into ~chunkChars pieces, preferring a sentence
// or newline boundary so a chunk stays a coherent thought rather than a fixed
// slice. Written by hand because Go's RE2 has no lookbehind.
func splitForEmbedding(text string) []string {
	if len(text) <= chunkChars {
		return []string{text}
	}
	var out []string
	start := 0
	for start < len(text) {
		end := start + chunkChars
		if end >= len(text) {
			out = append(out, strings.TrimSpace(text[start:]))
			break
		}
		// Walk back to the last sentence end or newline inside this window,
		// but never give up more than a third of it chasing one.
		cut := -1
		for i := end; i > start+chunkChars/3; i-- {
			switch text[i] {
			case '\n':
				cut = i + 1
			case '.', '!', '?':
				if i+1 < len(text) && (text[i+1] == ' ' || text[i+1] == '\n') {
					cut = i + 1
				}
			}
			if cut > 0 {
				break
			}
		}
		if cut <= start {
			cut = end // no boundary found: hard cut rather than emit nothing
		}
		if piece := strings.TrimSpace(text[start:cut]); piece != "" {
			out = append(out, piece)
		}
		start = cut
	}
	if len(out) == 0 {
		return []string{text}
	}
	return out
}

func lockPath() string {
	return filepath.Join(indexDir(), "index.lock")
}

func acquireLock() bool {
	path := lockPath()
	os.MkdirAll(filepath.Dir(path), 0755)

	// Check for stale lock (older than 2 hours)
	if info, err := os.Stat(path); err == nil {
		if time.Since(info.ModTime()) > 2*time.Hour {
			os.Remove(path)
		} else {
			return false
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return false
	}
	fmt.Fprintf(f, "%d", os.Getpid())
	f.Close()
	return true
}

func releaseLock() {
	os.Remove(lockPath())
}

func runIndex(reindexAll bool) {
	if !acquireLock() {
		fmt.Fprintln(os.Stderr, "indexing already in progress")
		return
	}
	defer releaseLock()

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
		// A vector is only comparable to others from the same model, and the dims
		// differ across models, so a model change invalidates the whole project.
		if reindexAll || idx.EmbedModel != indexStamp {
			idx = &Index{Files: make(map[string]FileMetadata), Project: project}
		}
		idx.EmbedModel = indexStamp

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
			startAt := 0
			if meta, ok := idx.Files[fpath]; ok {
				if !info.ModTime().After(meta.LastModified) {
					totalSkipped++
					continue
				}
				// Append-only: keep what is already indexed and start after it.
				// A shrunken file was rewritten, not appended to, so it cannot
				// be trusted for incremental work and is rebuilt.
				if meta.Messages > 0 && info.Size() >= meta.Size {
					startAt = meta.Messages
				} else {
					idx.Entries = removeEntriesForFile(idx.Entries, fpath)
				}
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
			if startAt > len(messages) {
				// Fewer messages than last time despite a bigger file: the parse
				// changed under us. Rebuild rather than guess.
				idx.Entries = removeEntriesForFile(idx.Entries, fpath)
				startAt = 0
			}
			newMsgs := messages[startAt:]
			if len(newMsgs) == 0 {
				idx.Files[fpath] = FileMetadata{FilePath: fpath, LastModified: info.ModTime(),
					Messages: len(messages), Size: info.Size()}
				totalSkipped++
				continue
			}
			fmt.Fprintf(os.Stderr, "indexing: %s/%s (%d new of %d messages)\n",
				project, sessionID, len(newMsgs), len(messages))

			for _, msg := range newMsgs {
				text := msg.Text
				if len(text) > maxEmbedChars {
					text = text[:maxEmbedChars]
				}

				// One vector per CHUNK, not per message. The preview follows the
				// chunk it belongs to, so a hit shows the matching passage rather
				// than whatever happened to open the message.
				for _, piece := range splitForEmbedding(text) {
					vec, err := embedDoc(piece)
					if err != nil {
						fmt.Fprintf(os.Stderr, "  embed error: %v\n", err)
						continue
					}

					preview := piece
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
			}

			idx.Files[fpath] = FileMetadata{
				FilePath:     fpath,
				LastModified: info.ModTime(),
				Messages:     len(messages),
				Size:         info.Size(),
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
	// Check if indexing is running
	if info, err := os.Stat(lockPath()); err == nil {
		pid := ""
		if data, err := os.ReadFile(lockPath()); err == nil {
			pid = string(data)
		}
		fmt.Printf("status:   indexing (pid %s, started %s ago)\n", pid, time.Since(info.ModTime()).Truncate(time.Second))
	} else {
		fmt.Printf("status:   idle\n")
	}

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

// embedQuery and embedDoc are the two sides of an asymmetric embedding model.
// Always go through one of them; embedRaw is the transport, not an entry point.
func embedQuery(text string) ([]float32, error) { return embedRaw(embedQueryPrefix + text) }
func embedDoc(text string) ([]float32, error)   { return embedRaw(embedDocPrefix + text) }

func embedRaw(text string) ([]float32, error) {
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
