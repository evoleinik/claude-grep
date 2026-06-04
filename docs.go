package main

import (
	"encoding/gob"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DocChunk is one heading-delimited section of a markdown file.
type DocChunk struct {
	Heading string
	Body    string
	Ordinal int
}

// discoverDocsDir resolves the cwd repo and its curated-doc dirs.
// Uses the CURRENT worktree root (not the session mainRepoPath substitution):
// a feature branch may have edited learnings/, and those edits are what we want.
func discoverDocsDir(cwd string) (repoRoot string, dirs []string, ok bool) {
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", nil, false
	}
	repoRoot = strings.TrimSpace(string(out))
	if repoRoot == "" {
		return "", nil, false
	}

	var candidates []string
	envSet := os.Getenv("CLAUDE_GREP_DOCS") != ""
	if envSet {
		for _, rel := range strings.Split(os.Getenv("CLAUDE_GREP_DOCS"), ":") {
			if rel = strings.TrimSpace(rel); rel != "" {
				candidates = append(candidates, rel)
			}
		}
	} else {
		candidates = []string{"learnings", "docs"}
	}

	for _, rel := range candidates {
		abs := filepath.Join(repoRoot, rel)
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			dirs = append(dirs, abs)
			if !envSet {
				break // default mode: first existing dir only (learnings beats docs)
			}
		}
	}
	// In default mode, also fold in the repo's git-tracked README/CLAUDE/MEMORY
	// files — they hold curated knowledge agents expect to be searchable. Skipped
	// when CLAUDE_GREP_DOCS is set (that's an explicit, literal override).
	if !envSet {
		dirs = append(dirs, trackedDocFiles(repoRoot, dirs)...)
	}
	return repoRoot, dirs, len(dirs) > 0
}

// trackedDocFiles returns absolute paths of git-tracked README.md / CLAUDE.md /
// MEMORY.md files at any depth, excluding any that live under one of the already-
// included dirs. A copy inside learnings/ or docs/ is a table-of-contents we
// deliberately skip (see isIndexDoc); the root/nested originals are real content.
// git-tracked means node_modules and gitignored paths are excluded for free.
func trackedDocFiles(repoRoot string, excludeDirs []string) []string {
	out, err := exec.Command("git", "-C", repoRoot, "ls-files", "-z", "--", "*.md").Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, rel := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if rel == "" {
			continue
		}
		switch strings.ToLower(filepath.Base(rel)) {
		case "readme.md", "claude.md", "memory.md":
		default:
			continue
		}
		abs := filepath.Join(repoRoot, rel)
		if !pathUnderAny(abs, excludeDirs) {
			files = append(files, abs)
		}
	}
	return files
}

// pathUnderAny reports whether path is, or is nested within, one of dirs.
func pathUnderAny(path string, dirs []string) bool {
	for _, d := range dirs {
		if path == d || strings.HasPrefix(path, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// chunkMarkdown splits markdown into one chunk per heading section. The chunk's
// Heading is the full breadcrumb path ("H1 › H2 › H3") so a deep section keeps
// its parent context for both embedding and display. Content before the first
// heading becomes an "(intro)" chunk (dropped if blank).
func chunkMarkdown(data []byte) []DocChunk {
	lines := strings.Split(string(data), "\n")
	var chunks []DocChunk
	type hnode struct {
		level int
		text  string
	}
	var stack []hnode
	heading := "(intro)"
	var body strings.Builder

	flush := func() {
		text := strings.TrimSpace(body.String())
		body.Reset()
		if text == "" && heading == "(intro)" {
			return // skip empty preamble
		}
		if len(text) > maxEmbedChars {
			text = text[:maxEmbedChars]
		}
		chunks = append(chunks, DocChunk{Heading: heading, Body: text, Ordinal: len(chunks)})
	}

	for _, ln := range lines {
		if lvl, txt := headingLevel(ln); lvl > 0 {
			flush()
			for len(stack) > 0 && stack[len(stack)-1].level >= lvl {
				stack = stack[:len(stack)-1]
			}
			stack = append(stack, hnode{lvl, txt})
			parts := make([]string, len(stack))
			for i, h := range stack {
				parts[i] = h.text
			}
			heading = strings.Join(parts, " › ")
			continue
		}
		body.WriteString(ln)
		body.WriteString("\n")
	}
	flush()
	return chunks
}

// isIndexDoc reports whether a path is a summary/index file (README, MEMORY)
// rather than real content — those compete with content chunks and pollute both
// ranking and mined bench labels.
func isIndexDoc(path string) bool {
	switch strings.ToLower(filepath.Base(path)) {
	case "readme.md", "memory.md":
		return true
	}
	return false
}

// headingLevel returns the level (# count) and text of a markdown ATX heading,
// or (0, "") if the line is not a heading.
func headingLevel(line string) (int, string) {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "#") {
		return 0, ""
	}
	lvl := 0
	for lvl < len(s) && s[lvl] == '#' {
		lvl++
	}
	if lvl >= len(s) || s[lvl] != ' ' {
		return 0, "" // "#nottag" or bare "#"
	}
	return lvl, strings.TrimSpace(s[lvl:])
}

func docsIndexPath(repoRoot string) string {
	return filepath.Join(indexDir(), encodePath(repoRoot)+".docs.gob")
}

func loadDocsIndex(repoRoot string) *Index {
	idx := &Index{Files: make(map[string]FileMetadata), Project: encodePath(repoRoot) + ".docs"}
	f, err := os.Open(docsIndexPath(repoRoot))
	if err != nil {
		return idx
	}
	defer f.Close()
	if err := gob.NewDecoder(f).Decode(idx); err != nil {
		return &Index{Files: make(map[string]FileMetadata), Project: idx.Project}
	}
	return idx
}

func saveDocsIndex(repoRoot string, idx *Index) error {
	if err := os.MkdirAll(indexDir(), 0755); err != nil {
		return err
	}
	f, err := os.Create(docsIndexPath(repoRoot))
	if err != nil {
		return err
	}
	defer f.Close()
	return gob.NewEncoder(f).Encode(idx)
}

// buildDocEntries chunks→embeds one markdown file into doc index entries.
func buildDocEntries(file string, chunks []DocChunk, embedFn func(string) ([]float32, error)) ([]IndexEntry, error) {
	var ts string
	if info, err := os.Stat(file); err == nil {
		ts = info.ModTime().Format("2006-01-02T15:04:05")
	}
	var entries []IndexEntry
	for _, c := range chunks {
		vec, err := embedFn(c.Heading + "\n" + c.Body)
		if err != nil {
			return nil, err
		}
		// Store the full chunk body (already ≤ maxEmbedChars) — not a 200-char
		// preview — so dense hits get BM25-compressed around the query at display
		// time, matching the lexical lane's snippet quality.
		entries = append(entries, IndexEntry{
			Source: "docs", Heading: c.Heading, FilePath: file,
			MsgIndex: c.Ordinal, Role: "doc", Timestamp: ts,
			Preview: c.Body, Vector: vec,
		})
	}
	return entries, nil
}

// docEmbedVersion stamps the docs gob. Bump it whenever chunking or embed-input
// logic changes — refreshDocsIndex then discards the stale vectors and rebuilds,
// since file mtimes alone won't reflect a code change (the prefix-experiment trap).
const docEmbedVersion = "v4-noindexfiles"

// refreshDocsIndex re-embeds only doc files whose mtime is newer than the index,
// unless the embed-logic version changed (then it rebuilds everything).
func refreshDocsIndex(repoRoot string, dirs []string, embedFn func(string) ([]float32, error)) error {
	idx := loadDocsIndex(repoRoot)
	if idx.DocEmbedVersion != docEmbedVersion {
		idx = &Index{Files: make(map[string]FileMetadata), Project: idx.Project, DocEmbedVersion: docEmbedVersion}
	}
	changed := false
	for _, dir := range dirs {
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
				return nil
			}
			if path != dir && isIndexDoc(path) {
				return nil // skip README/MEMORY found inside a docs dir; keep explicit file entries
			}
			if meta, ok := idx.Files[path]; ok && !info.ModTime().After(meta.LastModified) {
				return nil // unchanged
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			entries, err := buildDocEntries(path, chunkMarkdown(data), embedFn)
			if err != nil {
				return nil // skip this file; an ollama hiccup shouldn't abort the walk
			}
			idx.Entries = removeEntriesForFile(idx.Entries, path)
			idx.Entries = append(idx.Entries, entries...)
			idx.Files[path] = FileMetadata{FilePath: path, LastModified: info.ModTime()}
			changed = true
			return nil
		})
	}
	if changed {
		return saveDocsIndex(repoRoot, idx)
	}
	return nil
}
