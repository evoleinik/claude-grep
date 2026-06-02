# claude-grep AX Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop multi-word queries from returning empty by auto-recovering them as AND-of-terms before semantic fallback, and prove the gain with a deterministic benchmark.

**Architecture:** When the live regex returns 0 hits, a shared `searchWithRecovery` escalates query→tokenized→semantic *within the agent's chosen scope* (never widening `-a`/`-d` — a prior scope-escalation feature was reverted, see spec §"Prior art"). The same function powers both the live path and the `--bench` harness, so the benchmark measures exactly what ships. Bundled: correct cap-hit hint, aligned failure hints, telemetry for the new layer.

**Tech Stack:** Go (stdlib only; Ollama for the optional semantic layer), `jq` for the benchmark corpus.

**Spec:** `docs/specs/2026-06-02-ax-recovery-design.md`

---

## File Structure

| File | Responsibility | Change |
|------|----------------|--------|
| `bench/extract-corpus.sh` | Freeze historically-failing queries from telemetry into a corpus | Create |
| `bench/queries.json` | The frozen benchmark corpus (array of query strings) | Generate |
| `bench/measure-baseline.sh` | Measure the *pristine* binary on the corpus (the "before") | Create |
| `bench/baseline.json` | Before-change found/latency per query | Generate |
| `search.go` | `SearchStats.TotalMatches`; extract `searchCore`; `gate` param on `searchFileTracked`; `collectFileMatches` | Modify |
| `tokenized.go` | `extractWordTokens`, `containsAllTokens`, `tokenizedSearch` | Create |
| `recovery.go` | `searchWithRecovery` (regex→tokenized→semantic ladder, no I/O side effects) | Create |
| `bench.go` | `runBench` (drives `searchWithRecovery`, emits JSON, **never logs telemetry**) | Create |
| `main.go` | Call `searchWithRecovery`; per-layer telemetry + rescue note; `--bench` flag; `printCapHint(opts,total)`; reordered `printNoMatchHint`; BRE note | Modify |
| `tokenized_test.go`, `recovery_test.go`, `bench_test.go`, `main_test.go` | Unit/integration tests | Create |
| `README.md` | Document recovery ladder + `--bench` | Modify |

Design choice: `searchCore` is shared by the regex and tokenized paths (DRY); `searchWithRecovery` and `runBench` share the ladder (the benchmark must measure the live path). Telemetry logging stays in `main()` only — `runBench` reuses the mechanism without the policy, so benchmark runs never pollute `usage.jsonl` (Rule of Separation).

---

## Task 1: Benchmark corpus + baseline (measurement-first, no code change)

Capture the "before" against the pristine binary **now**, before any code change exists.

**Files:**
- Create: `bench/extract-corpus.sh`, `bench/measure-baseline.sh`
- Generate: `bench/queries.json`, `bench/baseline.json`

- [ ] **Step 1: Write the corpus extractor**

Create `bench/extract-corpus.sh`:

```bash
#!/usr/bin/env bash
# Freeze historically-empty regex queries from claude-grep telemetry into a
# reproducible benchmark corpus. Re-running refreshes the corpus deliberately.
set -euo pipefail
LOG="${1:-$HOME/.claude/search-index/usage.jsonl}"
OUT="${2:-bench/queries.json}"

jq -s '
  [ .[]
    | select(.mode == "regex" and .results == 0)   # empty regex searches
    | .pattern ]
  | map(select(. != null and . != ""))
  | unique
' "$LOG" > "$OUT"

echo "wrote $OUT ($(jq length "$OUT") queries) from $LOG" >&2
```

- [ ] **Step 2: Generate the corpus**

Run: `cd ~/src/claude-grep && chmod +x bench/extract-corpus.sh && ./bench/extract-corpus.sh`
Expected: `wrote bench/queries.json (N queries) ...` on stderr, N roughly 80–120.
Sanity: `jq -r '.[0:5][]' bench/queries.json` shows real multi-word queries.

- [ ] **Step 3: Write the baseline measurer**

Create `bench/measure-baseline.sh` (works on ANY binary version — uses exit code only, so it runs against the pristine binary that has no `--bench`):

```bash
#!/usr/bin/env bash
# Measure a claude-grep binary on the frozen corpus at a fixed scope.
# Emits a JSON array of {query, found, ms} to stdout. Used for the pre-change baseline.
set -euo pipefail
BIN="${1:-claude-grep}"
CORPUS="${2:-bench/queries.json}"

jq -r '.[]' "$CORPUS" | while IFS= read -r q; do
  start=$(date +%s%N)
  if "$BIN" -a -d 30 -- "$q" >/dev/null 2>&1; then found=true; else found=false; fi
  ms=$(( ($(date +%s%N) - start) / 1000000 ))
  jq -cn --arg q "$q" --argjson f "$found" --argjson ms "$ms" \
    '{query:$q, found:$f, ms:$ms}'
done | jq -s '.'
```

- [ ] **Step 4: Capture the baseline**

Run: `chmod +x bench/measure-baseline.sh && ./bench/measure-baseline.sh claude-grep > bench/baseline.json`
Then: `jq '[.[]|select(.found)]|length as $f | {found:$f, total:length, pct:(($f*100)/length)}' bench/baseline.json`
Expected: a found-rate well under 100% (these are historical empties; some now resolve via the current regex+semantic path). Record this number — it's the "before."

- [ ] **Step 5: Commit**

```bash
git add bench/extract-corpus.sh bench/measure-baseline.sh bench/queries.json bench/baseline.json
git commit -m "test: freeze benchmark corpus + capture pre-change baseline"
```

---

## Task 2: `SearchStats.TotalMatches` + extract `searchCore`

Behavior-preserving refactor: pull the file-walk/collect engine out of `regexSearch` so the tokenized path can reuse it, and expose the true pre-cap match count.

**Files:**
- Modify: `search.go` (`SearchStats` struct ~17; `regexSearch` ~56-135; `searchFileTracked` ~167)
- Test: `search_test.go`

- [ ] **Step 1: Write the failing test**

Add to `search_test.go`:

```go
func TestSearchTotalMatchesBeforeCap(t *testing.T) {
	dir := t.TempDir()
	// 3 sessions, each with one message containing "alpha"
	for i, ts := range []string{"2026-06-01T10:00:00", "2026-06-01T11:00:00", "2026-06-01T12:00:00"} {
		writeSession(t, dir, fmt.Sprintf("s%d.jsonl", i), ts, "user", "alpha beta")
	}
	matches, stats, err := regexSearch("alpha", dir, SearchOpts{Role: "both", MaxResults: 2, MaxDays: 3650})
	if err != nil {
		t.Fatalf("regexSearch error: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches (capped), got %d", len(matches))
	}
	if stats.TotalMatches != 3 {
		t.Errorf("expected TotalMatches=3 (pre-cap), got %d", stats.TotalMatches)
	}
}
```

Add this test helper to `search_test.go` if not already present:

```go
func writeSession(t *testing.T, dir, name, ts, role, text string) {
	t.Helper()
	line, _ := json.Marshal(map[string]any{
		"type": role, "timestamp": ts + "Z",
		"message": map[string]any{"role": role, "content": text},
	})
	if err := os.WriteFile(filepath.Join(dir, name), append(line, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
}
```

Ensure imports include `encoding/json`, `fmt`, `os`, `path/filepath`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/src/claude-grep && go test -run TestSearchTotalMatchesBeforeCap ./...`
Expected: FAIL — `stats.TotalMatches` is 0 (field absent / unset) or a compile error for the new field.

- [ ] **Step 3: Add the field**

In `search.go`, extend `SearchStats`:

```go
type SearchStats struct {
	FilesTotal       int
	PrefilterSkipped int
	RegexSearched    int
	TotalMatches     int // matches found before MaxResults truncation
}
```

- [ ] **Step 4: Extract `searchCore` and set `TotalMatches`**

Replace the body of `regexSearch` (from the `re, err := regexp.Compile(...)` through the `return allMatches, stats, nil`) so the engine lives in a reusable `searchCore`. `regexSearch` keeps its signature:

```go
// regexSearch finds matches across session files using regex.
func regexSearch(pattern, searchPath string, opts SearchOpts) ([]Match, SearchStats, error) {
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return nil, SearchStats{}, err
	}
	return searchCore(re, extractPrefilterLiterals(pattern), nil, searchPath, opts)
}

// searchCore walks session files and collects matches. Exactly one of prefilter
// (OR-of-literals, for regex) or gate (AND-of-tokens, for tokenized recovery)
// selects which files to scan; the other must be nil.
func searchCore(re *regexp.Regexp, prefilter [][]byte, gate [][]byte, searchPath string, opts SearchOpts) ([]Match, SearchStats, error) {
	var files []string
	var err error
	if opts.MaxAge > 0 {
		files, err = findSessionFilesWithAge(searchPath, opts.MaxAge)
	} else {
		files, err = findSessionFiles(searchPath, opts.MaxDays)
	}
	if err != nil {
		return nil, SearchStats{}, err
	}
	if opts.ExcludeSelf && len(files) > 0 {
		files = excludeNewestFile(files)
	}

	type fileResult struct{ matches []Match }
	results := make(chan fileResult, len(files))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	var pfSkipped int32

	for _, f := range files {
		wg.Add(1)
		go func(fp string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			matches, skipped := searchFileTracked(fp, re, prefilter, gate, opts)
			if skipped {
				atomic.AddInt32(&pfSkipped, 1)
			}
			if len(matches) > 0 {
				results <- fileResult{matches: matches}
			}
		}(f)
	}
	go func() { wg.Wait(); close(results) }()

	var allMatches []Match
	for r := range results {
		allMatches = append(allMatches, r.matches...)
	}
	sort.Slice(allMatches, func(i, j int) bool {
		return allMatches[i].Message.Timestamp > allMatches[j].Message.Timestamp
	})

	total := len(allMatches)
	if len(allMatches) > opts.MaxResults {
		allMatches = allMatches[:opts.MaxResults]
	}

	stats := SearchStats{
		FilesTotal:       len(files),
		PrefilterSkipped: int(pfSkipped),
		RegexSearched:    len(files) - int(pfSkipped),
		TotalMatches:     total,
	}
	return allMatches, stats, nil
}
```

Delete the now-duplicated walk that used to live inline in `regexSearch` (the `type fileResult`, goroutine loop, sort, truncate, and old `stats` block).

- [ ] **Step 5: Add the `gate` parameter to `searchFileTracked`**

Update the signature and the file-selection check. Everything after the gate is unchanged:

```go
func searchFileTracked(filepath string, re *regexp.Regexp, prefilter [][]byte, gate [][]byte, opts SearchOpts) (matches []Match, prefilterSkipped bool) {
	data, err := os.ReadFile(filepath)
	if err != nil {
		return nil, false
	}

	// File selection: AND-gate (tokenized) takes precedence over OR-prefilter (regex).
	if gate != nil {
		if !containsAllTokens(data, gate) {
			return nil, true
		}
	} else if !prefilterMatch(data, prefilter) {
		return nil, true
	}

	messages := parseJSONL(filepath, data)
	// ... unchanged from here down ...
```

(`containsAllTokens` is defined in Task 3; this references it ahead of time — they land together before the suite is green.)

- [ ] **Step 6: Run tests to verify pass**

Run: `go test ./...`
Expected: `TestSearchTotalMatchesBeforeCap` PASS; all pre-existing tests still PASS (the refactor is behavior-preserving). If `containsAllTokens` is undefined, do Task 3 first, then return here.

- [ ] **Step 7: Commit**

```bash
git add search.go search_test.go
git commit -m "refactor: extract searchCore + expose SearchStats.TotalMatches"
```

---

## Task 3: Token helpers — `extractWordTokens` + `containsAllTokens`

Pure functions, fully unit-testable, no I/O.

**Files:**
- Create: `tokenized.go` (helpers only; `tokenizedSearch` lands in Task 4)
- Test: `tokenized_test.go`

- [ ] **Step 1: Write the failing tests**

Create `tokenized_test.go`:

```go
package main

import (
	"reflect"
	"testing"
)

func TestExtractWordTokens(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"ax check generalize platform", []string{"ax", "check", "generalize", "platform"}},
		{"sp-ucp-manifest llms-txt", []string{"sp-ucp-manifest", "llms-txt"}},
		{"branded_accuracy|rank_for_merchant|sources dict",
			[]string{"branded_accuracy", "rank_for_merchant", "sources", "dict"}},
		{"foo foo foo", []string{"foo"}},          // dedup
		{"a x .*", nil},                            // all tokens <2 chars / metachars → none
		{"singleword", []string{"singleword"}},     // 1 token (caller decides ≥2)
	}
	for _, c := range cases {
		got := extractWordTokens(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("extractWordTokens(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestContainsAllTokens(t *testing.T) {
	data := []byte("The UCP manifest and the JSONLD presence check")
	all := [][]byte{[]byte("ucp"), []byte("jsonld")}
	if !containsAllTokens(data, all) {
		t.Error("expected all tokens present (case-insensitive)")
	}
	missing := [][]byte{[]byte("ucp"), []byte("missingtoken")}
	if containsAllTokens(data, missing) {
		t.Error("expected false when a token is absent")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run 'TestExtractWordTokens|TestContainsAllTokens' ./...`
Expected: FAIL — `extractWordTokens` / `containsAllTokens` undefined.

- [ ] **Step 3: Implement the helpers**

Create `tokenized.go`:

```go
package main

import (
	"bytes"
	"regexp"
	"strings"
)

// wordTokenRe matches contiguous runs of letters, digits, underscore, hyphen.
// Keeps symbol-y identifiers whole: "sp-ucp-manifest" is one token.
var wordTokenRe = regexp.MustCompile(`[\p{L}\p{N}_-]+`)

// extractWordTokens pulls de-duplicated, lowercased word tokens (length >= 2)
// from a pattern, discarding regex metacharacters. Order is preserved.
func extractWordTokens(pattern string) []string {
	raw := wordTokenRe.FindAllString(pattern, -1)
	seen := map[string]bool{}
	var tokens []string
	for _, w := range raw {
		w = strings.ToLower(strings.Trim(w, "-"))
		if len(w) < 2 || seen[w] {
			continue
		}
		seen[w] = true
		tokens = append(tokens, w)
	}
	return tokens
}

// containsAllTokens reports whether data contains every token (case-insensitive).
// tokens must already be lowercased.
func containsAllTokens(data []byte, tokens [][]byte) bool {
	lower := bytes.ToLower(data)
	for _, t := range tokens {
		if !bytes.Contains(lower, t) {
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test -run 'TestExtractWordTokens|TestContainsAllTokens' ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tokenized.go tokenized_test.go
git commit -m "feat: add word-token extraction + AND-of-terms file gate"
```

---

## Task 4: `tokenizedSearch`

Reuses `searchCore` with an OR-regex matcher and an AND-of-tokens file gate.

**Files:**
- Modify: `tokenized.go`
- Test: `tokenized_test.go`

- [ ] **Step 1: Write the failing test**

Add to `tokenized_test.go` (uses `writeSession` from Task 2):

```go
func TestTokenizedSearchAndSemantics(t *testing.T) {
	dir := t.TempDir()
	// session A: contains BOTH tokens → should match
	writeSession(t, dir, "a.jsonl", "2026-06-01T10:00:00", "assistant",
		"we fixed the ucp-manifest and the jsonld presence check")
	// session B: contains only ONE token → AND gate excludes it
	writeSession(t, dir, "b.jsonl", "2026-06-01T11:00:00", "assistant",
		"only talked about jsonld here")

	matches, stats, err := tokenizedSearch([]string{"ucp", "jsonld"}, dir,
		SearchOpts{Role: "both", MaxResults: 100, MaxDays: 3650})
	if err != nil {
		t.Fatalf("tokenizedSearch error: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match (session A only), got %d", len(matches))
	}
	if matches[0].Message.SessionID == "" {
		t.Error("expected a populated SessionID on the match")
	}
	if stats.TotalMatches != 1 {
		t.Errorf("expected TotalMatches=1, got %d", stats.TotalMatches)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestTokenizedSearchAndSemantics ./...`
Expected: FAIL — `tokenizedSearch` undefined.

- [ ] **Step 3: Implement `tokenizedSearch`**

Append to `tokenized.go`:

```go
// tokenizedSearch rescues a multi-word query that matched nothing as a literal
// phrase. It selects sessions containing ALL tokens (AND gate) and surfaces the
// messages matching ANY token (OR regex). No external dependencies.
func tokenizedSearch(tokens []string, searchPath string, opts SearchOpts) ([]Match, SearchStats, error) {
	quoted := make([]string, len(tokens))
	gate := make([][]byte, len(tokens))
	for i, t := range tokens {
		quoted[i] = regexp.QuoteMeta(t)
		gate[i] = []byte(strings.ToLower(t))
	}
	re, err := regexp.Compile("(?i)(" + strings.Join(quoted, "|") + ")")
	if err != nil {
		return nil, SearchStats{}, err
	}
	return searchCore(re, nil, gate, searchPath, opts)
}
```

Add `"regexp"` and `"strings"` to imports if the helper-only file didn't already need them (it does — no change required).

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./...`
Expected: PASS (new test green; existing tests still green).

- [ ] **Step 5: Commit**

```bash
git add tokenized.go tokenized_test.go
git commit -m "feat: tokenizedSearch — AND-session, OR-message recovery"
```

---

## Task 5: `searchWithRecovery` + wire into `main`

The ladder, returning the layer. No telemetry/printing inside (policy stays in `main`).

**Files:**
- Create: `recovery.go`
- Modify: `main.go` (regex branch ~250-302)
- Test: `recovery_test.go`

- [ ] **Step 1: Write the failing test**

Create `recovery_test.go` (tokenized rescue needs no Ollama, so this is deterministic):

```go
package main

import "testing"

func TestSearchWithRecoveryEscalatesToTokenized(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "a.jsonl", "2026-06-01T10:00:00", "assistant",
		"discussion of ucp manifest plus jsonld presence and product identifiers")

	// Literal phrase matches nothing; recovery should escalate to tokenized.
	matches, _, layer, err := searchWithRecovery(
		"ucp manifest jsonld presence", dir,
		SearchOpts{Role: "both", MaxResults: 100, MaxDays: 3650}, false /* allowSemantic */)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if layer != "tokenized" {
		t.Fatalf("expected layer=tokenized, got %q", layer)
	}
	if len(matches) == 0 {
		t.Fatal("expected tokenized recovery to surface the session")
	}
}

func TestSearchWithRecoveryDirectRegexHit(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "a.jsonl", "2026-06-01T10:00:00", "user", "the worktree alias")
	matches, _, layer, err := searchWithRecovery(
		"worktree", dir, SearchOpts{Role: "both", MaxResults: 100, MaxDays: 3650}, false)
	if err != nil || layer != "regex" || len(matches) == 0 {
		t.Fatalf("expected direct regex hit, got layer=%q matches=%d err=%v", layer, len(matches), err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestSearchWithRecovery ./...`
Expected: FAIL — `searchWithRecovery` undefined.

- [ ] **Step 3: Implement `searchWithRecovery`**

Create `recovery.go`:

```go
package main

// searchWithRecovery runs the escalation ladder for a single query and reports
// which layer produced the results: "regex" | "tokenized" | "semantic" | "none".
// It performs NO telemetry logging or printing — callers own that policy, so the
// benchmark harness can reuse the exact live path without polluting usage.jsonl.
// Scope is never widened here; opts is honored as-is (see spec §"Prior art").
func searchWithRecovery(pattern, searchPath string, opts SearchOpts, allowSemantic bool) ([]Match, SearchStats, string, error) {
	norm := normalizeBRE(pattern)
	matches, stats, err := regexSearch(norm, searchPath, opts)
	if err != nil {
		return nil, stats, "", err // invalid regex — caller exits 2 (preserve current behavior)
	}
	if len(matches) > 0 {
		return matches, stats, "regex", nil
	}

	// Layer 2: tokenized AND-of-terms (no Ollama).
	tokens := extractWordTokens(pattern)
	if len(tokens) >= 2 {
		if tm, ts, terr := tokenizedSearch(tokens, searchPath, opts); terr == nil && len(tm) > 0 {
			return tm, ts, "tokenized", nil
		}
	}

	// Layer 3: semantic (Ollama). Safety net for AND-misses / conceptual queries.
	if allowSemantic && ollamaReachable() {
		if sm, serr := semanticSearch(pattern, searchPath, opts); serr == nil && len(sm) > 0 {
			return sm, SearchStats{FilesTotal: stats.FilesTotal, TotalMatches: len(sm)}, "semantic", nil
		}
	}

	return nil, stats, "none", nil
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test -run TestSearchWithRecovery ./...`
Expected: PASS.

- [ ] **Step 5: Rewire `main` to use it**

In `main.go`, replace the regex section (current lines ~246-302: the `normalizeBRE` pair, `regexSearch` call, telemetry, empty-result block, output, cap hint) with the version below. Note `hasBRE` is still computed for the learn-ERE note; `printCapHint` gains a `total` arg (Task 6 defines the new signature — they land together):

```go
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
```

- [ ] **Step 6: Verify build + manual smoke**

Run: `go build -o /tmp/cg . && /tmp/cg -a -d 30 "worktree" >/dev/null && echo "regex ok"`
Run: `/tmp/cg -a -d 90 "ucp manifest jsonld presence generalize" 2>&1 >/dev/null | head -1`
Expected: a `phrase auto-matched as AND-of-terms (...)` line on stderr (assuming such a session exists; if not, try a phrase you know recurs).

- [ ] **Step 7: Run full suite**

Run: `go test ./...`
Expected: PASS. (If `printCapHint` arity breaks compilation, complete Task 6 — they are paired.)

- [ ] **Step 8: Commit**

```bash
git add recovery.go recovery_test.go main.go
git commit -m "feat: regex->tokenized->semantic recovery ladder with layer telemetry"
```

---

## Task 6: Correct the cap-hit hint (count + sane suggestion)

**Files:**
- Modify: `main.go` (`printCapHint` ~485; both call sites)
- Test: `main_test.go`

- [ ] **Step 1: Write the failing test**

Create `main_test.go`:

```go
package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// capHintString captures printCapHint's stderr output for assertions.
func capHintString(t *testing.T, opts SearchOpts, total int) string {
	t.Helper()
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	printCapHint(opts, total)
	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	return buf.String()
}

func TestPrintCapHintShowsTotal(t *testing.T) {
	out := capHintString(t, SearchOpts{MaxResults: 100, MaxDays: 30}, 437)
	if !strings.Contains(out, "showing 100 of 437") {
		t.Errorf("expected 'showing 100 of 437', got %q", out)
	}
	if strings.Contains(out, "-n 100") {
		t.Errorf("must not suggest the current cap value, got %q", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestPrintCapHintShowsTotal ./...`
Expected: FAIL — compile error (old `printCapHint` takes one arg) or wrong text.

- [ ] **Step 3: Rewrite `printCapHint`**

Replace `printCapHint` in `main.go`:

```go
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
```

- [ ] **Step 4: Fix the semantic-branch call site**

In `main.go`, the semantic branch still calls `printCapHint(opts)` (total unknown for vector top-K). Update to:

```go
		if capped {
			printCapHint(opts, 0)
		}
```

(The Task 5 regex/recovery call site already passes `searchStats.TotalMatches`.)

- [ ] **Step 5: Run tests to verify pass**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add main.go main_test.go
git commit -m "fix: cap hint shows 'N of total' and stops suggesting the current -n"
```

---

## Task 7: Align the no-match hint + (BRE note already done)

The BRE learn-note landed in Task 5. Here, reorder `printNoMatchHint` so the actionable line leads and the misleading `-a -d 30` only shows for the single-literal case.

**Files:**
- Modify: `main.go` (`printNoMatchHint` ~405-434)
- Test: `main_test.go`

- [ ] **Step 1: Write the failing test**

Add to `main_test.go`:

```go
func noMatchHintString(t *testing.T, pattern string, opts SearchOpts, stats SearchStats) string {
	t.Helper()
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	printNoMatchHint(pattern, "/tmp/x/.claude/projects", opts, false, stats)
	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	return buf.String()
}

func TestNoMatchHintPhraseLeadsWithNarrow(t *testing.T) {
	out := noMatchHintString(t, "alpha beta gamma",
		SearchOpts{MaxDays: 30}, SearchStats{FilesTotal: 50, PrefilterSkipped: 50})
	if !strings.Contains(out, "narrow") {
		t.Errorf("phrase hint should suggest narrowing to a distinctive token, got %q", out)
	}
}

func TestNoMatchHintSingleLiteralOffersWiderScope(t *testing.T) {
	out := noMatchHintString(t, "Xyzzy123",
		SearchOpts{MaxDays: 7}, SearchStats{FilesTotal: 50})
	if !strings.Contains(out, "-a -d 30") {
		t.Errorf("single-literal miss should offer wider scope, got %q", out)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run TestNoMatchHint ./...`
Expected: FAIL — current ordering/text differs.

- [ ] **Step 3: Rewrite `printNoMatchHint`**

Replace the function body in `main.go`:

```go
func printNoMatchHint(pattern, searchPath string, opts SearchOpts, isSemantic bool, stats SearchStats) {
	scope := "current project"
	if strings.HasSuffix(searchPath, filepath.Join(".claude", "projects")) {
		scope = "all projects"
	}
	fmt.Fprintf(os.Stderr, "no matches for %q (%d files, %d days, %s)\n",
		pattern, stats.FilesTotal, opts.MaxDays, scope)

	words := strings.Fields(pattern)
	isPhrase := !isSemantic && len(words) >= 2

	if isPhrase {
		// Phrase already auto-tried as AND-of-terms + semantic. Most distinctive token wins.
		fmt.Fprintf(os.Stderr, "hint: narrow to the most distinctive token, e.g. claude-grep %q\n",
			longestWord(words))
		fmt.Fprintf(os.Stderr, "or:    widen tokens — claude-grep \"(%s)\"\n", strings.Join(words, "|"))
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
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add main.go main_test.go
git commit -m "fix: phrase no-match hint leads with narrow-token guidance"
```

---

## Task 8: `--bench` harness

In-binary benchmark mode driving the live `searchWithRecovery`; emits per-query JSON; never logs telemetry.

**Files:**
- Create: `bench.go`
- Modify: `main.go` (flag registration + early dispatch)
- Test: `bench_test.go`

- [ ] **Step 1: Write the failing test**

Create `bench_test.go`:

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRunBenchProducesRecords(t *testing.T) {
	dir := t.TempDir()
	// A session that the phrase query can only reach via tokenized recovery.
	writeSession(t, dir, "a.jsonl", "2026-06-01T10:00:00", "assistant",
		"notes on ucp manifest and jsonld presence")
	corpus := filepath.Join(dir, "queries.json")
	body, _ := json.Marshal([]string{"ucp manifest jsonld presence", "nosuchtokenzzz"})
	os.WriteFile(corpus, body, 0644)

	recs := runBenchRecords(corpus, dir) // testable core (no stdout/exit)
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	if !recs[0].Found || recs[0].Layer != "tokenized" {
		t.Errorf("query 0 expected found/tokenized, got %+v", recs[0])
	}
	if recs[1].Found {
		t.Errorf("query 1 expected not-found, got %+v", recs[1])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestRunBenchProducesRecords ./...`
Expected: FAIL — `runBenchRecords`/`BenchRecord` undefined.

- [ ] **Step 3: Implement `bench.go`**

```go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"
)

type BenchRecord struct {
	Query   string `json:"query"`
	Found   bool   `json:"found"`
	Layer   string `json:"layer"`
	Results int    `json:"results"`
	Ms      int64  `json:"ms"`
}

// runBenchRecords runs every corpus query through the live recovery ladder at a
// fixed scope. searchDir is the projects root (overridable for tests). No telemetry.
func runBenchRecords(corpusPath, searchDir string) []BenchRecord {
	data, err := os.ReadFile(corpusPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bench: cannot read %s: %v\n", corpusPath, err)
		os.Exit(2)
	}
	var queries []string
	if err := json.Unmarshal(data, &queries); err != nil {
		fmt.Fprintf(os.Stderr, "bench: %s must be a JSON array of strings: %v\n", corpusPath, err)
		os.Exit(2)
	}

	opts := SearchOpts{Role: "both", MaxResults: 100, MaxDays: 30, ExcludeSelf: false}
	recs := make([]BenchRecord, 0, len(queries))
	for _, q := range queries {
		start := time.Now()
		matches, _, layer, err := searchWithRecovery(q, searchDir, opts, true)
		ms := time.Since(start).Milliseconds()
		if err != nil {
			layer = "error"
		}
		recs = append(recs, BenchRecord{
			Query: q, Found: len(matches) > 0, Layer: layer,
			Results: len(matches), Ms: ms,
		})
	}
	return recs
}

// runBench is the CLI entry: records to stdout (bare JSON array), summary to stderr.
func runBench(corpusPath string) {
	searchDir, err := resolveSearchPath(true) // benchmark always runs at all-projects scope
	if err != nil {
		fmt.Fprintf(os.Stderr, "bench: %v\n", err)
		os.Exit(2)
	}
	recs := runBenchRecords(corpusPath, searchDir)

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(recs)

	// Aggregate footer → stderr (data stays clean on stdout).
	found, byLayer := 0, map[string]int{}
	lat := make([]int, 0, len(recs))
	for _, r := range recs {
		if r.Found {
			found++
		}
		byLayer[r.Layer]++
		lat = append(lat, int(r.Ms))
	}
	sort.Ints(lat)
	p := func(q float64) int {
		if len(lat) == 0 {
			return 0
		}
		return lat[int(q*float64(len(lat)-1))]
	}
	fmt.Fprintf(os.Stderr, "bench: %d/%d found (%d%%); layers=%v; p50=%dms p95=%dms\n",
		found, len(recs), found*100/max(1, len(recs)), byLayer, p(0.5), p(0.95))
}
```

If `max` is not already defined in the package, add:

```go
func max(a, b int) int { if a > b { return a }; return b }
```

(Go 1.21+ has a builtin `max`; if the module's `go.mod` targets ≥1.21, delete the helper. Check `head -3 go.mod`.)

- [ ] **Step 4: Register the `--bench` flag and dispatch early**

In `main.go`, add with the other flags (near line 37):

```go
	benchPath := flag.String("bench", "", "run benchmark over a JSON corpus of queries")
```

Add to the usage text block (after the `--usage` line):

```
  --bench FILE  run recovery benchmark over a JSON array of queries
```

And dispatch right after the `--usage` block (near line 94, before context handling):

```go
	if *benchPath != "" {
		runBench(*benchPath)
		return
	}
```

- [ ] **Step 5: Run tests to verify pass**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add bench.go bench_test.go main.go
git commit -m "feat: --bench harness over the live recovery ladder (no telemetry)"
```

---

## Task 9: Run the after-benchmark, diff, document, final verification

**Files:**
- Generate: `bench/after.json`
- Modify: `README.md`

- [ ] **Step 1: Full suite + vet + build**

Run: `cd ~/src/claude-grep && go vet ./... && go test ./... && go build -o /tmp/cg .`
Expected: no vet errors, all tests PASS, clean build.

- [ ] **Step 2: Capture the after-benchmark**

Run: `/tmp/cg --bench bench/queries.json > bench/after.json 2> bench/after-summary.txt; cat bench/after-summary.txt`
Expected: a summary line like `bench: M/N found (X%); layers=map[regex:.. tokenized:.. semantic:.. none:..]; p50=..ms p95=..ms`.

- [ ] **Step 3: Compute the headline (layer attribution + latency)**

Found-rate is NOT the headline: the pristine binary already "finds" ~163/164 because semantic
fallback is a catch-all when Ollama is up (see `bench/baseline.json`). Tokenized recovery's
value is **precision + cost** — exact token match, no Ollama, lower latency. Because the ladder
tries tokenized *before* semantic, a single after-run attributes each query to the cheapest
layer that answered it.

Run (bulletproof explicit counts):

```bash
jq '{
  corpus:    length,
  regex:     ([.[]|select(.layer=="regex")]    |length),
  tokenized: ([.[]|select(.layer=="tokenized")]|length),
  semantic:  ([.[]|select(.layer=="semantic")] |length),
  none:      ([.[]|select(.layer=="none")]     |length)
}' bench/after.json
```

Then latency by layer (tokenized should be far below semantic):

```bash
for L in regex tokenized semantic; do
  echo -n "$L median ms: "
  jq --arg L "$L" '[.[]|select(.layer==$L)|.ms]|sort|.[(length/2)|floor] // 0' bench/after.json
done
```

Expected: `tokenized` > 0 (the cheap layer precisely answers queries that would otherwise fall
to fuzzy semantic), and tokenized median latency << semantic median. Record both — together they
are the success metric. (`bench/baseline.json` stays committed as the evidence that found-rate
has no headroom; it is context, not the headline.)

- [ ] **Step 4: Document in README**

Add a short section to `README.md` under the usage docs:

````markdown
## Recovery ladder

When a regex returns nothing, claude-grep escalates automatically (within your
chosen scope — it never widens `-a`/`-d`):

1. **regex** as typed
2. **tokenized** — multi-word queries retry as AND-of-terms (sessions containing
   every word), surfacing the matching messages. No Ollama needed.
3. **semantic** — embedding search (requires the index + Ollama)

A stderr note tells you which layer answered.

## Benchmarking

```bash
./bench/extract-corpus.sh                 # refresh corpus from telemetry (optional)
claude-grep --bench bench/queries.json    # JSON records to stdout, summary to stderr
```

`bench/baseline.json` is the pre-recovery measurement; compare after-runs against it.
````

- [ ] **Step 5: Commit**

```bash
git add bench/after.json bench/after-summary.txt README.md
git commit -m "docs: record AX recovery benchmark results + ladder docs"
```

- [ ] **Step 6: Update the spec status**

In `docs/specs/2026-06-02-ax-recovery-design.md`, change the Status line to `Implemented (<date>)` and paste the Step 3 headline object under "Success criteria." Commit:

```bash
git add docs/specs/2026-06-02-ax-recovery-design.md
git commit -m "docs: mark AX recovery spec implemented with measured results"
```

---

## Self-Review

**Spec coverage:** §1 recovery ladder → Tasks 3–5. §2 cap count/hint → Tasks 2 (TotalMatches) + 6. §3 hint alignment + BRE note → Tasks 5 (BRE) + 7. §4 tests → every task is TDD. §5 benchmark → Tasks 1, 8, 9. Prior-art scope constraint → honored in `searchWithRecovery` (opts passed through untouched). All covered.

**Type consistency:** `SearchStats{FilesTotal,PrefilterSkipped,RegexSearched,TotalMatches}` used identically in Tasks 2/4/5. `searchCore(re, prefilter, gate, path, opts)` and `searchFileTracked(fp, re, prefilter, gate, opts)` match between Tasks 2/4. `searchWithRecovery(pattern, path, opts, allowSemantic) ([]Match, SearchStats, string, error)` consistent across Tasks 5/8. `printCapHint(opts, total)` consistent Tasks 5/6. `tokenizedSearch([]string, path, opts)` consistent Tasks 3/4/5. `BenchRecord` fields consistent Task 8.

**Placeholder scan:** No TBD/TODO; every code step shows complete code; every run step states expected output.

**Risk note:** Tasks 2 and 5 touch the proven search/main flow; both are guarded by pre-existing tests plus the new ones, and Task 5 includes a manual smoke step. The `go.mod` Go-version check in Task 8 Step 3 avoids a duplicate-`max` build break.
