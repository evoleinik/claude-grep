package main

// Doc-staleness audit (`claude-grep --stale-docs`).
//
// A reflexion-model "divergence" detector for prose docs: flag curated docs
// (learnings/, docs/, tracked README/CLAUDE) that reference code paths which
// changed in git AFTER the doc itself was last edited. A doc edited yesterday
// is never flagged; a doc that names lib/foo.ts where foo.ts moved 40 days
// later is. This is the productised, precision-tuned version of the throwaway
// /tmp scanner spike — two fixes over the spike: a churn ignore-list (so a
// passing mention of high-churn files like schema.prisma doesn't fire), and
// per-ref attribution to the heading it appears under (so the report is
// actionable). Off the search hot path: a dedicated, cron/CI-able subcommand.
//
// Exit: 0 clean · 1 stale docs found · 2 no curated docs here.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const staleThresholdDays = 14

// churnIgnore: files whose git history churns for reasons unrelated to any one
// doc's claim. A doc mentioning prisma/schema.prisma is not "stale" just because
// the schema gained an unrelated column. (The dominant false-positive the spike
// surfaced: 2 of 5 learnings hits were incidental schema.prisma mentions.)
var churnIgnore = map[string]bool{
	"prisma/schema.prisma": true,
	"package.json":         true,
	"package-lock.json":    true,
	"yarn.lock":            true,
	"pnpm-lock.yaml":       true,
	"bun.lockb":            true,
	"go.mod":               true,
	"go.sum":               true,
}

// codePathRe matches repo-relative-ish code path tokens (a name + a code ext).
// Bare basenames are kept (they're grounded by an os.Stat existence check
// before counting), so a true positive like a root-level proxy.ts still fires.
var codePathRe = regexp.MustCompile(`[A-Za-z0-9_][A-Za-z0-9_./\-]*\.(?:tsx?|jsx?|py|go|sh|sql|prisma|ya?ml)\b`)

type staleRef struct {
	Path      string `json:"path"`
	DaysAfter int    `json:"days_after"`
	Heading   string `json:"heading"`
}
type staleDoc struct {
	Doc         string     `json:"doc"`
	DocLastEdit string     `json:"doc_last_edit"`
	RefsChecked int        `json:"refs_checked"`
	MovedAfter  int        `json:"moved_after"`
	Refs        []staleRef `json:"refs"`
}

// gitLastISO returns the committer ISO-8601 date of the last commit touching
// path (repo-relative), or "" on any error / untracked path.
func gitLastISO(repoRoot, path string) string {
	out, err := exec.Command("git", "-C", repoRoot, "log", "-1", "--format=%cI", "--", path).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func parseISO(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// staleDays reports how many days the referenced file post-dates the doc, and
// whether that crosses the threshold. Pure — unit-tested without git.
func staleDays(docISO, refISO string) (int, bool) {
	d, r := parseISO(docISO), parseISO(refISO)
	if d.IsZero() || r.IsZero() || !r.After(d) {
		return 0, false
	}
	days := int(r.Sub(d).Hours() / 24)
	return days, days >= staleThresholdDays
}

func runStaleDocs(cwd string, jsonOut bool) int {
	repoRoot, dirs, ok := discoverDocsDir(cwd)
	if !ok || len(dirs) == 0 {
		fmt.Fprintf(os.Stderr, "no curated docs in %s (no learnings//docs/ dir, README.md, CLAUDE.md, or MEMORY.md)\n", cwd)
		return 2
	}

	refDate := map[string]string{} // ref path -> last-commit ISO (memoised across docs)
	lastISO := func(rel string) string {
		if iso, ok := refDate[rel]; ok {
			return iso
		}
		iso := gitLastISO(repoRoot, rel)
		refDate[rel] = iso
		return iso
	}

	var results []staleDoc

	scan := func(path string) {
		if !strings.HasSuffix(path, ".md") {
			return
		}
		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		docISO := gitLastISO(repoRoot, rel)
		if parseISO(docISO).IsZero() {
			return // untracked or undateable — nothing to compare against
		}
		worst := map[string]staleRef{} // ref -> worst (max-days) hit, attributed to its heading
		checked := map[string]bool{}
		for _, c := range chunkMarkdown(data) {
			for _, ref := range codePathRe.FindAllString(c.Body, -1) {
				if churnIgnore[ref] {
					continue
				}
				if _, err := os.Stat(filepath.Join(repoRoot, ref)); err != nil {
					continue // regex noise: not a real repo file
				}
				checked[ref] = true
				days, stale := staleDays(docISO, lastISO(ref))
				if !stale {
					continue
				}
				if prev, ok := worst[ref]; !ok || days > prev.DaysAfter {
					worst[ref] = staleRef{Path: ref, DaysAfter: days, Heading: c.Heading}
				}
			}
		}
		if len(worst) == 0 {
			return
		}
		refs := make([]staleRef, 0, len(worst))
		for _, r := range worst {
			refs = append(refs, r)
		}
		sort.Slice(refs, func(i, j int) bool { return refs[i].DaysAfter > refs[j].DaysAfter })
		results = append(results, staleDoc{
			Doc: rel, DocLastEdit: firstN(docISO, 10),
			RefsChecked: len(checked), MovedAfter: len(refs), Refs: refs,
		})
	}

	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil {
			continue
		}
		if info.IsDir() {
			filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
				if err != nil || fi.IsDir() {
					return nil
				}
				if p != dir && isIndexDoc(p) {
					return nil // skip a README/MEMORY inside a docs dir (it's a TOC)
				}
				scan(p)
				return nil
			})
		} else {
			scan(dir) // explicitly-listed root README/CLAUDE file
		}
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].MovedAfter != results[j].MovedAfter {
			return results[i].MovedAfter > results[j].MovedAfter
		}
		return results[i].Refs[0].DaysAfter > results[j].Refs[0].DaysAfter
	})

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(results)
		if len(results) > 0 {
			return 1
		}
		return 0
	}

	if len(results) == 0 {
		fmt.Println("stale-docs: clean — no curated doc references code that changed after it")
		return 0
	}
	fmt.Printf("stale-docs: %d doc(s) reference code that changed after the doc was last edited\n\n", len(results))
	for _, r := range results {
		w := r.Refs[0]
		fmt.Printf("  %s  (edited %s · %d/%d refs moved after)\n", r.Doc, r.DocLastEdit, r.MovedAfter, r.RefsChecked)
		fmt.Printf("    └─ %s moved %dd after — under § %s\n", w.Path, w.DaysAfter, w.Heading)
	}
	fmt.Print("\nfix: re-run curate-docs on the worst, or convert the structural claim to a verifier.\n")
	return 1
}

func firstN(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[:n]
}
