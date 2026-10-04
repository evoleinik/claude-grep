package main

// End-to-end tests for the maintenance subcommands: usage stats, the two
// benchmarks, bench-case mining and the stale-docs audit.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func (sb *sandbox) writeUsage(events ...UsageEvent) {
	sb.t.Helper()
	var b strings.Builder
	for _, ev := range events {
		line, _ := json.Marshal(ev)
		b.Write(line)
		b.WriteByte('\n')
	}
	sb.writeFile(filepath.Join(sb.indexDir(), "usage.jsonl"), b.String())
}

func TestE2EUsageStats(t *testing.T) {
	sb := newSandbox(t)
	sb.run("--usage").want(t, 0, "no usage data yet")

	sb.writeFile(filepath.Join(sb.indexDir(), "usage.jsonl"), "not json\n")
	sb.run("--usage").want(t, 0, "no usage data yet")

	at := func(d time.Duration) string { return time.Now().Add(d).Format(time.RFC3339) }
	sb.writeUsage(UsageEvent{Timestamp: at(-90 * 24 * time.Hour), Pattern: "ancient", Mode: "regex", Results: 1})
	sb.run("--usage").want(t, 0, "no searches in last 30 days (1 total all-time)")

	sb.writeUsage(
		UsageEvent{Timestamp: at(-90 * 24 * time.Hour), Pattern: "ancient", Mode: "regex", Results: 1},
		// A retry chain: three searches seconds apart, widening from project to all.
		UsageEvent{Timestamp: at(-60 * time.Minute), Pattern: "deploy fail", Mode: "regex", Scope: "project",
			Files: 4, PrefilterSkip: 4, DurationMs: 30, Flags: "-p"},
		UsageEvent{Timestamp: at(-60*time.Minute + 10*time.Second), Pattern: "deploy fail", Mode: "regex", Scope: "project",
			Files: 4, PrefilterSkip: 4, DurationMs: 30, Flags: "-p -d"},
		UsageEvent{Timestamp: at(-60*time.Minute + 20*time.Second), Pattern: "deploy", Mode: "regex", Scope: "all",
			Results: 100, Capped: true, DurationMs: 60, Flags: "-a"},
		// Separate searches with the agent mistakes the report calls out.
		UsageEvent{Timestamp: at(-30 * time.Minute), Pattern: `a\|b`, Mode: "regex", Scope: "all", Results: 2, BRE: true},
		UsageEvent{Timestamp: at(-20 * time.Minute), Pattern: "x src/", Mode: "semantic", Scope: "all", Results: 3, ExtraArgs: true},
	)
	sb.run("--usage").want(t, 0,
		"Last 30 days: 5 searches (3 found, 2 empty)",
		"Hit rate: 60%",
		"Avg latency: 24ms",
		"regex: 4 (80%)", "semantic: 1 (20%)",
		"Agent issues:", "BRE patterns (auto-fixed): 1", "Extra positional args (ignored): 1", "Cap-hit searches: 1 (20%)",
		"Prefilter rejected ALL files (2 searches):",
		"Empty search patterns (improvement candidates):", `"deploy fail" (2x)`,
		"Flag frequency:", "-p 40%",
		"Retry chains: 1 (avg 3.0 searches/chain)", "Wasted time (empty searches in chains): 60ms",
		`Worst chain (3 searches): "deploy fail" → "deploy fail" → "deploy"`,
		"Scope escalations (project→all): 1",
		"Duplicate searches:", `2x project  "deploy fail"`,
	)
}

func TestE2EBench(t *testing.T) {
	sb := newSandbox(t)
	// The bench always searches every project, and it drops any project whose
	// name contains "claude-grep", so these fixtures use a name of their own.
	sb.session("-srv-shop", "sess-deploy", you("how do we deploy the api"), ai("run the deploy script"))
	sb.session("-srv-shop", "sess-db", you("the postgres migration failed"))
	corpus := func(rows any) string {
		data, _ := json.Marshal(rows)
		return sb.writeFile(filepath.Join(sb.cwd, "corpus.json"), string(data))
	}

	var recs []BenchRecord
	r := sb.run("--bench", corpus([]string{"deploy script", "zzzqqqxxx"})).want(t, 0,
		"bench: 1/2 found (50%)", "corpus is UNLABELED")
	if err := json.Unmarshal([]byte(r.stdout), &recs); err != nil || len(recs) != 2 {
		t.Fatalf("stdout must be the bare records array: %v\n%s", err, r.stdout)
	}
	if !recs[0].Found || recs[0].Layer != "regex" || recs[1].Found || recs[1].Layer != "none" {
		t.Errorf("unexpected records: %+v", recs)
	}

	labeled := []BenchQuery{
		{Query: "deploy script", ExpectSession: "sess-deploy", ExpectTopic: "deploy"},
		{Query: "postgres migration", ExpectSession: "sess-db", ExpectProject: "shop"},
	}
	r = sb.run("--bench", corpus(labeled)).want(t, 0,
		"bench: rank hit@1 2 hit@3 2 hit@10 2 of 2 labeled", "PASS: ladder MRR=1.00 vs regex-baseline MRR=1.00 over 2 labeled")
	if err := json.Unmarshal([]byte(r.stdout), &recs); err != nil || recs[0].HitRank == nil || *recs[0].HitRank != 1 {
		t.Errorf("labeled rows must report hit_rank: %v\n%s", err, r.stdout)
	}

	// A label that points at nothing cannot be scored, which is not the same as a miss.
	sb.run("--bench", corpus([]BenchQuery{
		{Query: "deploy", ExpectSession: "sess-vanished"},
		{Query: "deploy", ExpectSession: "sess-db", ExpectTopic: "kubernetes"},
	})).want(t, 2, "CANNOT MEASURE — 2 label(s) invalid", "sess-vanished (session gone)", "topic /kubernetes/ absent from indexed text")

	sb.run("--bench", filepath.Join(sb.cwd, "missing.json")).want(t, 2, "bench: cannot read")
	sb.run("--bench", sb.writeFile(filepath.Join(sb.cwd, "bad.json"), `{"query":"x"}`)).want(t, 2, "must be a JSON array")
}

func TestE2EBenchFailsWhenLadderIsWorseThanRegex(t *testing.T) {
	sb := newSandbox(t)
	// The phrase matches this session literally, so the ladder stops at the regex
	// layer and returns only it. The labeled target holds the words apart, which a
	// plain OR-of-words regex finds. The ladder is then worse than the baseline.
	sb.session("-srv-shop", "sess-decoy", you("the phrase alpha beta appears verbatim here"))
	sb.session("-srv-shop", "sess-target", you("alpha is one thing"), ai("beta is another"))
	data, _ := json.Marshal([]BenchQuery{{Query: "alpha beta", ExpectSession: "sess-target"}})
	path := sb.writeFile(filepath.Join(sb.cwd, "corpus.json"), string(data))

	sb.run("--bench", path).want(t, 1, "FAIL: ladder MRR=0.00 vs regex-baseline MRR=", "WORSE than a plain regex")
}

func TestE2EDocsBench(t *testing.T) {
	sb := newSandbox(t)
	rows := []DocsBenchQuery{{Query: "rollback script", ExpectFile: "deploy.md"}}
	data, _ := json.Marshal(rows)
	corpus := sb.writeFile(filepath.Join(sb.home, "docs-corpus.json"), string(data))

	sb.run("--bench-docs", corpus).want(t, 2, "bench: no learnings/ or docs/ dir")

	sb.docsRepo()
	var recs []DocsBenchRecord
	r := sb.run("--bench-docs", corpus).want(t, 0, "docs-bench: grep hit@any 1/1", "cg-regex hit@1 1", "PASS:")
	if err := json.Unmarshal([]byte(r.stdout), &recs); err != nil || len(recs) != 1 {
		t.Fatalf("stdout must be the bare records array: %v\n%s", err, r.stdout)
	}
	if recs[0].Grep.HitRank != 1 || recs[0].CgRegex.HitRank != 1 || recs[0].CgSemantic.HitRank != 1 {
		t.Errorf("every engine should find deploy.md first: %+v", recs[0])
	}

	sb.run("--bench-docs", filepath.Join(sb.home, "missing.json")).want(t, 2, "bench: cannot read")
	sb.run("--bench-docs", sb.writeFile(filepath.Join(sb.home, "bad.json"), `"x"`)).want(t, 2, "must be a JSON array")
}

func TestE2EMineDocsQueries(t *testing.T) {
	sb := newSandbox(t)
	sb.run("--mine-docs-queries").want(t, 1, "no learnings/ or docs/ dir")

	sb.docsRepo()
	sb.writeUsage(
		UsageEvent{Pattern: "rollback script"},  // answerable by the doc
		UsageEvent{Pattern: "rollback"},         // one token: too thin to be a bench case
		UsageEvent{Pattern: "kubernetes audit"}, // the docs cannot answer it
		UsageEvent{Pattern: "rollback script"},  // a repeat is proposed once
	)
	r := sb.run("--mine-docs-queries").want(t, 0, "1 candidates proposed from usage.jsonl")
	var got []struct {
		Query      string `json:"query"`
		ExpectFile string `json:"expect_file"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || len(got) != 1 {
		t.Fatalf("stdout must be the candidate array: %v\n%s", err, r.stdout)
	}
	if got[0].Query != "rollback script" || got[0].ExpectFile != "deploy.md" {
		t.Errorf("unexpected candidate: %+v", got[0])
	}
}

// commitAt commits everything in dir with a fixed date, so the audit's
// "changed N days after the doc" arithmetic is exact.
func commitAt(t *testing.T, dir, date, msg string) {
	t.Helper()
	runGit(t, dir, "add", "-A")
	cmd := exec.Command("git", "-C", dir, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "-m", msg)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
}

func TestE2EStaleDocs(t *testing.T) {
	sb := newSandbox(t)
	sb.run("--stale-docs").want(t, 2, "no curated docs in")

	mustInitGitRepo(t, sb.cwd)
	sb.writeFile(filepath.Join(sb.cwd, "server.go"), "package main\n")
	sb.writeFile(filepath.Join(sb.cwd, "go.mod"), "module x\n")
	sb.writeFile(filepath.Join(sb.cwd, "learnings", "api.md"),
		"# API\n\n## Handler\n\nThe handler lives in server.go and is declared in go.mod. Ignore ghost.go.\n")
	sb.writeFile(filepath.Join(sb.cwd, "learnings", "README.md"), "# Index\n\nSee server.go.\n")
	// Kept out of git on purpose: a doc git has never seen has no date to compare.
	sb.writeFile(filepath.Join(sb.cwd, "learnings", "draft.md"), "# Draft\n\nserver.go\n")
	sb.writeFile(filepath.Join(sb.cwd, ".git", "info", "exclude"), "learnings/draft.md\n")
	commitAt(t, sb.cwd, "2026-01-01T12:00:00Z", "docs and code")

	sb.run("--stale-docs").want(t, 0, "stale-docs: clean")
	if out := sb.run("--stale-docs", "--json").want(t, 0).stdout; strings.TrimSpace(out) != "null" {
		t.Errorf("a clean audit prints an empty JSON result, got %q", out)
	}

	// The code moves 40 days after the doc was last edited. go.mod moves too,
	// and is ignored: it churns for reasons no single doc is about.
	sb.writeFile(filepath.Join(sb.cwd, "server.go"), "package main\n\nfunc handler() {}\n")
	sb.writeFile(filepath.Join(sb.cwd, "go.mod"), "module y\n")
	commitAt(t, sb.cwd, "2026-02-10T12:00:00Z", "change the handler")

	sb.run("--stale-docs").want(t, 1,
		"stale-docs: 1 doc(s) reference code that changed after the doc was last edited",
		"learnings/api.md  (edited 2026-01-01 · 1/1 refs moved after)",
		"server.go moved 40d after — under § API › Handler",
		"fix:").lacks(t, "README.md", "go.mod", "ghost.go", "draft.md")

	var docs []staleDoc
	r := sb.run("--stale-docs", "--json").want(t, 1)
	if err := json.Unmarshal([]byte(r.stdout), &docs); err != nil || len(docs) != 1 {
		t.Fatalf("--json must print the stale docs: %v\n%s", err, r.stdout)
	}
	if want := (staleRef{Path: "server.go", DaysAfter: 40, Heading: "API › Handler"}); docs[0].Refs[0] != want {
		t.Errorf("stale ref = %+v, want %+v", docs[0].Refs[0], want)
	}
}

func TestFormatSize(t *testing.T) {
	for bytes, want := range map[int64]string{
		512:           "512 B",
		3 << 10:       "3.0 KB",
		5<<20 + 1<<19: "5.5 MB",
		2 << 30:       "2.0 GB",
	} {
		if got := formatSize(bytes); got != want {
			t.Errorf("formatSize(%d) = %q, want %q", bytes, got, want)
		}
	}
}

func TestOllamaBase(t *testing.T) {
	t.Setenv("CLAUDE_GREP_OLLAMA_URL", "")
	if got := ollamaBase(); got != "http://localhost:11434" {
		t.Errorf("default ollama address = %q", got)
	}
	t.Setenv("CLAUDE_GREP_OLLAMA_URL", "http://gpu-box:11434/")
	if got := ollamaBase(); got != "http://gpu-box:11434" {
		t.Errorf("override should drop the trailing slash, got %q", got)
	}
}

func TestReorderArgs(t *testing.T) {
	cases := []struct{ in, want []string }{
		{[]string{"pattern", "-n", "5"}, []string{"-n", "5", "pattern"}},
		{[]string{"pattern", "--json", "-d", "30"}, []string{"--json", "-d", "30", "pattern"}},
		{[]string{"--shards", "4", "--shard", "1", "--reembed-orphans"}, []string{"--shards", "4", "--shard", "1", "--reembed-orphans"}},
		{[]string{"--bench", "corpus.json", "--json"}, []string{"--bench", "corpus.json", "--json"}},
		{[]string{"-p", "--", "-n", "5"}, []string{"-p", "-n", "5"}},
		{nil, nil},
	}
	saved := os.Args
	defer func() { os.Args = saved }()
	for _, c := range cases {
		os.Args = append([]string{"claude-grep"}, c.in...)
		reorderArgs()
		if got := fmt.Sprint(os.Args[1:]); got != fmt.Sprint(c.want) {
			t.Errorf("reorderArgs(%v) = %v, want %v", c.in, os.Args[1:], c.want)
		}
	}
}
