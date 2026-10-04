package main

// End-to-end tests for everything that talks to ollama: building the index,
// semantic search, the docs index and the orphan re-embed. The embedder is the
// sandbox's fake, so these run anywhere and never load a model.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestE2EIndexNeedsOllama(t *testing.T) {
	sb := newSandbox(t)
	sb.seed()
	sb.run("--index").want(t, 2, "ollama not running")
	if _, err := os.Stat(filepath.Join(sb.indexDir(), "index.lock")); err == nil {
		t.Error("a failed index run must not leave its lock behind")
	}
}

func TestE2EIndexLifecycle(t *testing.T) {
	sb := newSandbox(t)
	sb.seed()
	sb.fakeOllama()

	sb.run("--index", "--status").want(t, 0, "status:   idle", "no index — run: claude-grep --index")

	sb.run("--index").want(t, 0, "done: 7 files indexed, 0 skipped")
	if _, err := os.Stat(filepath.Join(sb.indexDir(), sb.project()+".gob")); err != nil {
		t.Fatalf("index file was not written: %v", err)
	}

	// Unchanged transcripts are not embedded again.
	sb.run("--index").want(t, 0, "done: 0 files indexed, 7 skipped")

	// A transcript that grew is indexed from where the last run stopped.
	grown := filepath.Join(sb.projectsDir(), sb.project(), "sess-deploy.jsonl")
	sb.appendTurns(grown, time.Now().Add(-30*time.Minute), you("and how do we roll it back"))
	sb.run("--index").want(t, 0, "sess-deploy (1 new of 3 messages)", "done: 1 files indexed, 6 skipped")

	// A transcript that shrank was rewritten, so it is rebuilt from the start.
	sb.writeFile(grown, "")
	sb.appendTurns(grown, time.Now().Add(-20*time.Minute), you("a rewritten transcript"))
	sb.run("--index").want(t, 0, "sess-deploy (1 new of 1 messages)")

	// A touched file with nothing new only refreshes its bookkeeping.
	now := time.Now().Add(-10 * time.Minute)
	os.Chtimes(grown, now, now)
	sb.run("--index").want(t, 0, "done: 0 files indexed, 7 skipped")

	sb.run("--index", "--status").want(t, 0, "status:   idle", "projects: 1", "files:    7", "vectors:  13", "size:")
	sb.run("--index", "--all").want(t, 0, "done: 7 files indexed, 0 skipped")

	// A second indexer backs off while the first holds the lock.
	lock := sb.writeFile(filepath.Join(sb.indexDir(), "index.lock"), "4242")
	sb.run("--index").want(t, 0, "indexing already in progress")
	sb.run("--index", "--status").want(t, 0, "status:   indexing (pid 4242")

	// A lock older than two hours belongs to a dead run and is taken over.
	stale := time.Now().Add(-3 * time.Hour)
	os.Chtimes(lock, stale, stale)
	sb.run("--index").want(t, 0, "done:")
}

func TestE2EIndexChunksLongMessages(t *testing.T) {
	sb := newSandbox(t)
	sb.fakeOllama()
	sentence := "The deploy pipeline builds the image and pushes it to the registry. "
	sb.session(sb.project(), "sess-long", you(strings.Repeat(sentence, 40)))

	sb.run("--index").want(t, 0, "done: 1 files indexed")
	// 2720 chars are cut to the 2048-char embed limit, then into 512-char chunks.
	r := sb.run("--index", "--status").want(t, 0, "files:    1")
	if strings.Contains(r.stdout, "vectors:  1\n") {
		t.Errorf("a long message must be split into several vectors: %s", r.stdout)
	}
}

func TestE2ESemanticSearch(t *testing.T) {
	sb := newSandbox(t)
	sb.seed()

	sb.run("-s", "deploy rollback script").want(t, 2, "ollama not running")

	sb.fakeOllama()
	sb.run("-s", "deploy rollback script").want(t, 2, "no index — run: claude-grep --index")
	sb.run("--index").want(t, 0, "done: 7 files indexed")

	// Meaning, not phrase: the words are scattered through the answer.
	r := sb.run("-s", "deploy rollback script").want(t, 0, "sess-deploy", "> ")
	if !strings.Contains(r.stdout, " [0.") {
		t.Errorf("semantic hits should print their similarity: %s", r.stdout)
	}
	if strings.Contains(r.stdout, "sess-filler") {
		t.Errorf("unrelated sessions must stay under the similarity floor: %s", r.stdout)
	}

	var hits []JSONMatch
	js := sb.run("-s", "--json", "deploy rollback script").want(t, 0)
	if err := json.Unmarshal([]byte(js.stdout), &hits); err != nil || len(hits) == 0 || hits[0].Similarity <= simFloor {
		t.Errorf("--json should carry similarity above the floor: %v %+v", err, hits)
	}

	sb.run("-s", "-C", "1", "verify the rollback plan").want(t, 0, "how do we deploy the api")
	sb.run("-s", "-p", "deploy the api").want(t, 0, "[YOU]").lacks(t, "[AI ]")
	sb.run("-s", "-n", "1", "deploy").want(t, 0, "results capped at 1")
	sb.run("-s", "zebra giraffe").want(t, 1, `no matches for "zebra giraffe"`).lacks(t, "claude-grep -s")

	if !strings.Contains(sb.usageLog(), `"mode":"semantic"`) {
		t.Errorf("semantic search was not logged: %q", sb.usageLog())
	}
}

func TestE2ESemanticFallback(t *testing.T) {
	sb := newSandbox(t)
	sb.seed()
	sb.fakeOllama()
	sb.run("--index").want(t, 0)

	// No transcript holds all four words, so regex and the AND-of-terms layer
	// both miss; the vector layer still finds the closest session.
	sb.run("verify rollback plan zebra").want(t, 0, "no regex/token match — semantic results", "sess-deploy")
	if !strings.Contains(sb.usageLog(), `"mode":"semantic-fallback"`) {
		t.Errorf("semantic fallback was not logged: %q", sb.usageLog())
	}
}

func TestE2ESemanticRefusesForeignModelIndex(t *testing.T) {
	sb := newSandbox(t)
	sb.seed()
	sb.fakeOllama()
	file := filepath.Join(sb.projectsDir(), sb.project(), "sess-deploy.jsonl")
	entry := IndexEntry{SessionID: "sess-deploy", Role: "user", Timestamp: recentTS(),
		Preview: "how do we deploy the api", FilePath: file, Vector: bagOfWords("how do we deploy the api")}

	// Vectors from another model have the same shape and mean something else.
	// Scoring them would return confident nonsense, so the search refuses.
	sb.plantIndex(&Index{Project: sb.project(), EmbedModel: "nomic-embed-text", Entries: []IndexEntry{entry}})
	sb.run("-s", "deploy the api").want(t, 2, "built with a different embedding model", "claude-grep --index --all")

	// With one project rebuilt and one not, it answers from the rebuilt one and says so.
	sb.session("-srv-other", "sess-other", you("deploy the api gateway"))
	other := filepath.Join(sb.projectsDir(), "-srv-other", "sess-other.jsonl")
	sb.plantIndex(&Index{Project: "-srv-other", EmbedModel: indexStamp, Entries: []IndexEntry{{
		SessionID: "sess-other", Role: "user", Timestamp: recentTS(),
		Preview: "deploy the api gateway", FilePath: other, Vector: bagOfWords("deploy the api gateway")}}})
	sb.run("-a", "-s", "deploy the api").want(t, 0, "index rebuild in progress", "sess-other")
}

func TestE2ESemanticArchivedProjects(t *testing.T) {
	sb := newSandbox(t)
	sb.seed()
	sb.fakeOllama()
	// The transcripts of this project are gone; only its index survives.
	sb.plantIndex(&Index{Project: "-srv-gone", EmbedModel: indexStamp, Entries: []IndexEntry{{
		SessionID: "sess-gone", Role: "user", Timestamp: recentTS(), Preview: "the helm chart upgrade",
		FilePath: filepath.Join(sb.projectsDir(), "-srv-gone", "sess-gone.jsonl"),
		Vector:   bagOfWords("the helm chart upgrade")}}})

	sb.run("-a", "-s", "helm chart upgrade").want(t, 1, "1 archived projects", "add --archived")
	sb.run("-a", "-s", "--archived", "helm chart upgrade").want(t, 0, "sess-gone", "the helm chart upgrade")
}

func TestE2EDocsIndex(t *testing.T) {
	sb := newSandbox(t)
	sb.run("--index", "--docs").want(t, 1, "no learnings/ or docs/ dir")

	sb.docsRepo()
	sb.run("--index", "--docs").want(t, 2, "ollama not running")
	sb.run("--index", "--docs", "--status").want(t, 0, "docs index:", "files: 0", "chunks: 0")

	sb.fakeOllama()
	sb.run("--index", "--docs").want(t, 0, "docs indexed: 1 files, 3 chunks")
	sb.run("--index", "--docs", "--status").want(t, 0, "files: 1", "chunks: 3")

	// With an index and an embedder the docs lane ranks by meaning.
	sb.seed()
	r := sb.run("-s", "rollback script deploy fails").want(t, 0, "=== curated docs (learnings/) ===", "Rollback")
	if !strings.Contains(sb.usageLog(), `"docs_engine":"hybrid"`) {
		t.Errorf("-s should use the hybrid docs engine: %q\n%s", sb.usageLog(), r.stdout)
	}
}

func TestE2EReembedOrphans(t *testing.T) {
	sb := newSandbox(t)
	sb.seed()
	vec := []float32{1, 0, 0}
	gone := func(project, stamp string, previews ...string) {
		idx := &Index{Project: project, EmbedModel: stamp}
		for i, p := range previews {
			idx.Entries = append(idx.Entries, IndexEntry{SessionID: "s", MsgIndex: i, Preview: p, Vector: vec})
		}
		sb.plantIndex(idx)
	}
	gone("-gone-old-model", "nomic-embed-text", "the first note", "the second note", "  ")
	gone("-gone-old-chunking", embedModel+"/c256", "a short note", strings.Repeat("long ", 200))
	gone("-gone-migrated", indexStamp, "already current")
	gone(sb.project(), "nomic-embed-text", "live project, the indexer owns it")

	dry := sb.run("--reembed-orphans").want(t, 0,
		"orphans: 2 projects, 5 vectors to re-embed (1 live projects skipped, 1 already on "+indexStamp+")",
		"dry run — pass --apply", "-gone-old-model", "-gone-old-chunking")
	if dry.stdout != "" {
		t.Errorf("the report belongs on stderr, stdout had %q", dry.stdout)
	}
	sb.run("--reembed-orphans", "--shards", "2", "--shard", "1").want(t, 0, "[shard 1/2]", "orphans [shard 1/2]: 1 projects")

	sb.run("--reembed-orphans", "--apply").want(t, 2, "ollama not running")

	sb.fakeOllama()
	sb.run("--reembed-orphans", "--apply").want(t, 0,
		"done: 3 re-embedded, 1 restamped (model unchanged), 1 dropped (no preview), 0 failed")

	// Re-embedded under the current model, the empty preview dropped, the live project untouched.
	t.Setenv("HOME", sb.home)
	old := loadIndex("-gone-old-model")
	if old.EmbedModel != indexStamp || len(old.Entries) != 2 || len(old.Entries[0].Vector) != 256 {
		t.Errorf("orphan was not migrated: stamp=%q entries=%d", old.EmbedModel, len(old.Entries))
	}
	chunked := loadIndex("-gone-old-chunking")
	if len(chunked.Entries[0].Vector) != 3 || len(chunked.Entries[1].Vector) != 256 {
		t.Errorf("a short entry keeps its vector and a long one is re-embedded, got dims %d and %d",
			len(chunked.Entries[0].Vector), len(chunked.Entries[1].Vector))
	}
	if live := loadIndex(sb.project()); live.EmbedModel != "nomic-embed-text" {
		t.Errorf("a live project must be left to the indexer, stamp is now %q", live.EmbedModel)
	}

	sb.run("--reembed-orphans").want(t, 0, "orphans: 0 projects, 0 vectors", "3 already on")
}
