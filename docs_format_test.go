package main

import (
	"bytes"
	"encoding/json"
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
