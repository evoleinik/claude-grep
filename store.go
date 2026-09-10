package main

import (
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// IndexEntry holds a single embedded message (chat) or markdown chunk (docs).
type IndexEntry struct {
	SessionID string
	MsgIndex  int
	Role      string
	Timestamp string
	Preview   string // first 200 chars of text
	FilePath  string
	Vector    []float32
	Source    string // "" or "session" for chat; "docs" for markdown chunks
	Heading   string // markdown heading path for doc chunks; "" for chat
	Line      int    // 1-based heading line for doc chunks; 0 for chat
}

// FileMetadata tracks which files have been indexed.
type FileMetadata struct {
	FilePath     string
	LastModified time.Time
	// Messages and Size make indexing INCREMENTAL. Transcripts are append-only,
	// so a changed file usually just has new messages at the end; re-embedding
	// the whole thing costs more every time the session grows. Size guards the
	// assumption: if a file shrank, it was rewritten rather than appended to,
	// and the file is rebuilt from scratch.
	Messages int
	Size     int64
}

// Index is the in-memory representation of a project's vector index.
type Index struct {
	Entries         []IndexEntry
	Files           map[string]FileMetadata // keyed by filepath
	Project         string
	DocEmbedVersion string // docs index only: embed-logic version; mismatch forces full rebuild
	EmbedModel      string // session index: model that produced Vector; mismatch forces full rebuild
}

// IndexStats holds aggregate index statistics.
type IndexStats struct {
	Projects  int
	Files     int
	Vectors   int
	SizeBytes int64
}

func indexDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "search-index")
}

func indexPath(project string) string {
	return filepath.Join(indexDir(), project+".gob")
}

func loadIndex(project string) *Index {
	idx := &Index{
		Files:   make(map[string]FileMetadata),
		Project: project,
	}

	f, err := os.Open(indexPath(project))
	if err != nil {
		return idx
	}
	defer f.Close()

	dec := gob.NewDecoder(f)
	if err := dec.Decode(idx); err != nil {
		return &Index{Files: make(map[string]FileMetadata), Project: project}
	}
	normalizeIndexPaths(idx)
	return idx
}

// normalizeIndexPaths rewrites stored file paths onto THIS machine's projects
// dir. Indexes are built on GPU workers, where the same transcripts live under
// /tmp/s<n>/.claude/projects, so a path stored there resolves to nothing back
// here. Two things broke silently because of it: context retrieval (-C/-A/-B)
// re-reads Entry.FilePath and found no file, so it returned the match with no
// surrounding lines; and the incremental-index metadata is keyed by path, so it
// never matched and every cron pass rebuilt every file from scratch.
//
// Done at LOAD so existing indexes repair themselves without a rebuild. The
// next saveIndex writes the corrected paths back.
func normalizeIndexPaths(idx *Index) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	base := filepath.Join(home, ".claude", "projects")
	const marker = "/.claude/projects/"
	fix := func(p string) string {
		if i := strings.Index(p, marker); i >= 0 {
			return filepath.Join(base, p[i+len(marker):])
		}
		return p
	}
	for i := range idx.Entries {
		idx.Entries[i].FilePath = fix(idx.Entries[i].FilePath)
	}
	if len(idx.Files) > 0 {
		fixed := make(map[string]FileMetadata, len(idx.Files))
		for k, m := range idx.Files {
			m.FilePath = fix(m.FilePath)
			fixed[fix(k)] = m
		}
		idx.Files = fixed
	}
}

// saveIndexRaw writes an index verbatim. Only tests need it: they must be able
// to plant a foreign path that loadIndex is then expected to repair.
func saveIndexRaw(idx *Index) error { return saveIndex(idx) }

func saveIndex(idx *Index) error {
	dir := indexDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	f, err := os.Create(indexPath(idx.Project))
	if err != nil {
		return err
	}
	defer f.Close()

	return gob.NewEncoder(f).Encode(idx)
}

func getIndexStats() IndexStats {
	var stats IndexStats
	dir := indexDir()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return stats
	}

	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".gob" {
			continue
		}
		stats.Projects++

		info, err := e.Info()
		if err != nil {
			continue
		}
		stats.SizeBytes += info.Size()

		project := e.Name()[:len(e.Name())-4]
		idx := loadIndex(project)
		stats.Files += len(idx.Files)
		stats.Vectors += len(idx.Entries)
	}

	return stats
}

func formatSize(bytes int64) string {
	switch {
	case bytes >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(1<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
