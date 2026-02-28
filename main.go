package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
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
	showUsage := flag.Bool("usage", false, "show usage stats")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `claude-grep — search Claude Code session history

Usage:
  claude-grep [flags] <pattern>     regex search (default)
  claude-grep -s [flags] <query>    semantic search
  claude-grep --index [--all]       build/update search index
  claude-grep --index --status      show index stats
  claude-grep --usage               show usage stats

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
  --usage       show usage stats (agent telemetry)
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

	if *showUsage {
		printUsageStats()
		return
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
	origPattern := pattern
	startTime := time.Now()

	// Warn about extra positional args (agents try grep-style "pattern path")
	hasExtraArgs := flag.NArg() > 1
	if hasExtraArgs {
		fmt.Fprintf(os.Stderr, "warning: extra arguments ignored: %s\n", strings.Join(flag.Args()[1:], " "))
		fmt.Fprintf(os.Stderr, "  claude-grep searches ~/.claude/projects/ automatically\n")
		fmt.Fprintf(os.Stderr, "  use -a for all projects, -d N for age range\n")
	}

	// Resolve search path
	searchPath, err := resolveSearchPath(*allProjects)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(2)
	}

	scope := "project"
	if *allProjects {
		scope = "all"
	}

	// Build flags string for telemetry
	var flagList []string
	if *prompts { flagList = append(flagList, "-p") }
	if *responses { flagList = append(flagList, "-r") }
	if *allProjects { flagList = append(flagList, "-a") }
	if *listOnly { flagList = append(flagList, "-l") }
	if *semantic { flagList = append(flagList, "-s") }
	if *jsonOut { flagList = append(flagList, "--json") }
	if *maxDays != 7 { flagList = append(flagList, fmt.Sprintf("-d %d", *maxDays)) }
	if *maxResults != 20 { flagList = append(flagList, fmt.Sprintf("-n %d", *maxResults)) }
	if *ctxBefore > 0 || *ctxAfter > 0 || *ctxBoth > 0 {
		flagList = append(flagList, fmt.Sprintf("-C %d", *ctxBoth))
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
		files, _ := findSessionFiles(searchPath, opts.MaxDays)
		logUsage(UsageEvent{
			Pattern: pattern, Mode: "semantic", Flags: strings.Join(flagList, " "),
			Results: len(matches), Files: len(files), Days: *maxDays,
			Scope: scope, ExtraArgs: hasExtraArgs,
			DurationMs: time.Since(startTime).Milliseconds(),
		})
		if len(matches) == 0 {
			printNoMatchHint(pattern, searchPath, opts, true)
			os.Exit(1)
		}
		if *jsonOut {
			formatJSON(matches, os.Stdout)
		} else {
			formatTerminal(matches, opts)
		}
		return
	}

	// Normalize BRE syntax to ERE (agents write \| \( \) \+ \? instead of | ( ) + ?)
	hasBRE := pattern != normalizeBRE(pattern)
	pattern = normalizeBRE(pattern)

	// Regex search
	matches, err := regexSearch(pattern, searchPath, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(2)
	}

	files, _ := findSessionFiles(searchPath, opts.MaxDays)
	logUsage(UsageEvent{
		Pattern: origPattern, Mode: "regex", Flags: strings.Join(flagList, " "),
		Results: len(matches), Files: len(files), Days: *maxDays,
		Scope: scope, BRE: hasBRE, ExtraArgs: hasExtraArgs,
		DurationMs: time.Since(startTime).Milliseconds(),
	})

	if len(matches) == 0 {
		printNoMatchHint(pattern, searchPath, opts, false)
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

func printNoMatchHint(pattern, searchPath string, opts SearchOpts, isSemantic bool) {
	// Count files in scope for context
	files, _ := findSessionFiles(searchPath, opts.MaxDays)
	nFiles := len(files)

	scope := "current project"
	if strings.HasSuffix(searchPath, filepath.Join(".claude", "projects")) {
		scope = "all projects"
	}

	fmt.Fprintf(os.Stderr, "no matches for %q (%d files, %d days, %s)\n", pattern, nFiles, opts.MaxDays, scope)

	// Suggest broadening
	var hints []string
	if opts.MaxDays <= 7 {
		hints = append(hints, "-d 30 (broader time range)")
	}
	if scope == "current project" {
		hints = append(hints, "-a (all projects)")
	}
	if !isSemantic {
		hints = append(hints, "-s (semantic search by meaning)")
	}
	if len(hints) > 0 {
		fmt.Fprintf(os.Stderr, "try: %s\n", strings.Join(hints, ", "))
	}
}

func encodePath(path string) string {
	// Strip leading /, replace / with -
	path = strings.TrimPrefix(path, "/")
	return "-" + strings.ReplaceAll(path, "/", "-")
}

// normalizeBRE converts common BRE escape sequences to ERE equivalents.
// Agents often write grep BRE syntax (\|, \(, \), \+, \?) which silently
// fails in Go's ERE-style regexp.
func normalizeBRE(pattern string) string {
	// Only normalize sequences that are BRE-specific escapes.
	// \| → |  (alternation)
	// \( → (  (group open)
	// \) → )  (group close)
	// \+ → +  (one or more)
	// \? → ?  (zero or one)
	// Note: \. \* \[ \] \^ \$ are the SAME in both BRE and ERE, so don't touch them.
	for _, c := range []string{"|", "(", ")", "+", "?"} {
		pattern = strings.ReplaceAll(pattern, `\`+c, c)
	}
	return pattern
}
