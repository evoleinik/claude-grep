package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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
