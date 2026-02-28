package main

import (
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// IndexEntry holds a single embedded message.
type IndexEntry struct {
	SessionID string
	MsgIndex  int
	Role      string
	Timestamp string
	Preview   string // first 200 chars of text
	FilePath  string
	Vector    []float32
}

// FileMetadata tracks which files have been indexed.
type FileMetadata struct {
	FilePath     string
	LastModified time.Time
}

// Index is the in-memory representation of a project's vector index.
type Index struct {
	Entries  []IndexEntry
	Files    map[string]FileMetadata // keyed by filepath
	Project  string
}

// IndexStats holds aggregate index statistics.
type IndexStats struct {
	Projects     int
	Files        int
	Vectors      int
	SizeBytes    int64
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
	return idx
}

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
