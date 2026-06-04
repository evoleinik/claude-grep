package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocsToJSONAndFormat(t *testing.T) {
	docs := []DocMatch{{File: "/r/learnings/vercel.md", Heading: "Cron auth", Text: "guard", Similarity: 0.82}}
	jms := docsToJSON(docs)
	if len(jms) != 1 || jms[0].Source != "docs" || jms[0].Heading != "Cron auth" {
		t.Fatalf("bad docsToJSON: %+v", jms)
	}

	// Session entries must omit the doc-only fields.
	var buf bytes.Buffer
	formatJSON([]Match{{Message: Message{SessionID: "s", Role: "user", Text: "hi"}}}, &buf)
	if bytes.Contains(buf.Bytes(), []byte(`"source"`)) {
		t.Error("session JSON should omit source field")
	}

	// Doc entries serialize source/file/heading.
	var out []map[string]any
	b2, _ := json.Marshal(jms)
	json.Unmarshal(b2, &out)
	if out[0]["source"] != "docs" || out[0]["file"] == nil {
		t.Errorf("doc JSON missing fields: %v", out[0])
	}
}

func TestPrintDocsBlock(t *testing.T) {
	out := captureStdout(t, func() {
		searchQuery = "cron auth"
		printDocsBlock("learnings/", []DocMatch{
			{File: "/r/learnings/vercel.md", Heading: "Cron auth", Line: 42,
				Text: "every cron handler must guard if !secret to avoid Bearer undefined bypass", Similarity: 0.82},
			{File: "/r/CLAUDE.md", Heading: "Rules", Text: "no line known here"}, // Line 0 → no :N
		})
	})
	if !strings.Contains(out, "=== curated docs (learnings/) ===") {
		t.Errorf("missing header:\n%s", out)
	}
	if !strings.Contains(out, "vercel.md:42 § Cron auth") {
		t.Errorf("missing navigable file:line § heading:\n%s", out)
	}
	if !strings.Contains(out, "[0.82]") {
		t.Errorf("missing similarity:\n%s", out)
	}
	// A hit with no known line must not print a bogus ":0".
	if strings.Contains(out, "CLAUDE.md:0") || !strings.Contains(out, "CLAUDE.md § Rules") {
		t.Errorf("Line==0 should omit the colon:\n%s", out)
	}
}

func TestChunkMarkdownLines(t *testing.T) {
	md := "intro text\n# H1\nbody one\n## H2\nbody two\n# H3\nbody three\n"
	//      line1       l2    l3       l4     l5       l6     l7
	chunks := chunkMarkdown([]byte(md))
	want := map[string]int{"(intro)": 1, "H1": 2, "H1 › H2": 4, "H3": 6}
	got := map[string]int{}
	for _, c := range chunks {
		got[c.Heading] = c.Line
	}
	for h, line := range want {
		if got[h] != line {
			t.Errorf("chunk %q: want line %d, got %d (all: %v)", h, line, got[h], got)
		}
	}
}

func TestDocsLabel(t *testing.T) {
	if got := docsLabel("/r/learnings"); got != "learnings/" {
		t.Errorf("want learnings/, got %q", got)
	}
}

func TestUsageEventHasDocsFields(t *testing.T) {
	b, _ := json.Marshal(UsageEvent{DocsResults: 3, DocsEngine: "semantic"})
	if !bytes.Contains(b, []byte(`"docs_results":3`)) || !bytes.Contains(b, []byte(`"docs_engine":"semantic"`)) {
		t.Errorf("docs telemetry not serialized: %s", b)
	}
}

// TestRunDocsOnly is the mechanical check for --docs-only: it must emit ONLY the
// curated-docs block (no session output, so `head -N` can't truncate it) and return
// the right exit code on every branch.
func TestRunDocsOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate usage.jsonl writes

	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init").CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v (%s)", err, out)
	}
	ldir := filepath.Join(repo, "learnings")
	if err := os.Mkdir(ldir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ldir, "vercel.md"),
		[]byte("# Cron auth\nevery handler must guard if !secret to avoid Bearer undefined\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("hit emits only the docs block", func(t *testing.T) {
		var rc int
		out := captureStdout(t, func() { rc = runDocsOnly(repo, "guard", false, false, false, 100) })
		if rc != 0 {
			t.Fatalf("rc=%d, want 0", rc)
		}
		// Nothing precedes the block → no session output to be truncated by head.
		if !strings.HasPrefix(strings.TrimSpace(out), "=== curated docs (learnings/) ===") {
			t.Errorf("docs block is not the sole output:\n%s", out)
		}
		if !strings.Contains(out, "vercel.md:1 § Cron auth") {
			t.Errorf("missing navigable hit (file:line § heading):\n%s", out)
		}
	})

	t.Run("json output is a docs array", func(t *testing.T) {
		var rc int
		out := captureStdout(t, func() { rc = runDocsOnly(repo, "guard", false, true, false, 100) })
		if rc != 0 {
			t.Fatalf("rc=%d, want 0", rc)
		}
		var arr []map[string]any
		if err := json.Unmarshal([]byte(out), &arr); err != nil {
			t.Fatalf("invalid JSON: %v\n%s", err, out)
		}
		if len(arr) != 1 || arr[0]["source"] != "docs" {
			t.Errorf("want 1 docs entry, got %v", arr)
		}
	})

	t.Run("no match exits 1", func(t *testing.T) {
		rc := -1
		captureStdout(t, func() { rc = runDocsOnly(repo, "zzznotpresent", false, false, false, 100) })
		if rc != 1 {
			t.Errorf("rc=%d, want 1", rc)
		}
	})

	t.Run("--no-docs contradiction exits 2", func(t *testing.T) {
		rc := -1
		captureStdout(t, func() { rc = runDocsOnly(repo, "guard", false, false, true, 100) })
		if rc != 2 {
			t.Errorf("rc=%d, want 2", rc)
		}
	})

	t.Run("not a repo exits 1", func(t *testing.T) {
		rc := -1
		captureStdout(t, func() { rc = runDocsOnly(t.TempDir(), "guard", false, false, false, 100) })
		if rc != 1 {
			t.Errorf("rc=%d, want 1", rc)
		}
	})
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var b bytes.Buffer
	io.Copy(&b, r)
	return b.String()
}
