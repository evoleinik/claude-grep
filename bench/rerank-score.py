#!/usr/bin/env python3
"""Score reranking of pre-dumped candidates. Run on a GPU, never on box.

Two modes, because they have different costs and the choice matters:
  per-candidate  one model call per candidate (20 per query). Accurate, slow.
  batched        one call per query listing all candidates. The only variant
                 that could ship inside an interactive search.
"""
import argparse, json, re, sys, urllib.request
from concurrent.futures import ThreadPoolExecutor

OLLAMA = "http://localhost:11434/api/generate"


def gen(model, prompt, num_predict=8):
    req = urllib.request.Request(
        OLLAMA,
        json.dumps({"model": model, "prompt": prompt, "stream": False,
                    "options": {"temperature": 0, "num_predict": num_predict}}).encode(),
        {"Content-Type": "application/json"})
    return json.loads(urllib.request.urlopen(req, timeout=600).read()).get("response", "")


def score_per_candidate(model, q, cands, workers=8):
    """Candidates are independent, so score them concurrently. Sequentially this
    was 340 calls in series on a GPU that can serve many at once."""
    def one(c):
        p = (f"Question: {q}\n\nPassage:\n{c['text'][:900]}\n\n"
             "How well does this passage answer the question? "
             "Reply with one number 0-10 and nothing else.")
        m = re.search(r"\d+", gen(model, p))
        return int(m.group()) if m else 0
    with ThreadPoolExecutor(max_workers=workers) as ex:
        return list(ex.map(one, cands))


def score_batched(model, q, cands):
    listing = "\n".join(f"[{i}] {c['text'][:320]}" for i, c in enumerate(cands))
    p = (f"Question: {q}\n\nPassages:\n{listing}\n\n"
         "Which passage best answers the question? "
         "Reply with the number in brackets and nothing else.")
    m = re.search(r"\d+", gen(model, p, num_predict=6))
    pick = int(m.group()) if m else 0
    return [1 if i == pick else 0 for i in range(len(cands))]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--candidates", default="bench/candidates.json")
    ap.add_argument("--model", default="gemma3:27b")
    ap.add_argument("--mode", choices=["per-candidate", "batched"], default="batched")
    ap.add_argument("--query-workers", type=int, default=6)
    a = ap.parse_args()

    data = json.load(open(a.candidates))

    def rank_one(row):
        cands = row["candidates"]
        if not cands:
            return 0
        fn = score_per_candidate if a.mode == "per-candidate" else score_batched
        scores = fn(a.model, row["query"], cands)
        order = sorted(range(len(cands)), key=lambda i: -scores[i])
        for pos, i in enumerate(order, 1):
            if row["expect_project"] in cands[i]["project"]:
                return pos
        return 0

    # Queries are independent too. Both levels fan out; ollama serves them
    # concurrently when OLLAMA_NUM_PARALLEL is raised.
    with ThreadPoolExecutor(max_workers=a.query_workers) as ex:
        ranks = list(ex.map(rank_one, data))
    for row, r in zip(data, ranks):
        print(f"  rank={r:<3} {row['query'][:56]}", file=sys.stderr)

    at = lambda k: sum(1 for r in ranks if 1 <= r <= k)
    mrr = sum(1 / r for r in ranks if r) / len(ranks)
    print(f"{a.model} [{a.mode}]  hit@1 {at(1)}  hit@3 {at(3)}  hit@10 {at(10)}  MRR {mrr:.3f}  {ranks}")


if __name__ == "__main__":
    main()
