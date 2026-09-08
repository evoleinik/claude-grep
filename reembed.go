package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// An ORPHAN is a project whose transcripts are gone from disk (Claude Code
// deletes them after cleanupPeriodDays) but whose vector index survives. On
// 2026-09-08 that was 1791 projects, 521 of them airshelf worktrees, and the
// gob's 200-char previews were the ONLY remaining record of those sessions.
//
// The live indexer never touches them: it walks ~/.claude/projects, and their
// directories do not exist. So a model change silently strands them — the new
// code skips any index built by a different model, and they become
// unsearchable while still occupying the disk.
//
// This re-embeds each surviving preview under the current model so that history
// stays reachable. Run it after any embedModel change.
//
// Resumable by construction: each project is written as soon as it is done and
// already-migrated projects are skipped, so an interrupted run loses at most
// one project.
// orphanProjects returns indexed projects that are gone from disk AND carry a
// stale model stamp. Live projects are excluded: the indexer owns those, and
// rewriting one from 200-char previews would replace full-text vectors with
// truncated ones.
func orphanProjects() []string {
	home, _ := os.UserHomeDir()
	base := filepath.Join(home, ".claude", "projects")
	entries, err := os.ReadDir(indexDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".gob" {
			continue
		}
		project := strings.TrimSuffix(e.Name(), ".gob")
		if _, err := os.Stat(filepath.Join(base, project)); err == nil {
			continue
		}
		idx := loadIndex(project)
		if len(idx.Entries) == 0 || idx.EmbedModel == indexStamp {
			continue
		}
		out = append(out, project)
	}
	return out
}

// orphanSkipCounts reports how many projects were skipped and why (for the log).
func orphanSkipCounts() (live, migrated int) {
	home, _ := os.UserHomeDir()
	base := filepath.Join(home, ".claude", "projects")
	entries, _ := os.ReadDir(indexDir())
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".gob" {
			continue
		}
		project := strings.TrimSuffix(e.Name(), ".gob")
		if _, err := os.Stat(filepath.Join(base, project)); err == nil {
			live++
			continue
		}
		if idx := loadIndex(project); idx.EmbedModel == indexStamp && len(idx.Entries) > 0 {
			migrated++
		}
	}
	return
}

// shardFilter keeps every nth project starting at i, so N processes can run
// concurrently over disjoint sets. The single-threaded job leaves box at half
// capacity: measured 2026-09-08, adding 4 concurrent embed requests yielded
// 7.8/s on top of the running job's own 7/s.
func shardFilter(projects []string, shard, shards int) []string {
	if shards <= 1 {
		return projects
	}
	var out []string
	for i, p := range projects {
		if i%shards == shard {
			out = append(out, p)
		}
	}
	return out
}

func runReembedOrphans(apply bool, shard, shards int) {
	home, _ := os.UserHomeDir()
	projectsBase := filepath.Join(home, ".claude", "projects")

	_ = projectsBase

	type job struct {
		project string
		count   int
	}
	var jobs []job
	totalVecs, live, migrated := 0, 0, 0
	for _, project := range shardFilter(orphanProjects(), shard, shards) {
		idx := loadIndex(project)
		jobs = append(jobs, job{project, len(idx.Entries)})
		totalVecs += len(idx.Entries)
	}
	live, migrated = orphanSkipCounts()
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].count > jobs[j].count })

	shardNote := ""
	if shards > 1 {
		shardNote = fmt.Sprintf(" [shard %d/%d]", shard, shards)
	}
	fmt.Fprintf(os.Stderr,
		"orphans%s: %d projects, %d vectors to re-embed (%d live projects skipped, %d already on %s)\n",
		shardNote, len(jobs), totalVecs, live, migrated, indexStamp)
	if !apply {
		fmt.Fprintln(os.Stderr, "dry run — pass --apply to re-embed")
		for i, j := range jobs {
			if i >= 5 {
				break
			}
			fmt.Fprintf(os.Stderr, "  %6d  %s\n", j.count, j.project)
		}
		return
	}
	if !ollamaReachable() {
		fmt.Fprintln(os.Stderr, "error: ollama not running — start with: ollama serve")
		os.Exit(2)
	}

	start := time.Now()
	done, dropped, failed := 0, 0, 0
	restamped := 0
	for n, j := range jobs {
		idx := loadIndex(j.project)

		// Fast path: same MODEL, only the chunking version moved. Archived
		// entries are 200-char previews, already shorter than chunkChars, so
		// re-chunking them is a no-op and their vectors are still correct.
		// Re-embedding anyway would cost ~159k calls to produce identical
		// numbers.
		if strings.HasPrefix(idx.EmbedModel, embedModel+"/") || idx.EmbedModel == embedModel {
			short := true
			for _, e := range idx.Entries {
				if len(e.Preview) > chunkChars {
					short = false
					break
				}
			}
			if short {
				idx.EmbedModel = indexStamp
				if err := saveIndex(idx); err == nil {
					restamped++
					continue
				}
			}
		}
		kept := make([]IndexEntry, 0, len(idx.Entries))
		for _, entry := range idx.Entries {
			// No preview means no text survived, so there is nothing to
			// re-embed. Keeping the old vector under the new model's stamp
			// would poison search with a silently incomparable vector.
			if strings.TrimSpace(entry.Preview) == "" {
				dropped++
				continue
			}
			vec, err := embedDoc(entry.Preview)
			if err != nil {
				failed++
				continue
			}
			entry.Vector = vec
			kept = append(kept, entry)
			done++
		}
		idx.Entries = kept
		idx.EmbedModel = indexStamp
		if err := saveIndex(idx); err != nil {
			fmt.Fprintf(os.Stderr, "save %s: %v\n", j.project, err)
			continue
		}
		if n%25 == 0 || n == len(jobs)-1 {
			el := time.Since(start).Seconds()
			rate := float64(done) / el
			eta := 0.0
			if rate > 0 {
				eta = float64(totalVecs-done) / rate / 60
			}
			fmt.Fprintf(os.Stderr, "[%d/%d projects] %d/%d vectors  %.1f/s  eta %.0fm\n",
				n+1, len(jobs), done, totalVecs, rate, eta)
		}
	}
	fmt.Fprintf(os.Stderr, "done: %d re-embedded, %d restamped (model unchanged), %d dropped (no preview), %d failed, %s\n",
		done, restamped, dropped, failed, time.Since(start).Truncate(time.Second))
}
