#!/usr/bin/env python3
"""Dump the top-N candidate sessions per labeled query, for offline reranking.

Reranking is measured BEFORE it is built: the question is whether a model that
reads query and passage together beats the current order enough to justify
seconds per query. Candidates are produced by the real search path here, then
scored elsewhere (GPU), so the measurement uses exactly what ships.
"""
import json, subprocess, sys, pathlib

CG = str(pathlib.Path.home() / "bin" / "claude-grep")
TOPN = 20

corpus = json.load(open(pathlib.Path(__file__).parent / "queries-labeled.json"))
out = []
for row in corpus:
    q = row["query"]
    res = subprocess.run(
        [CG, "-s", "-a", "-d", "365", "-n", "200", "--json", q],
        capture_output=True, text=True)
    try:
        hits = json.loads(res.stdout or "[]")
    except json.JSONDecodeError:
        hits = []
    seen, cands = set(), []
    for h in hits:
        p = h.get("project")
        if not p or p in seen:
            continue
        # skip the session doing the measuring; it contains every query verbatim
        if "claude-grep" in p:
            continue
        seen.add(p)
        cands.append({"project": p, "session": h.get("session"), "text": (h.get("text") or "")[:1200]})
        if len(cands) >= TOPN:
            break
    out.append({"query": q, "expect_project": row["expect_project"],
                "expect_session": row["expect_session"], "candidates": cands})
    print(f"{len(cands):>3} candidates  {q[:56]}", file=sys.stderr)

json.dump(out, open(sys.argv[1] if len(sys.argv) > 1 else "bench/candidates.json", "w"), indent=1)
