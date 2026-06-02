package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
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
			{File: "/r/learnings/vercel.md", Heading: "Cron auth",
				Text: "every cron handler must guard if !secret to avoid Bearer undefined bypass", Similarity: 0.82},
		})
	})
	if !strings.Contains(out, "=== curated docs (learnings/) ===") {
		t.Errorf("missing header:\n%s", out)
	}
	if !strings.Contains(out, "vercel.md § Cron auth") {
		t.Errorf("missing file § heading:\n%s", out)
	}
	if !strings.Contains(out, "[0.82]") {
		t.Errorf("missing similarity:\n%s", out)
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
