package main

// End-to-end tests. They build the real binary and drive it the way an agent
// does: argv in, exit code + stdout + stderr out. main() exits the process on
// most paths, so this is the only way to test the CLI contract (flag routing,
// exit codes, hints on stderr) without restructuring main around a test seam.
//
// Each test gets a sandbox: its own $HOME (so ~/.claude is empty), its own cwd,
// and an ollama address that either refuses connections or points at a fake
// embedder. Nothing touches the developer's real history or a real ollama.
//
// Coverage: under `go test -cover` the binary is built with -cover too. go test
// hands the child a GOCOVERDIR, the child writes its counters there on exit, and
// they are folded into the same profile as the unit tests. That is how main()
// and the subcommands show up in the coverage number at all.

import (
	"encoding/json"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	e2eOnce sync.Once
	e2eBin  string
	e2eErr  string
)

func TestMain(m *testing.M) {
	code := m.Run()
	if e2eBin != "" {
		os.RemoveAll(filepath.Dir(e2eBin))
	}
	os.Exit(code)
}

// e2eBinary builds the CLI once per test run.
func e2eBinary(t *testing.T) string {
	t.Helper()
	e2eOnce.Do(func() {
		dir, err := os.MkdirTemp("", "cg-e2e-bin-")
		if err != nil {
			e2eErr = err.Error()
			return
		}
		e2eBin = filepath.Join(dir, "cg")
		args := []string{"build", "-o", e2eBin}
		if mode := testing.CoverMode(); mode != "" {
			args = append(args, "-cover", "-covermode="+mode)
		}
		if out, err := exec.Command("go", append(args, ".")...).CombinedOutput(); err != nil {
			e2eErr = err.Error() + "\n" + string(out)
		}
	})
	if e2eErr != "" {
		t.Fatalf("building the CLI: %s", e2eErr)
	}
	return e2eBin
}

// deadOllama refuses connections at once, so "ollama down" paths are instant.
const deadOllama = "http://127.0.0.1:1"

type sandbox struct {
	t      *testing.T
	home   string // $HOME of the child process
	cwd    string // its working directory
	ollama string // its CLAUDE_GREP_OLLAMA_URL
	seq    int    // spreads message timestamps so ordering is deterministic
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	root := resolveSymlinks(t.TempDir())
	sb := &sandbox{t: t, home: filepath.Join(root, "home"), cwd: filepath.Join(root, "work", "proj"), ollama: deadOllama}
	for _, d := range []string{sb.home, sb.cwd} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	return sb
}

type result struct {
	stdout, stderr string
	code           int
}

// run executes the CLI in the sandbox and returns what an agent would see.
func (sb *sandbox) run(args ...string) result {
	sb.t.Helper()
	cmd := exec.Command(e2eBinary(sb.t), args...)
	cmd.Dir = sb.cwd
	cmd.Env = append(os.Environ(),
		"HOME="+sb.home,
		"CLAUDE_GREP_OLLAMA_URL="+sb.ollama,
		"CLAUDE_GREP_DOCS=",
	)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		sb.t.Fatalf("running %v: %v", args, err)
	}
	return result{out.String(), errb.String(), code}
}

// want asserts the exit code and that every needle appears in stdout+stderr.
func (r result) want(t *testing.T, code int, needles ...string) result {
	t.Helper()
	if r.code != code {
		t.Errorf("exit code = %d, want %d\nstdout: %s\nstderr: %s", r.code, code, r.stdout, r.stderr)
	}
	all := r.stdout + "\n" + r.stderr
	for _, n := range needles {
		if !strings.Contains(all, n) {
			t.Errorf("output missing %q\nstdout: %s\nstderr: %s", n, r.stdout, r.stderr)
		}
	}
	return r
}

// lacks asserts none of the needles appear in stdout+stderr.
func (r result) lacks(t *testing.T, needles ...string) result {
	t.Helper()
	all := r.stdout + "\n" + r.stderr
	for _, n := range needles {
		if strings.Contains(all, n) {
			t.Errorf("output must not contain %q\nstdout: %s\nstderr: %s", n, r.stdout, r.stderr)
		}
	}
	return r
}

func (sb *sandbox) projectsDir() string { return filepath.Join(sb.home, ".claude", "projects") }
func (sb *sandbox) indexDir() string    { return filepath.Join(sb.home, ".claude", "search-index") }

// project is the directory Claude Code would file this sandbox's cwd under.
func (sb *sandbox) project() string { return encodePath(sb.cwd) }

type turn struct{ role, text string }

func you(text string) turn { return turn{"user", text} }
func ai(text string) turn  { return turn{"assistant", text} }

// session writes one transcript. Its mtime is an hour old: the search skips the
// newest file when that file is under a minute old (it takes it for the session
// doing the searching), and a fixture written a moment ago would qualify.
func (sb *sandbox) session(project, id string, turns ...turn) string {
	sb.t.Helper()
	dir := filepath.Join(sb.projectsDir(), project)
	if err := os.MkdirAll(dir, 0755); err != nil {
		sb.t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		sb.t.Fatal(err)
	}
	sb.appendTurns(path, time.Now().Add(-time.Hour), turns...)
	return path
}

func (sb *sandbox) appendTurns(path string, mtime time.Time, turns ...turn) {
	sb.t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		sb.t.Fatal(err)
	}
	base := time.Now().Add(-2 * time.Hour).UTC()
	for _, tn := range turns {
		sb.seq++
		line, _ := json.Marshal(map[string]any{
			"type":      tn.role,
			"timestamp": base.Add(time.Duration(sb.seq)*time.Second).Format("2006-01-02T15:04:05") + ".000Z",
			"message":   map[string]any{"role": tn.role, "content": tn.text},
		})
		f.Write(append(line, '\n'))
	}
	f.Close()
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		sb.t.Fatal(err)
	}
}

// seed fills the cwd's project with seven sessions. Seven matters: with five or
// fewer the CLI widens the search to every project, which most tests do not want.
func (sb *sandbox) seed() {
	sb.t.Helper()
	p := sb.project()
	sb.session(p, "sess-deploy",
		you("how do we deploy the api"),
		ai("run the deploy script, then verify the rollback plan"))
	sb.session(p, "sess-db",
		you("the postgres migration failed"),
		ai("add the missing column and rerun the migration"))
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		sb.session(p, "sess-filler-"+n, you("unrelated notes "+n), ai("acknowledged "+n))
	}
}

// docsRepo turns the cwd into a git repo holding one curated doc.
func (sb *sandbox) docsRepo() {
	sb.t.Helper()
	mustInitGitRepo(sb.t, sb.cwd)
	dir := filepath.Join(sb.cwd, "learnings")
	if err := os.MkdirAll(dir, 0755); err != nil {
		sb.t.Fatal(err)
	}
	doc := "# Deploy\n\n## Rollback\n\nUse the rollback script when a deploy fails.\n\n" +
		"## Runbook\n\nThe runbook lists every manual step.\n"
	if err := os.WriteFile(filepath.Join(dir, "deploy.md"), []byte(doc), 0644); err != nil {
		sb.t.Fatal(err)
	}
}

// usageLog reads the telemetry the child wrote.
func (sb *sandbox) usageLog() string {
	data, _ := os.ReadFile(filepath.Join(sb.indexDir(), "usage.jsonl"))
	return string(data)
}

// writeFile writes a file under the sandbox, creating parent dirs.
func (sb *sandbox) writeFile(path, content string) string {
	sb.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		sb.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		sb.t.Fatal(err)
	}
	return path
}

// fakeOllama serves the two endpoints the CLI calls. The embedding is a bag of
// words hashed into 256 buckets: texts that share words have a high cosine and
// unrelated texts sit near zero, which is all the ranking code needs.
func (sb *sandbox) fakeOllama() {
	sb.t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			w.WriteHeader(http.StatusOK)
			return
		}
		var req embedRequest
		json.NewDecoder(r.Body).Decode(&req)
		json.NewEncoder(w).Encode(embedResponse{Embeddings: [][]float32{bagOfWords(req.Input)}})
	}))
	sb.t.Cleanup(srv.Close)
	sb.ollama = srv.URL
}

func bagOfWords(text string) []float32 {
	text = strings.TrimPrefix(text, embedQueryPrefix)
	text = strings.TrimPrefix(text, embedDocPrefix)
	vec := make([]float32, 256)
	for _, w := range strings.Fields(strings.ToLower(text)) {
		w = strings.Trim(w, ".,!?:;#")
		if w == "" {
			continue
		}
		h := fnv.New32a()
		h.Write([]byte(w))
		vec[h.Sum32()%256]++
	}
	return vec
}

// plantIndex writes a vector index into the sandbox, for states the CLI cannot
// be driven into directly: an index from an older model, or one whose project
// directory is gone.
func (sb *sandbox) plantIndex(idx *Index) {
	sb.t.Helper()
	sb.t.Setenv("HOME", sb.home)
	if idx.Files == nil {
		idx.Files = map[string]FileMetadata{}
	}
	if err := saveIndex(idx); err != nil {
		sb.t.Fatal(err)
	}
}

func recentTS() string {
	return time.Now().Add(-2 * time.Hour).UTC().Format("2006-01-02T15:04:05")
}

func TestE2EVersionAndUsage(t *testing.T) {
	sb := newSandbox(t)
	if out := sb.run("--version").want(t, 0).stdout; !strings.HasPrefix(out, "claude-grep "+version) {
		t.Errorf("--version printed %q", out)
	}
	sb.run().want(t, 2, "Usage:", "Exit codes:")
	sb.run(".").want(t, 2, "matches everything")
	sb.run("--", "-").want(t, 2, "matches everything")
}

func TestE2ENoSessionsForProject(t *testing.T) {
	sb := newSandbox(t)
	sb.run("anything").want(t, 2, "no sessions for", "try -a")
}

func TestE2ERegexSearch(t *testing.T) {
	sb := newSandbox(t)
	sb.seed()

	r := sb.run("deploy").want(t, 0,
		"--- "+sb.project()+"/sess-deploy ---", "[YOU]", "[AI ]", "how do we deploy the api")
	if r.stderr != "" {
		t.Errorf("a plain hit should be silent on stderr, got %q", r.stderr)
	}
	if log := sb.usageLog(); !strings.Contains(log, `"mode":"regex"`) || !strings.Contains(log, `"pattern":"deploy"`) {
		t.Errorf("search was not logged to usage.jsonl: %q", log)
	}

	sb.run("-p", "deploy").want(t, 0, "[YOU]").lacks(t, "[AI ]")
	sb.run("-r", "deploy").want(t, 0, "[AI ]").lacks(t, "[YOU]")

	list := sb.run("-l", "deploy").want(t, 0, "sess-deploy  ")
	if strings.Contains(list.stdout, "[YOU]") {
		t.Errorf("-l must list sessions only, got %q", list.stdout)
	}

	var hits []JSONMatch
	js := sb.run("--json", "deploy").want(t, 0)
	if err := json.Unmarshal([]byte(js.stdout), &hits); err != nil {
		t.Fatalf("--json did not print JSON: %v\n%s", err, js.stdout)
	}
	if len(hits) != 2 || hits[0].Session != "sess-deploy" || hits[0].Project != sb.project() {
		t.Errorf("unexpected --json hits: %+v", hits)
	}
}

func TestE2ESearchFlags(t *testing.T) {
	sb := newSandbox(t)
	sb.seed()

	// Flags after the pattern are how agents type it; the cap hint names the total.
	sb.run("deploy", "-n", "1").want(t, 0, "showing 1 of 2", "-d 30")

	// Context: the non-matching neighbour is printed without the match marker.
	sb.run("-C", "1", "missing column").want(t, 0, "the postgres migration failed", "> ")
	sb.run("-B", "1", "missing column").want(t, 0, "the postgres migration failed")
	sb.run("-A", "1", "postgres migration").want(t, 0, "add the missing column")
	sb.run("-C", "1", "--json", "missing column").want(t, 0, `"context_before"`)

	sb.run("-H", "4", "deploy").want(t, 0, "sess-deploy")
	sb.run("-d", "30", "deploy").want(t, 0, "sess-deploy")
	sb.run(`deploy\|postgres`).want(t, 0, "rewrote BRE escapes", "sess-deploy", "sess-db")
	sb.run("api").want(t, 0, "short pattern")
	sb.run("deploy", "src/").want(t, 0, "extra arguments ignored: src/")

	for _, f := range []string{`"flags":"-n"`, `"flags":"-C"`, `"flags":"-H"`, `"flags":"-d"`, `"bre":true`, `"extra_args":true`, `"capped":true`} {
		if !strings.Contains(sb.usageLog(), f) {
			t.Errorf("usage.jsonl is missing %s", f)
		}
	}

	// An old transcript is outside the default window and inside a wider one.
	old := sb.session(sb.project(), "sess-old", you("the quarterly kubernetes audit"))
	stale := time.Now().Add(-20 * 24 * time.Hour)
	os.Chtimes(old, stale, stale)
	sb.run("kubernetes").want(t, 1, "no matches for")
	sb.run("-d", "30", "kubernetes").want(t, 0, "sess-old")
}

func TestE2ENoMatchHints(t *testing.T) {
	sb := newSandbox(t)
	sb.seed()

	// One literal that is nowhere: offer a wider scope and the semantic lane.
	sb.run("zzzqqqxxx").want(t, 1, `no matches for "zzzqqqxxx"`, "retry: claude-grep -a -d 30", "claude-grep -s")

	// A regex that misses while its literal part is present: say how close it was.
	sb.run("deploy[0-9]{9}").want(t, 1, "no matches for", `files contain "deploy"`)

	// An invalid regex is an error, not a miss.
	sb.run("deploy(").want(t, 2, "error:")
}

func TestE2ETokenizedFallback(t *testing.T) {
	sb := newSandbox(t)
	sb.seed()
	sb.run("rollback deploy").want(t, 0, "phrase auto-matched as AND-of-terms (2 tokens)", "sess-deploy")
	if !strings.Contains(sb.usageLog(), `"mode":"tokenized-fallback"`) {
		t.Errorf("tokenized fallback was not logged: %q", sb.usageLog())
	}
}

func TestE2EScopeEscalation(t *testing.T) {
	sb := newSandbox(t)
	sb.session(sb.project(), "sess-local", you("local notes only"))
	sb.session("-srv-other", "sess-remote", you("the terraform state is locked"))

	// A project with almost no history widens to every project and says so.
	sb.run("terraform").want(t, 0, "only 1 sessions in project scope", "-srv-other/sess-remote")
	if !strings.Contains(sb.usageLog(), `"scope":"all"`) {
		t.Errorf("escalated search should log scope all: %q", sb.usageLog())
	}

	// -a asks for it outright and needs no notice.
	sb.run("-a", "terraform").want(t, 0, "-srv-other/sess-remote").lacks(t, "only 1 sessions")
}

func TestE2EWorktreeSearchesMainProject(t *testing.T) {
	sb := newSandbox(t)
	main := filepath.Join(filepath.Dir(sb.cwd), "main")
	mustInitGitRepo(t, main)
	runGit(t, main, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(filepath.Dir(sb.cwd), "wt")
	runGit(t, main, "worktree", "add", "-q", wt)

	mainProject := encodePath(main)
	sb.session(mainProject, "sess-main", you("the cache invalidation bug"))
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		sb.session(mainProject, "sess-pad-"+n, you("padding "+n))
	}

	sb.cwd = wt
	sb.run("invalidation").want(t, 0, "worktree detected — searching main project ("+main+")", "sess-main")
}

func TestE2EDocsLane(t *testing.T) {
	sb := newSandbox(t)
	sb.seed()
	sb.docsRepo()

	// A search answers from sessions and from the repo's curated docs.
	sb.run("rollback").want(t, 0, "sess-deploy", "=== curated docs (learnings/) ===", "deploy.md:3 § Deploy › Rollback")
	sb.run("--no-docs", "rollback").want(t, 0, "sess-deploy").lacks(t, "curated docs")

	// Role filters are about chat turns, so they switch the docs lane off.
	sb.run("-p", "deploy").want(t, 0).lacks(t, "curated docs")

	// No session says "runbook", the doc does: still a hit, exit 0.
	sb.run("runbook").want(t, 0, "curated docs", "Runbook").lacks(t, "no matches for")

	var hits []JSONMatch
	js := sb.run("--json", "runbook").want(t, 0)
	if err := json.Unmarshal([]byte(js.stdout), &hits); err != nil || len(hits) == 0 || hits[0].Source != "docs" {
		t.Errorf("--json should carry the doc hit tagged source=docs: %v %+v", err, hits)
	}

	// A natural-language phrase matches no literal line; the lexical rescue finds it.
	sb.run("script rollback fails").want(t, 0, "curated docs")
	if !strings.Contains(sb.usageLog(), `"docs_engine":"rescue"`) {
		t.Errorf("phrase query should be answered by the rescue engine: %q", sb.usageLog())
	}
}

func TestE2EDocsOnly(t *testing.T) {
	sb := newSandbox(t)
	sb.run("--docs-only", "rollback").want(t, 1, "no curated docs in")

	sb.docsRepo()
	r := sb.run("--docs-only", "rollback").want(t, 0, "=== curated docs (learnings/) ===", "rollback script")
	if strings.Contains(r.stdout, "---") {
		t.Errorf("--docs-only must not print session groups: %q", r.stdout)
	}
	sb.run("--docs-only", "--json", "rollback").want(t, 0, `"source": "docs"`)
	sb.run("--docs-only", "-s", "rollback script").want(t, 0, "curated docs")
	sb.run("--docs-only", "zzzqqqxxx").want(t, 1, `no curated-docs match for "zzzqqqxxx" in learnings/`)
	sb.run("--docs-only", "--no-docs", "rollback").want(t, 2, "contradictory")

	log := sb.usageLog()
	for _, f := range []string{`"mode":"docs-only"`, `"flags":"--docs-only --json"`, `"flags":"--docs-only -s"`} {
		if !strings.Contains(log, f) {
			t.Errorf("usage.jsonl is missing %s in %q", f, log)
		}
	}
}
