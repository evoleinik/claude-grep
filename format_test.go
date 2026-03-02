package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestFormatTerminalNoANSI(t *testing.T) {
	matches := []Match{
		{
			Message: Message{
				SessionID: "abc123",
				Project:   "test-project",
				Timestamp: "2025-01-01T12:00:00Z",
				Role:      "user",
				Text:      "hello world",
				MsgIndex:  0,
			},
		},
	}
	opts := SearchOpts{MaxResults: 20}

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	formatTerminal(matches, opts)

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if strings.Contains(output, "\033") {
		t.Errorf("output contains ANSI escape sequences: %q", output)
	}
	if !strings.Contains(output, "hello world") {
		t.Errorf("output missing message text: %q", output)
	}
	if !strings.Contains(output, "YOU") {
		t.Errorf("output missing role tag: %q", output)
	}
}
