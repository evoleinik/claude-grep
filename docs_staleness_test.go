package main

import "testing"

func TestStaleDays(t *testing.T) {
	cases := []struct {
		name     string
		doc, ref string
		wantDays int
		wantStl  bool
	}{
		{"ref 40d after doc → stale", "2026-01-01T00:00:00+00:00", "2026-02-10T00:00:00+00:00", 40, true},
		{"ref before doc → not stale", "2026-02-10T00:00:00+00:00", "2026-01-01T00:00:00+00:00", 0, false},
		{"ref 5d after (< threshold) → not stale", "2026-01-01T00:00:00+00:00", "2026-01-06T00:00:00+00:00", 5, false},
		{"ref exactly at threshold → stale", "2026-01-01T00:00:00+00:00", "2026-01-15T00:00:00+00:00", 14, true},
		{"empty doc date → not stale", "", "2026-02-10T00:00:00+00:00", 0, false},
		{"empty ref date → not stale", "2026-01-01T00:00:00+00:00", "", 0, false},
		{"garbage dates → not stale", "not-a-date", "also-bad", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			days, stale := staleDays(c.doc, c.ref)
			if stale != c.wantStl {
				t.Errorf("staleDays(%q,%q) stale=%v, want %v", c.doc, c.ref, stale, c.wantStl)
			}
			if c.wantStl && days != c.wantDays {
				t.Errorf("staleDays(%q,%q) days=%d, want %d", c.doc, c.ref, days, c.wantDays)
			}
		})
	}
}

func TestChurnIgnore(t *testing.T) {
	// High-churn files must be ignored (they drove the spike's false positives).
	for _, p := range []string{"prisma/schema.prisma", "package-lock.json", "go.sum"} {
		if !churnIgnore[p] {
			t.Errorf("expected %q to be churn-ignored", p)
		}
	}
	// A normal source file must NOT be ignored.
	if churnIgnore["lib/db.ts"] {
		t.Errorf("lib/db.ts should not be churn-ignored")
	}
}

func TestCodePathRe(t *testing.T) {
	body := "See `lib/loop-status.ts` and scripts/verify-cron-table.py for details. " +
		"Also proxy.ts at root. Not a path: e.g. or v1.2 or foo.md should not match as code."
	got := map[string]bool{}
	for _, m := range codePathRe.FindAllString(body, -1) {
		got[m] = true
	}
	for _, want := range []string{"lib/loop-status.ts", "scripts/verify-cron-table.py", "proxy.ts"} {
		if !got[want] {
			t.Errorf("expected codePathRe to match %q (matches: %v)", want, got)
		}
	}
	// .md is intentionally excluded (docs reference each other constantly).
	if got["foo.md"] {
		t.Errorf("codePathRe should not match .md files")
	}
}
