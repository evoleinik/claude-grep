#!/usr/bin/env python3
"""Compare embedding models end-to-end on the labeled session corpus.

A full reindex is ~6h on box, so comparing models against the REAL index costs a
day per candidate. This indexes a SUBSET instead — every labeled target session
plus N random distractor sessions — and scores the same session-rank metric the
real bench uses. Minutes per model, and embeddings cache to disk so re-running
the metric is free.

This is a MODEL-SELECTION instrument, not a gate. `claude-grep --bench` against
the real index stays the gate. It mirrors the production retrieval path
(index.go maxEmbedChars, vector.go simFloor + top-N + session rank); if that
path changes, change it here too.

Usage:  python3 bench/model-bakeoff.py [--distractors 250] [--models a,b]
"""
import argparse, glob, hashlib, json, math, os, pathlib, random, re, sys, time
from concurrent.futures import ThreadPoolExecutor
import urllib.request

OLLAMA = "http://localhost:11434/api/embed"
MAX_EMBED_CHARS = 2048          # index.go
TOP_N           = 100           # vector.go, opts.MaxResults
CACHE = pathlib.Path.home() / ".cache" / "claude-grep-bakeoff"

# name -> (query_prefix, doc_prefix, similarity floor)
# Floors are per-model; the scales are NOT comparable (see vector.go simFloor).
MODELS = {
    "nomic-embed-text":        ("", "", 0.55),                       # the old setup, no prefixes
    "nomic-embed-text+pfx":    ("search_query: ", "search_document: ", 0.55),
    "embeddinggemma":          ("task: search result | query: ", "title: none | text: ", 0.35),
    "mxbai-embed-large":       ("Represent this sentence for searching relevant passages: ", "", 0.62),
}

def ollama_model(name): return name.split("+")[0]

def embed_batch(model, texts, workers=4):
    def one(t):
        req = urllib.request.Request(OLLAMA,
            json.dumps({"model": ollama_model(model), "input": t}).encode(),
            {"Content-Type": "application/json"})
        return json.loads(urllib.request.urlopen(req, timeout=600).read())["embeddings"][0]
    with ThreadPoolExecutor(max_workers=workers) as ex:
        return list(ex.map(one, texts))

def cos(a, b):
    d = sum(x*y for x, y in zip(a, b))
    na = math.sqrt(sum(x*x for x in a)); nb = math.sqrt(sum(y*y for y in b))
    return d/(na*nb) if na and nb else 0.0

def messages(path, include_tools=False):
    """Mirror parseJSONL: type user|assistant, text content only.
    include_tools also pulls tool_use inputs and tool_result content, which are
    13x the text volume, so this is a real population change, not an add-on."""
    out = []
    try: fh = open(path, errors="ignore")
    except OSError: return out
    for ln in fh:
        try: d = json.loads(ln)
        except Exception: continue
        if d.get("type") not in ("user", "assistant"): continue
        c = d.get("message", {}).get("content")
        if isinstance(c, str): t = c
        elif isinstance(c, list):
            parts = []
            for x in c:
                if not isinstance(x, dict): continue
                if x.get("type") == "text":
                    parts.append(x.get("text", ""))
                elif include_tools and x.get("type") in ("tool_use", "tool_result"):
                    v = x.get("input") if x.get("type") == "tool_use" else x.get("content")
                    parts.append(v if isinstance(v, str) else json.dumps(v)[:MAX_EMBED_CHARS])
            t = " ".join(parts)
        else: continue
        if t.strip(): out.append(t[:MAX_EMBED_CHARS])
    return out

def session_key(path):
    p = pathlib.Path(path)
    return f"{p.parent.name}/{p.stem}"

def build_subset(corpus, n_distractors, seed=13):
    base = pathlib.Path.home() / ".claude" / "projects"
    targets = {}
    for row in corpus:
        pat = f"*{row.get('expect_project','')}*/{row.get('expect_session','')}*.jsonl"
        hits = glob.glob(str(base / pat))
        if not hits:
            sys.exit(f"CANNOT MEASURE: no session for label {row.get('expect_project')}/"
                     f"{row.get('expect_session')}  <<{row['query']}")
        targets[row["query"]] = session_key(hits[0])
    chosen = {glob.glob(str(base / f"*{r.get('expect_project','')}*/{r.get('expect_session','')}*.jsonl"))[0]
              for r in corpus}
    allf = [f for f in glob.glob(str(base / "*" / "*.jsonl")) if f not in chosen]
    random.Random(seed).shuffle(allf)
    return sorted(chosen) + allf[:n_distractors], targets

def chunk(text, size):
    """Split a message into ~size-char pieces on sentence-ish boundaries.
    A whole 2048-char message embeds to ONE vector, so a query matching one
    sentence is diluted by everything around it. size=0 keeps whole messages."""
    if not size or len(text) <= size:
        return [text]
    out, cur = [], ""
    for part in re.split(r"(?<=[.!?\n])\s+", text):
        if cur and len(cur) + len(part) > size:
            out.append(cur); cur = part
        else:
            cur = (cur + " " + part).strip() if cur else part
    if cur:
        out.append(cur)
    return out or [text]


def load_corpus_texts(files, targets, cap_distractor, seed=13, chunk_chars=0, include_tools=False):
    """Target sessions keep EVERY message — sampling one could drop the very
    message the query is supposed to recover. Distractor sessions are capped,
    because what discriminates is the number of distinct competing SESSIONS, and
    embedding every message of every distractor costs hours at no extra signal.
    This makes absolute ranks optimistic against the full 197k index; it is a
    RELATIVE comparison between models, which is what the flag is for."""
    tset = set(targets.values())
    docs = []  # (session_key, text)
    for f in files:
        k = session_key(f)
        msgs = messages(f, include_tools)
        if k not in tset and cap_distractor and len(msgs) > cap_distractor:
            msgs = random.Random(seed + hash(k) % 10000).sample(msgs, cap_distractor)
        for t in msgs:
            for c in chunk(t, chunk_chars):
                docs.append((k, c))
    return docs

def cached_vectors(model, docs):
    qp, dp, _ = MODELS[model]
    CACHE.mkdir(parents=True, exist_ok=True)
    sig = hashlib.sha256(("\x00".join(t for _, t in docs) + model).encode()).hexdigest()[:16]
    cf = CACHE / f"{model.replace('/','_')}-{sig}.json"
    if cf.exists():
        return json.loads(cf.read_text()), True
    t0 = time.time()
    vecs = embed_batch(model, [dp + t for _, t in docs])
    cf.write_text(json.dumps(vecs))
    print(f"    embedded {len(docs)} docs in {time.time()-t0:.0f}s", file=sys.stderr)
    return vecs, False

def score(model, corpus, docs, vecs):
    qp, dp, floor = MODELS[model]
    qvecs = embed_batch(model, [qp + r["query"] for r in corpus])
    ranks = []
    for row, qv in zip(corpus, qvecs):
        target = row["_target"]
        scored = [(cos(qv, v), docs[i][0]) for i, v in enumerate(vecs)]
        scored = [s for s in scored if s[0] > floor]
        scored.sort(key=lambda s: -s[0])
        seen, rank, hit = set(), 0, 0
        for sim, key in scored[:TOP_N]:
            if key in seen: continue
            seen.add(key); rank += 1
            if key == target: hit = rank; break
        ranks.append(hit)
    return ranks

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--distractors", type=int, default=150)
    ap.add_argument("--cap-distractor-msgs", type=int, default=25,
                    help="max messages sampled per distractor session (0 = all)")
    ap.add_argument("--models", default="nomic-embed-text,embeddinggemma,mxbai-embed-large")
    ap.add_argument("--include-tools", action="store_true",
                    help="also index tool_use inputs and tool_result content")
    ap.add_argument("--chunk-chars", type=int, default=0,
                    help="split messages into ~N-char chunks (0 = whole message, the current behaviour)")
    ap.add_argument("--corpus", default=str(pathlib.Path(__file__).parent / "queries-labeled.json"))
    a = ap.parse_args()

    corpus = [r for r in json.load(open(a.corpus)) if r.get("expect_session") or r.get("expect_project")]
    files, targets = build_subset(corpus, a.distractors)
    for r in corpus: r["_target"] = targets[r["query"]]
    docs = load_corpus_texts(files, targets, a.cap_distractor_msgs, chunk_chars=a.chunk_chars, include_tools=a.include_tools)
    print(f"subset: {len(files)} sessions ({len(corpus)} targets + {len(files)-len(corpus)} distractors), "
          f"{len(docs)} messages\n", file=sys.stderr)

    print(f"{'model':<24} {'hit@1':>6} {'hit@3':>6} {'hit@10':>7} {'MRR':>6}  ranks")
    print("-" * 86)
    for model in a.models.split(","):
        if model not in MODELS: sys.exit(f"unknown model {model}; known: {list(MODELS)}")
        print(f"  {model} ...", file=sys.stderr)
        vecs, hit = cached_vectors(model, docs)
        if hit: print(f"    (cached)", file=sys.stderr)
        ranks = score(model, corpus, docs, vecs)
        at = lambda k: sum(1 for r in ranks if 1 <= r <= k)
        mrr = sum(1/r for r in ranks if r) / len(ranks)
        print(f"{model:<24} {at(1):>6} {at(3):>6} {at(10):>7} {mrr:>6.3f}  {ranks}")
    print(f"\n(n={len(corpus)} labeled queries; rank is over DISTINCT sessions, "
          f"0 = not in top {TOP_N} chunks)")

if __name__ == "__main__":
    main()
