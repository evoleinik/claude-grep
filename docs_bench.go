package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var errSkip = errors.New("engine skipped")

// seam: lets tests stub the semantic engine (ollama-free).
var semanticDocsBenchFn func(query, root string, dirs []string, cap int) ([]DocMatch, error)

// DocsBenchQuery is one labeled corpus row.
type DocsBenchQuery struct {
	Query         string `json:"query"`
	ExpectFile    string `json:"expect_file"`
	ExpectHeading string `json:"expect_heading,omitempty"`
}

// EngineResult: HitRank 0 = miss, >=1 = 1-based rank of expect_file, -1 = skipped.
type EngineResult struct {
	HitRank int `json:"hit_rank"`
	Files   int `json:"files"`
}

type DocsBenchRecord struct {
	Query      string       `json:"query"`
	ExpectFile string       `json:"expect_file"`
	Grep       EngineResult `json:"grep"`
	CgRegex    EngineResult `json:"cg_regex"`
	CgSemantic EngineResult `json:"cg_semantic"`
}

// orWords builds grep's realistic "term1|term2" form: lowercase content words.
// Reuses the BM25 tokenizer's stop-word filter so the grep baseline matches how
// the rest of the tool tokenizes (keeps short acronyms like "ucp", drops "how").
func orWords(query string) string {
	var toks []string
	for _, w := range strings.Fields(strings.ToLower(query)) {
		w = strings.Trim(w, ".,?!:;\"'()")
		if len(w) > 1 && !stopWords[w] {
			toks = append(toks, w)
		}
	}
	return strings.Join(toks, "|")
}

// grepEngine runs the realistic agent grep and records hit + file count.
func grepEngine(query, expect string, dirs []string) EngineResult {
	pat := orWords(query)
	if pat == "" {
		return EngineResult{}
	}
	args := append([]string{"-rIliE", pat}, dirs...)
	out, _ := exec.Command("grep", args...).Output() // exit 1 (no match) is fine
	files := map[string]bool{}
	hit := 0
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if ln == "" {
			continue
		}
		b := filepath.Base(ln)
		files[b] = true
		if b == expect {
			hit = 1
		}
	}
	return EngineResult{Files: len(files), HitRank: hit}
}

// rankOf returns the 1-based rank of expect in docs (0 = absent) + distinct files.
func rankOf(expect string, docs []DocMatch) (rank, files int) {
	seen := map[string]bool{}
	for i, d := range docs {
		b := filepath.Base(d.File)
		seen[b] = true
		if rank == 0 && b == expect {
			rank = i + 1
		}
	}
	return rank, len(seen)
}

func runDocsBenchRecords(corpusPath, root string, dirs []string) []DocsBenchRecord {
	data, err := os.ReadFile(corpusPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bench: cannot read %s: %v\n", corpusPath, err)
		os.Exit(2)
	}
	var queries []DocsBenchQuery
	if err := json.Unmarshal(data, &queries); err != nil {
		fmt.Fprintf(os.Stderr, "bench: %s must be a JSON array of {query,expect_file}: %v\n", corpusPath, err)
		os.Exit(2)
	}

	semFn := semanticDocsBenchFn
	if semFn == nil {
		semFn = hybridDocsSearch // dense ⊕ lexical RRF; degrades to lexical w/o ollama
	}

	recs := make([]DocsBenchRecord, 0, len(queries))
	for _, q := range queries {
		rec := DocsBenchRecord{Query: q.Query, ExpectFile: q.ExpectFile}
		rec.Grep = grepEngine(q.Query, q.ExpectFile, dirs)

		rdocs, _ := regexDocsSearch("(?i)"+orWords(q.Query), dirs, 100)
		rrank, rfiles := rankOf(q.ExpectFile, rdocs)
		rec.CgRegex = EngineResult{HitRank: rrank, Files: rfiles}

		sdocs, serr := semFn(q.Query, root, dirs, 100)
		if serr == errSkip {
			rec.CgSemantic = EngineResult{HitRank: -1}
		} else {
			srank, sfiles := rankOf(q.ExpectFile, sdocs)
			rec.CgSemantic = EngineResult{HitRank: srank, Files: sfiles}
		}
		recs = append(recs, rec)
	}
	return recs
}

// benchVerdict gates CI on RANKING QUALITY: cg-semantic MRR >= grep effective-MRR.
// grep is unranked, so a found doc sits uniformly within its returned pile →
// expected reciprocal rank = 2/(files+1); grep's noise (more files) lowers it.
// This avoids the saturation trap where grep "hits" by dumping the whole corpus.
// All-skipped semantic (no ollama) cannot be judged → pass (don't block CI).
func benchVerdict(recs []DocsBenchRecord) (bool, string) {
	var grepMRR, semMRR float64
	judged := 0
	for _, r := range recs {
		if r.CgSemantic.HitRank == -1 {
			continue // skipped — exclude from both sides for a fair denominator
		}
		judged++
		if r.Grep.HitRank >= 1 && r.Grep.Files > 0 {
			grepMRR += 2.0 / float64(r.Grep.Files+1)
		}
		if r.CgSemantic.HitRank >= 1 {
			semMRR += 1.0 / float64(r.CgSemantic.HitRank)
		}
	}
	if judged == 0 {
		return true, "semantic skipped (no ollama) — gate not evaluated"
	}
	grepMRR /= float64(judged)
	semMRR /= float64(judged)
	return semMRR >= grepMRR, fmt.Sprintf("cg-hybrid MRR=%.2f vs grep eff-MRR=%.2f", semMRR, grepMRR)
}

func runDocsBench(corpusPath string) {
	cwd, _ := os.Getwd()
	root, dirs, ok := discoverDocsDir(cwd)
	if !ok {
		fmt.Fprintln(os.Stderr, "bench: no learnings/ or docs/ dir in this repo")
		os.Exit(2)
	}
	recs := runDocsBenchRecords(corpusPath, root, dirs)

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(recs)

	// Summary → stderr (stdout stays clean JSON).
	n := len(recs)
	var grepAny, grepFiles, rAt1, rAt3, sAt1, sAt3, judged int
	var mrr float64
	for _, r := range recs {
		if r.Grep.HitRank >= 1 {
			grepAny++
		}
		grepFiles += r.Grep.Files
		if r.CgRegex.HitRank == 1 {
			rAt1++
		}
		if r.CgRegex.HitRank >= 1 && r.CgRegex.HitRank <= 3 {
			rAt3++
		}
		if r.CgSemantic.HitRank != -1 {
			judged++
			if r.CgSemantic.HitRank == 1 {
				sAt1++
			}
			if r.CgSemantic.HitRank >= 1 && r.CgSemantic.HitRank <= 3 {
				sAt3++
			}
			if r.CgSemantic.HitRank >= 1 {
				mrr += 1.0 / float64(r.CgSemantic.HitRank)
			}
		}
	}
	avgFiles, semMRR := 0.0, 0.0
	if n > 0 {
		avgFiles = float64(grepFiles) / float64(n)
	}
	if judged > 0 {
		semMRR = mrr / float64(judged)
	}
	fmt.Fprintf(os.Stderr,
		"docs-bench: grep hit@any %d/%d (avg %.1f files) | cg-regex hit@1 %d hit@3 %d | cg-hybrid hit@1 %d hit@3 %d/%d mrr %.2f\n",
		grepAny, n, avgFiles, rAt1, rAt3, sAt1, sAt3, judged, semMRR)

	pass, msg := benchVerdict(recs)
	if !pass {
		fmt.Fprintf(os.Stderr, "FAIL: %s — docs lane is WORSE than grep.\n", msg)
		fmt.Fprintln(os.Stderr, "  fix: rebuild index (claude-grep --index --docs) or relabel bench/docs-queries.json")
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "PASS: %s\n", msg)
}

// runDocsIndexCmd handles `--index --docs [--status]`.
func runDocsIndexCmd(status bool) {
	cwd, _ := os.Getwd()
	root, dirs, ok := discoverDocsDir(cwd)
	if !ok {
		fmt.Fprintln(os.Stderr, "no learnings/ or docs/ dir in this repo")
		os.Exit(1)
	}
	if status {
		idx := loadDocsIndex(root)
		fmt.Printf("docs index: %s\nfiles: %d\nchunks: %d\n", docsIndexPath(root), len(idx.Files), len(idx.Entries))
		return
	}
	if !ollamaReachable() {
		fmt.Fprintln(os.Stderr, "error: ollama not running — start with: ollama serve")
		os.Exit(2)
	}
	if err := refreshDocsIndex(root, dirs, embed); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	idx := loadDocsIndex(root)
	fmt.Fprintf(os.Stderr, "docs indexed: %d files, %d chunks\n", len(idx.Files), len(idx.Entries))
}
