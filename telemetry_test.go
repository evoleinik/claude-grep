package main

import (
	"testing"
	"time"
)

func TestDetectRetryChains(t *testing.T) {
	base := time.Now()
	events := []UsageEvent{
		{Timestamp: base.Format(time.RFC3339), Pattern: "(foo|bar)", Results: 0},
		{Timestamp: base.Add(10 * time.Second).Format(time.RFC3339), Pattern: "foo", Results: 0},
		{Timestamp: base.Add(20 * time.Second).Format(time.RFC3339), Pattern: "foo", Results: 5},
		// Gap > 90s — new chain
		{Timestamp: base.Add(200 * time.Second).Format(time.RFC3339), Pattern: "baz", Results: 20},
		{Timestamp: base.Add(210 * time.Second).Format(time.RFC3339), Pattern: "baz", Results: 20},
	}
	chains := detectChains(events)
	if len(chains) != 2 {
		t.Fatalf("got %d chains, want 2", len(chains))
	}
	if len(chains[0]) != 3 {
		t.Errorf("chain 0: got %d events, want 3", len(chains[0]))
	}
	if len(chains[1]) != 2 {
		t.Errorf("chain 1: got %d events, want 2", len(chains[1]))
	}
}

func TestDetectChainsEmpty(t *testing.T) {
	chains := detectChains(nil)
	if chains != nil {
		t.Errorf("expected nil for empty input, got %v", chains)
	}
}

func TestDetectChainsSingleEvent(t *testing.T) {
	events := []UsageEvent{
		{Timestamp: time.Now().Format(time.RFC3339), Pattern: "foo", Results: 1},
	}
	chains := detectChains(events)
	if len(chains) != 0 {
		t.Errorf("expected 0 chains for single event, got %d", len(chains))
	}
}

func TestDetectDuplicates(t *testing.T) {
	events := []UsageEvent{
		{Pattern: "foo", Scope: "all"},
		{Pattern: "foo", Scope: "all"},
		{Pattern: "foo", Scope: "project"},
		{Pattern: "bar", Scope: "all"},
	}
	dups := detectDuplicates(events)
	if len(dups) != 1 {
		t.Fatalf("got %d duplicate groups, want 1", len(dups))
	}
	if dups[0].count != 2 {
		t.Errorf("got count %d, want 2", dups[0].count)
	}
	if dups[0].pattern != "foo" || dups[0].scope != "all" {
		t.Errorf("got %q/%q, want foo/all", dups[0].pattern, dups[0].scope)
	}
}

func TestDetectDuplicatesNone(t *testing.T) {
	events := []UsageEvent{
		{Pattern: "foo", Scope: "all"},
		{Pattern: "bar", Scope: "all"},
		{Pattern: "baz", Scope: "project"},
	}
	dups := detectDuplicates(events)
	if len(dups) != 0 {
		t.Errorf("expected 0 duplicates, got %d", len(dups))
	}
}
