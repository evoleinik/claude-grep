package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// BenchQuery is one corpus row. A bare JSON string is still accepted as an
// UNLABELED query (recovery-only reporting, the original behaviour). The object
// form adds the session the query is supposed to recover, which lets the bench
// score RANK instead of merely "something came back".
type BenchQuery struct {
	Query         string `json:"query"`
	ExpectSession string `json:"expect_session,omitempty"` // session id, or any prefix of one
	ExpectProject string `json:"expect_project,omitempty"` // substring of the project dir
	ExpectTopic   string `json:"expect_topic,omitempty"`   // regex that MUST appear in that session's indexed text
}

func (q BenchQuery) labeled() bool {
	return q.ExpectSession != "" || q.ExpectProject != ""
}

// isTarget reports whether m belongs to the session this query is labeled to.
// Both fields are optional; whichever are set must all match.
func (q BenchQuery) isTarget(m Message) bool {
	if q.ExpectSession != "" && !strings.HasPrefix(m.SessionID, q.ExpectSession) {
		return false
	}
	if q.ExpectProject != "" && !strings.Contains(m.Project, q.ExpectProject) {
		return false
	}
	return true
}

func (q BenchQuery) label() string {
	switch {
	case q.ExpectSession != "" && q.ExpectProject != "":
		return q.ExpectProject + "/" + q.ExpectSession
	case q.ExpectSession != "":
		return q.ExpectSession
	default:
		return q.ExpectProject
	}
}

type BenchRecord struct {
	Query   string `json:"query"`
	Found   bool   `json:"found"`
	Layer   string `json:"layer"`
	Results int    `json:"results"`
	Ms      int64  `json:"ms"`

	// Labeled rows only. A pointer so a MISS serializes as 0 rather than vanishing.
	Expect   string `json:"expect,omitempty"`
	HitRank  *int   `json:"hit_rank,omitempty"`  // 0 = miss, N = 1-based rank of the target session
	BaseRank *int   `json:"base_rank,omitempty"` // same, for the plain-regex baseline
}

// parseBenchCorpus accepts either form: ["q", ...] or [{"query":…, "expect_session":…}, ...].
func parseBenchCorpus(path string) []BenchQuery {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bench: cannot read %s: %v\n", path, err)
		os.Exit(2)
	}
	var rows []BenchQuery
	if err := json.Unmarshal(data, &rows); err == nil {
		ok := len(rows) > 0
		for _, q := range rows {
			if q.Query == "" {
				ok = false
				break
			}
		}
		if ok {
			return rows
		}
	}
	var plain []string
	if err := json.Unmarshal(data, &plain); err != nil {
		fmt.Fprintf(os.Stderr,
			"bench: %s must be a JSON array of strings, or of {query, expect_session|expect_project}: %v\n", path, err)
		os.Exit(2)
	}
	rows = make([]BenchQuery, len(plain))
	for i, s := range plain {
		rows[i] = BenchQuery{Query: s}
	}
	return rows
}

// sessionRank returns the 1-based rank of the labeled session among the DISTINCT
// sessions in result order (0 = absent), mirroring how results are grouped for
// display. Ranking by session is what the reader actually scans.
func sessionRank(q BenchQuery, matches []Match) int {
	seen := map[string]bool{}
	rank := 0
	for _, m := range matches {
		key := m.Message.Project + "/" + m.Message.SessionID
		if seen[key] {
			continue
		}
		seen[key] = true
		rank++
		if q.isTarget(m.Message) {
			return rank
		}
	}
	return 0
}

// benchExcludeProject drops the worktree this tool is developed in. Running the
// bench writes every query string into that session as a tool_use input, and
// tool output is indexed, so without this the corpus leaks into the index it is
// scoring. Measured 2026-09-08: contaminated it read hit@1 0 where the true
// figure was 9.
const benchExcludeProject = "claude-grep"

// validateLabels resolves every labeled row to a session file that exists on
// disk. A label pointing at a deleted or aged-out session would otherwise score
// as a plain miss, so the corpus would rot into a permanent red and nobody could
// tell "quality regressed" from "the target is gone". Returns the bad labels.
func validateLabels(queries []BenchQuery, searchDir string) []string {
	var present []Message
	filepath.Walk(searchDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		present = append(present, Message{
			SessionID: extractSessionID(path),
			Project:   extractProject(path),
		})
		return nil
	})

	var bad []string
	for _, q := range queries {
		if !q.labeled() {
			continue
		}
		found := false
		for _, m := range present {
			if q.isTarget(m) {
				found = true
				break
			}
		}
		if !found {
			bad = append(bad, q.label()+" (session gone)  <<"+q.Query)
			continue
		}
		// A session existing is not enough. A label built by grepping the RAW
		// file can match text the indexer never sees (tool_use/tool_result are
		// skipped) or a substring inside another word — "Hari" matched
		// "sharing", and that row silently scored working search as a miss.
		// So the topic must appear in the text that actually gets indexed.
		if q.ExpectTopic != "" && !topicInIndexedText(q, searchDir) {
			bad = append(bad, q.label()+" (topic /"+q.ExpectTopic+"/ absent from indexed text)  <<"+q.Query)
		}
	}
	return bad
}

// topicInIndexedText reports whether the label's topic regex appears in the
// session's INDEXED text, i.e. what search can actually reach.
func topicInIndexedText(q BenchQuery, searchDir string) bool {
	re, err := regexp.Compile("(?i)" + q.ExpectTopic)
	if err != nil {
		return false
	}
	found := false
	filepath.Walk(searchDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || found || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if !q.isTarget(Message{SessionID: extractSessionID(path), Project: extractProject(path)}) {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for _, m := range parseJSONL(path, data) {
			if re.MatchString(m.Text) {
				found = true
				break
			}
		}
		return nil
	})
	return found
}

// runBenchRecords runs every corpus query through the live recovery ladder at a
// fixed scope. searchDir is the projects root (overridable for tests). No telemetry.
func runBenchRecords(corpusPath, searchDir string) []BenchRecord {
	queries := parseBenchCorpus(corpusPath)

	// ExcludeSelf is MANDATORY here, not a nicety. Tool output is indexed, and
	// running the bench puts every query string verbatim into this session's
	// own transcript as a tool_use input. Without this the corpus leaks into
	// the index it is measuring: measured 2026-09-08, all 17 queries matched
	// the running session at the regex layer and scored 0, having read 0.61
	// MRR minutes earlier.
	opts := SearchOpts{Role: "both", MaxResults: 100, MaxDays: 365, ExcludeSelf: true,
		ExcludeProject: benchExcludeProject}
	recs := make([]BenchRecord, 0, len(queries))
	for _, q := range queries {
		start := time.Now()
		matches, _, layer, err := searchWithRecovery(q.Query, searchDir, opts, true)
		ms := time.Since(start).Milliseconds()
		if err != nil {
			layer = "error"
		}
		rec := BenchRecord{
			Query: q.Query, Found: len(matches) > 0, Layer: layer,
			Results: len(matches), Ms: ms,
		}
		if q.labeled() {
			rec.Expect = q.label()
			hit := sessionRank(q, matches)
			rec.HitRank = &hit
			// Baseline: the OR-of-content-words regex an agent would type by hand.
			// Same ranking path, so ladder and baseline are compared like for like.
			base := 0
			if pat := orWords(q.Query); pat != "" {
				if bm, _, berr := regexSearch("(?i)"+pat, searchDir, opts); berr == nil {
					base = sessionRank(q, bm)
				}
			}
			rec.BaseRank = &base
		}
		recs = append(recs, rec)
	}
	return recs
}

// sessionBenchVerdict gates on RANKING QUALITY, not on "did anything come back".
// The recovery ladder must recover the labeled session at least as well as a
// plain OR-of-words regex does. No magic threshold: the baseline sets the bar.
// A corpus with no labeled rows cannot be judged → pass, so legacy corpora and
// smoke runs never block.
func sessionBenchVerdict(recs []BenchRecord) (bool, string) {
	var ladderMRR, baseMRR float64
	judged := 0
	for _, r := range recs {
		if r.HitRank == nil {
			continue
		}
		judged++
		if *r.HitRank >= 1 {
			ladderMRR += 1.0 / float64(*r.HitRank)
		}
		if r.BaseRank != nil && *r.BaseRank >= 1 {
			baseMRR += 1.0 / float64(*r.BaseRank)
		}
	}
	if judged == 0 {
		return true, "no labeled rows — rank gate not evaluated"
	}
	ladderMRR /= float64(judged)
	baseMRR /= float64(judged)
	return ladderMRR >= baseMRR,
		fmt.Sprintf("ladder MRR=%.2f vs regex-baseline MRR=%.2f over %d labeled", ladderMRR, baseMRR, judged)
}

// runBench is the CLI entry: records to stdout (bare JSON array), summary to stderr.
func runBench(corpusPath string) {
	searchDir, err := resolveSearchPath(true) // benchmark always runs at all-projects scope
	if err != nil {
		fmt.Fprintf(os.Stderr, "bench: %v\n", err)
		os.Exit(2)
	}
	queries := parseBenchCorpus(corpusPath)
	if bad := validateLabels(queries, searchDir); len(bad) > 0 {
		fmt.Fprintf(os.Stderr, "bench: CANNOT MEASURE — %d label(s) invalid (session gone, or topic not in indexed text):\n", len(bad))
		for _, b := range bad {
			fmt.Fprintf(os.Stderr, "  %s\n", b)
		}
		fmt.Fprintln(os.Stderr, "  fix: relabel against a live session, or drop the row")
		os.Exit(2)
	}
	recs := runBenchRecords(corpusPath, searchDir)

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(recs)

	// Aggregate footer → stderr (data stays clean on stdout).
	found, byLayer := 0, map[string]int{}
	lat := make([]int, 0, len(recs))
	var at1, at3, at10, judged int
	for _, r := range recs {
		if r.Found {
			found++
		}
		byLayer[r.Layer]++
		lat = append(lat, int(r.Ms))
		if r.HitRank == nil {
			continue
		}
		judged++
		switch h := *r.HitRank; {
		case h == 1:
			at1, at3, at10 = at1+1, at3+1, at10+1
		case h >= 1 && h <= 3:
			at3, at10 = at3+1, at10+1
		case h >= 1 && h <= 10:
			at10++
		}
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

	if judged == 0 {
		fmt.Fprintln(os.Stderr,
			"bench: corpus is UNLABELED — 'found' only proves something came back, not that it was right.")
		fmt.Fprintln(os.Stderr,
			"  fix: add expect_session/expect_project per row to score rank. See bench/queries-labeled.json")
		return
	}
	fmt.Fprintf(os.Stderr, "bench: rank hit@1 %d hit@3 %d hit@10 %d of %d labeled\n", at1, at3, at10, judged)

	pass, msg := sessionBenchVerdict(recs)
	if !pass {
		fmt.Fprintf(os.Stderr, "FAIL: %s — the ladder is WORSE than a plain regex.\n", msg)
		fmt.Fprintln(os.Stderr, "  fix: claude-grep --index --all, or relabel the corpus if a target session aged out")
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "PASS: %s\n", msg)
}
