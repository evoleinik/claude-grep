package main

import (
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
	return repoRoot, dirs, len(dirs) > 0
}

// chunkMarkdown splits markdown into one chunk per heading section.
// Content before the first heading becomes an "(intro)" chunk (dropped if blank).
func chunkMarkdown(data []byte) []DocChunk {
	lines := strings.Split(string(data), "\n")
	var chunks []DocChunk
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
		if h := headingText(ln); h != "" {
			flush()
			heading = h
			continue
		}
		body.WriteString(ln)
		body.WriteString("\n")
	}
	flush()
	return chunks
}

// headingText returns the text of a markdown ATX heading (#/##/###...), or "".
func headingText(line string) string {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "#") {
		return ""
	}
	s = strings.TrimLeft(s, "#")
	if s == "" || s[0] != ' ' {
		return "" // e.g. "#nottag" or bare "#"
	}
	return strings.TrimSpace(s)
}
