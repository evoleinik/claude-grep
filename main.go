package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var version = "1.0.0"

func main() {
	// Flags
	prompts := flag.Bool("p", false, "search only user prompts")
	responses := flag.Bool("r", false, "search only assistant responses")
	allProjects := flag.Bool("a", false, "search all projects")
	listOnly := flag.Bool("l", false, "list matching sessions only")
	maxResults := flag.Int("n", 20, "max results")
	maxDays := flag.Int("d", 7, "max age in days")
	ctxBoth := flag.Int("C", 0, "context lines before and after")
	ctxBefore := flag.Int("B", 0, "context lines before")
	ctxAfter := flag.Int("A", 0, "context lines after")
	semantic := flag.Bool("s", false, "semantic search mode")
	jsonOut := flag.Bool("json", false, "JSON output")
	index := flag.Bool("index", false, "index sessions for semantic search")
	indexStatus := flag.Bool("status", false, "show index status (use with --index)")
	indexAll := flag.Bool("all", false, "reindex everything (use with --index)")
	showVersion := flag.Bool("version", false, "show version")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `claude-grep — search Claude Code session history

Usage:
  claude-grep [flags] <pattern>     regex search (default)
  claude-grep -s [flags] <query>    semantic search
  claude-grep --index [--all]       build/update search index
  claude-grep --index --status      show index stats

Flags:
  -p            search only user prompts
  -r            search only assistant responses
  -a            search all projects (default: current dir)
  -l            list matching sessions only
  -n N          max results (default: 20)
  -d N          max age in days (default: 7)
  -C N          context messages before and after
  -B N          context messages before
  -A N          context messages after
  -s            semantic search (requires index)
  --json        JSON output
  --index       build/update vector index
  --status      show index stats (with --index)
  --all         reindex everything (with --index)
  --version     show version

Examples:
  claude-grep "worktree"              find mentions of worktree
  claude-grep -p -n 5 "database"      your prompts about databases
  claude-grep -C 2 "error"            matches with 2 messages context
  claude-grep -a -d 30 "deploy"       all projects, last 30 days
  claude-grep -s "that migration fix" semantic search by meaning
  claude-grep --json "test" | jq .    pipe JSON to jq

Exit codes:
  0  matches found
  1  no matches found
  2  error
`)
	}

	flag.Parse()

	if *showVersion {
		fmt.Println("claude-grep", version)
		os.Exit(0)
	}

	// Context: -C sets both if not individually set
	if *ctxBoth > 0 {
		if *ctxBefore == 0 {
			*ctxBefore = *ctxBoth
		}
		if *ctxAfter == 0 {
			*ctxAfter = *ctxBoth
		}
	}

	// Determine role filter
	role := "both"
	if *prompts {
		role = "user"
	} else if *responses {
		role = "assistant"
	}

	// Index mode
	if *index {
		if *indexStatus {
			printIndexStatus(*allProjects)
			return
		}
		runIndex(*allProjects || *indexAll)
		return
	}

	// Pattern required for search
	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(2)
	}
	pattern := flag.Arg(0)

	// Resolve search path
	searchPath, err := resolveSearchPath(*allProjects)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(2)
	}

	opts := SearchOpts{
		Role:       role,
		MaxResults: *maxResults,
		MaxDays:    *maxDays,
		Before:     *ctxBefore,
		After:      *ctxAfter,
		ListOnly:   *listOnly,
	}

	if *semantic {
		matches, err := semanticSearch(pattern, searchPath, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %s\n", err)
			os.Exit(2)
		}
		if len(matches) == 0 {
			os.Exit(1)
		}
		if *jsonOut {
			formatJSON(matches, os.Stdout)
		} else {
			formatTerminal(matches, opts)
		}
		return
	}

	// Normalize BRE syntax to ERE (agents write \| instead of |)
	pattern = strings.ReplaceAll(pattern, `\|`, "|")

	// Regex search
	matches, err := regexSearch(pattern, searchPath, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(2)
	}
	if len(matches) == 0 {
		os.Exit(1)
	}

	if *jsonOut {
		formatJSON(matches, os.Stdout)
	} else {
		re, _ := regexp.Compile("(?i)" + pattern)
		formatTerminal(matches, opts, re)
	}
}

func resolveSearchPath(allProjects bool) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	base := filepath.Join(home, ".claude", "projects")

	if allProjects {
		return base, nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	encoded := encodePath(cwd)
	path := filepath.Join(base, encoded)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", fmt.Errorf("no sessions for %s\ntry -a to search all projects", cwd)
	}
	return path, nil
}

func encodePath(path string) string {
	// Strip leading /, replace / with -
	path = strings.TrimPrefix(path, "/")
	return "-" + strings.ReplaceAll(path, "/", "-")
}
