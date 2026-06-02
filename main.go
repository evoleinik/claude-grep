package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var version = "1.5.0"

func main() {
	// Reorder args: allow flags after pattern (agents write "pattern -n 5" not "-n 5 pattern")
	reorderArgs()

	// Flags
	prompts := flag.Bool("p", false, "search only user prompts")
	responses := flag.Bool("r", false, "search only assistant responses")
	allProjects := flag.Bool("a", false, "search all projects")
	listOnly := flag.Bool("l", false, "list matching sessions only")
	maxResults := flag.Int("n", 100, "max results")
	maxDays := flag.Int("d", 7, "max age in days")
	maxHours := flag.Int("H", 0, "max age in hours (overrides -d)")
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
	benchPath := flag.String("bench", "", "run benchmark over a JSON corpus of queries")

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
  -n N          max results (default: 100)
  -d N          max age in days (default: 7)
  -H N          max age in hours (overrides -d)
  -C N          context messages before and after
  -B N          context messages before
  -A N          context messages after
  -s            semantic search (requires index)
  --json        JSON output
  --index       build/update vector index
  --status      show index stats (with --index)
  --all         reindex everything (with --index)
  --usage       show usage stats (agent telemetry)
  --bench FILE  run recovery benchmark over a JSON array of queries
  --version     show version

Examples:
  claude-grep "worktree"              find mentions of worktree
  claude-grep -p -n 5 "database"      your prompts about databases
  claude-grep -C 2 "error"            matches with 2 messages context
  claude-grep -a -d 30 "deploy"       all projects, last 30 days
  claude-grep -H 4 "bug"              last 4 hours only
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

	if *benchPath != "" {
		runBench(*benchPath)
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

	// Reject suspicious patterns that match everything (flag-parsing mistakes)
	if isSuspiciousPattern(pattern) {
		fmt.Fprintf(os.Stderr, "pattern %q matches everything — did you mean a different search term?\n", pattern)
		fmt.Fprintf(os.Stderr, "  use -- to separate flags from pattern: claude-grep -- %q\n", pattern)
		os.Exit(2)
	}

	origPattern := pattern
	startTime := time.Now()

	// Warn about short patterns that produce noisy results
	lit := longestLiteral(pattern)
	if !*semantic && len(lit) <= 3 && len(lit) > 0 {
		fmt.Fprintf(os.Stderr, "warning: short pattern %q will match many false positives — consider: claude-grep -s %q\n", pattern, pattern)
	}

	// Warn about extra positional args (agents try grep-style "pattern path")
	hasExtraArgs := flag.NArg() > 1
	if hasExtraArgs {
		fmt.Fprintf(os.Stderr, "warning: extra arguments ignored: %s\n", strings.Join(flag.Args()[1:], " "))
		fmt.Fprintf(os.Stderr, "  claude-grep searches ~/.claude/projects/ automatically\n")
		fmt.Fprintf(os.Stderr, "  use -a for all projects, -d N for age range\n")
	}
	// NOTE: with reorderArgs(), flags after the pattern are handled correctly.
	// Extra args only trigger for truly unknown positional args (e.g. file paths).

	// Resolve search path
	searchPath, err := resolveSearchPath(*allProjects)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(2)
	}

	// Auto-escalate to all projects if current project has very few sessions
	if !*allProjects {
		files, _ := findSessionFiles(searchPath, *maxDays)
		if len(files) <= 5 {
			allSearchPath, err := resolveSearchPath(true)
			if err == nil {
				fmt.Fprintf(os.Stderr, "only %d sessions in project scope — searching all projects\n", len(files))
				searchPath = allSearchPath
				*allProjects = true
			}
		}
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
	if *maxHours > 0 { flagList = append(flagList, "-H") }
	if *maxDays != 7 { flagList = append(flagList, "-d") }
	if *maxResults != 100 { flagList = append(flagList, "-n") }
	if *ctxBefore > 0 || *ctxAfter > 0 || *ctxBoth > 0 {
		flagList = append(flagList, "-C")
	}

	opts := SearchOpts{
		Role:        role,
		MaxResults:  *maxResults,
		MaxDays:     *maxDays,
		Before:      *ctxBefore,
		After:       *ctxAfter,
		ListOnly:    *listOnly,
		ExcludeSelf: true,
	}
	if *maxHours > 0 {
		opts.MaxAge = time.Duration(*maxHours) * time.Hour
	}

	// Set search query for BM25 compression in terminal output.
	// Extract literal words from regex pattern — regex metacharacters
	// become whitespace for tokenization, which is exactly what we want:
	// "(deploy|rollback)" → tokens "deploy", "rollback"
	// "deploy.*config" → tokens "deploy", "config"
	searchQuery = pattern

	if *semantic {
		matches, err := semanticSearch(pattern, searchPath, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %s\n", err)
			os.Exit(2)
		}
		capped := len(matches) >= opts.MaxResults
		files, _ := findSessionFiles(searchPath, opts.MaxDays)
		logUsage(UsageEvent{
			Pattern: pattern, Mode: "semantic", Flags: strings.Join(flagList, " "),
			Results: len(matches), Files: len(files), Days: *maxDays,
			Scope: scope, ExtraArgs: hasExtraArgs, Capped: capped,
			DurationMs: time.Since(startTime).Milliseconds(),
		})
		if len(matches) == 0 {
			printNoMatchHint(pattern, searchPath, opts, true, SearchStats{FilesTotal: len(files)})
			os.Exit(1)
		}
		if *jsonOut {
			formatJSON(matches, os.Stdout)
		} else {
			formatTerminal(matches, opts)
		}
		if capped {
			printCapHint(opts, 0)
		}
		return
	}

	// Normalize BRE syntax to ERE (agents write \| \( \) \+ \? instead of | ( ) + ?)
	hasBRE := pattern != normalizeBRE(pattern)
	if hasBRE {
		fmt.Fprintf(os.Stderr, "note: rewrote BRE escapes to ERE — claude-grep uses Go regex (use | ( ) + ? directly)\n")
	}

	matches, searchStats, layer, err := searchWithRecovery(pattern, searchPath, opts, !*semantic)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(2)
	}

	files := searchStats.FilesTotal
	capped := len(matches) >= opts.MaxResults

	switch layer {
	case "regex":
		logUsage(UsageEvent{
			Pattern: origPattern, Mode: "regex", Flags: strings.Join(flagList, " "),
			Results: len(matches), Files: files, Days: *maxDays,
			Scope: scope, BRE: hasBRE, ExtraArgs: hasExtraArgs, Capped: capped,
			DurationMs: time.Since(startTime).Milliseconds(),
			PrefilterSkip: searchStats.PrefilterSkipped, RegexSearched: searchStats.RegexSearched,
		})
	case "tokenized":
		fmt.Fprintf(os.Stderr, "phrase auto-matched as AND-of-terms (%d tokens) — %d results\n",
			len(extractWordTokens(pattern)), len(matches))
		logUsage(UsageEvent{
			Pattern: origPattern, Mode: "tokenized-fallback", Flags: strings.Join(flagList, " "),
			Results: len(matches), Files: files, Days: *maxDays,
			Scope: scope, DurationMs: time.Since(startTime).Milliseconds(),
		})
	case "semantic":
		fmt.Fprintf(os.Stderr, "no regex/token match — semantic results\n")
		logUsage(UsageEvent{
			Pattern: origPattern, Mode: "semantic-fallback", Flags: strings.Join(flagList, " "),
			Results: len(matches), Files: files, Days: *maxDays,
			Scope: scope, DurationMs: time.Since(startTime).Milliseconds(),
		})
	case "none":
		logUsage(UsageEvent{
			Pattern: origPattern, Mode: "regex", Flags: strings.Join(flagList, " "),
			Results: 0, Files: files, Days: *maxDays,
			Scope: scope, BRE: hasBRE, ExtraArgs: hasExtraArgs,
			DurationMs: time.Since(startTime).Milliseconds(),
			PrefilterSkip: searchStats.PrefilterSkipped, RegexSearched: searchStats.RegexSearched,
		})
		printNoMatchHint(pattern, searchPath, opts, false, searchStats)
		printNearMiss(pattern, searchPath, opts)
		os.Exit(1)
	}

	if *jsonOut {
		formatJSON(matches, os.Stdout)
	} else {
		formatTerminal(matches, opts)
	}
	if capped {
		printCapHint(opts, searchStats.TotalMatches)
	}
}

// reorderArgs moves flags after the pattern to before it.
// Go's flag.Parse() stops at the first non-flag arg, so
// "claude-grep pattern -n 5" fails. This reorders to "-n 5 pattern".
func reorderArgs() {
	args := os.Args[1:]
	if len(args) == 0 {
		return
	}

	// Flags that consume the next arg as a value
	valueTakers := map[string]bool{
		"-n": true, "-d": true, "-H": true, "-C": true, "-B": true, "-A": true,
	}

	var flags, positional []string
	i := 0
	for i < len(args) {
		arg := args[i]
		if arg == "--" {
			// Everything after -- is positional
			positional = append(positional, args[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			if valueTakers[arg] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		} else {
			positional = append(positional, arg)
		}
		i++
	}

	os.Args = append([]string{os.Args[0]}, append(flags, positional...)...)
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

	// Sessions are keyed by the encoded cwd at session start. A linked git
	// worktree (e.g. `.claude/worktrees/foo`) is a short-lived branch of the
	// main repo — project history lives with the main repo, not the worktree
	// path. Substitute so "project scope" means the main repo's sessions.
	candidate := cwd
	substituted := false
	if main, ok := mainRepoPath(cwd); ok {
		mainPath := filepath.Join(base, encodePath(main))
		if _, err := os.Stat(mainPath); err == nil {
			candidate = main
			substituted = true
		}
	}

	encoded := encodePath(candidate)
	path := filepath.Join(base, encoded)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", fmt.Errorf("no sessions for %s\ntry -a to search all projects", cwd)
	}
	if substituted {
		fmt.Fprintf(os.Stderr, "worktree detected — searching main project (%s)\n", candidate)
	}
	return path, nil
}

// mainRepoPath returns the main repository working directory if cwd is
// inside a linked git worktree. Returns ("", false) for the main worktree,
// non-git directories, or any git error.
func mainRepoPath(cwd string) (string, bool) {
	cmd := exec.Command("git", "-C", cwd, "rev-parse",
		"--path-format=absolute", "--git-dir", "--git-common-dir")
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		return "", false
	}
	gitDir, commonDir := lines[0], lines[1]
	if gitDir == "" || commonDir == "" || gitDir == commonDir {
		return "", false // main worktree, not a linked one
	}
	return filepath.Dir(commonDir), true
}

func printNoMatchHint(pattern, searchPath string, opts SearchOpts, isSemantic bool, stats SearchStats) {
	scope := "current project"
	if strings.HasSuffix(searchPath, filepath.Join(".claude", "projects")) {
		scope = "all projects"
	}
	fmt.Fprintf(os.Stderr, "no matches for %q (%d files, %d days, %s)\n",
		pattern, stats.FilesTotal, opts.MaxDays, scope)

	tokens := extractWordTokens(pattern)
	isPhrase := !isSemantic && len(tokens) >= 2

	if isPhrase {
		// Phrase already auto-tried as AND-of-terms + semantic. Most distinctive token wins.
		fmt.Fprintf(os.Stderr, "hint: narrow to the most distinctive token, e.g. claude-grep %q\n",
			longestWord(tokens))
		fmt.Fprintf(os.Stderr, "or:    widen tokens — claude-grep \"(%s)\"\n", strings.Join(tokens, "|"))
		return
	}

	// Single literal that simply isn't here: widening scope/time can help.
	if scope == "current project" || opts.MaxDays <= 7 {
		fmt.Fprintf(os.Stderr, "retry: claude-grep -a -d 30 %q\n", pattern)
	}
	if !isSemantic {
		fmt.Fprintf(os.Stderr, "or:    claude-grep -s %q\n", pattern)
	}
}

// longestWord returns the longest (most distinctive) word from a slice.
func longestWord(words []string) string {
	best := ""
	for _, w := range words {
		if len(w) > len(best) {
			best = w
		}
	}
	return best
}

// printNearMiss tries a relaxed search when regex found nothing.
// Extracts the longest literal from the pattern and does a simple
// case-insensitive substring search to show near-misses.
func printNearMiss(pattern, searchPath string, opts SearchOpts) {
	lit := longestLiteral(pattern)
	if len(lit) < 3 || lit == strings.TrimLeft(pattern, "(?i:") {
		// Pattern IS a simple literal, or too short — no point retrying
		return
	}

	// Quick search using the literal as the pattern
	relaxedOpts := opts
	relaxedOpts.MaxResults = 3
	re, err := regexp.Compile("(?i)" + regexp.QuoteMeta(lit))
	if err != nil {
		return
	}
	files, err := findSessionFiles(searchPath, opts.MaxDays)
	if err != nil || len(files) == 0 {
		return
	}

	var found int
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if re.Match(data) {
			found++
		}
		if found >= 3 {
			break
		}
	}

	if found > 0 {
		fmt.Fprintf(os.Stderr, "near: %d files contain %q — try: claude-grep %q\n", found, lit, lit)
	}
}

func isSuspiciousPattern(pattern string) bool {
	switch pattern {
	case "-", ".", "*", ".*":
		return true
	}
	return false
}

func printCapHint(opts SearchOpts, total int) {
	var hint string
	if total > opts.MaxResults {
		hint = fmt.Sprintf("showing %d of %d — narrow the pattern or raise -n (e.g. -n %d)",
			opts.MaxResults, total, total)
	} else {
		hint = fmt.Sprintf("results capped at %d — narrow the pattern or raise -n", opts.MaxResults)
	}
	if opts.MaxDays <= 7 && opts.MaxAge == 0 {
		hint += ", -d 30"
	}
	fmt.Fprintln(os.Stderr, hint)
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
