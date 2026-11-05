# Index CPU and search latency

**Type:** Decision doc
**Opened:** 2026-11-04, when Eugene asked whether the embeddings could be made cheaper
or whether search still works. The box was at load 42 on 20 threads, and ollama's
`llama-server` held about 7 of them for `claude-grep --index`.

## Where we are (measured 2026-11-04, Claude)

Recall bench, `bench/queries-labeled.json`, 17 labeled queries, full history:

    2026-09-10   hit@1 10  hit@3 11  hit@10 14  MRR 0.65   1.4s per query
    2026-11-04   hit@1  8  hit@3  9  hit@10 11  MRR 0.52   18s per query (box at load ~40)

Five targets are now absent from the top 100. All five project directories still
exist. The index grew from 2164 to 2760 projects and holds 1,047,180 vectors in
4.8 GB. The 2026-09-10 ADR already found that ranking degrades as the number of
competing sessions grows, and nothing since has changed the model or the ranking.

Usage log, `semantic` mode, median latency by half-month:

    2026-08b 2.8s   2026-09a 2.7s   2026-10a 2.9s   2026-10b 3.1s   2026-11a 5.0s (p90 46s)

Latency does not depend on whether an index run is going (p50 3.2s during the
:00 and :30 windows, 3.7s outside). It follows index size. `semanticSearch`
decodes every non-archived project's gob before it applies the date filter, so a
default 7-day search reads 4.35 GB. Only 0.85 GB of it (146 of 935 projects) has
a transcript modified in the last 7 days.

Indexing, from the journal over 2026-11-02 to 11-04: 37 to 66 busy minutes a day,
3,700 to 7,500 new messages a day. At the default thread count that is about
8 core-hours a day, about 2% of box. Every run also rewrites all 935 live gobs,
4.35 GB, even when nothing changed (the 19:00 no-op run took 27s). That is about
209 GB of SSD writes a day.

Embedding cost per 512-char chunk, same 40 to 80 real chunks, box at load 12 to 28:

    bf16 (current), default threads   6.4 chunks/s   1.53 core-s/chunk
    bf16, num_thread 6                5.7            1.03
    bf16, num_thread 4                4.6            0.85
    bf16, num_thread 2                2.8            0.70
    qat-q8_0, default threads         5.9            1.67   (cosine vs bf16 0.986)

`num_thread` gives vectors identical to the default (min cosine 1.00000), so it
needs no reindex. The i9-10900K has AVX2 and no bf16 or AMX instructions, and
the q8 build was still not faster.

## Criteria (fixed before options)

1. Box CPU spent per day on claude-grep.
2. Search latency for the default `-a -s` search.
3. Result quality on the recall bench. A change must not lower it.
4. Entities added: files, flags, models, config.
5. Whether it needs a full reindex (about 1M vectors, a GPU job).

## Options

### A. Cap embedding threads at 4 on every embed call
One option on the `/api/embed` request, for documents and queries alike.
- Gain: about 45% less index CPU (8.2 to 4.5 core-hours a day). The spike every
  30 minutes drops from about 7 to 10 threads to 4. Same vectors, no reindex.
- Strongest counterargument: index time per day grows from about 50 to 70
  minutes, and a query embed takes about 0.1s longer. The CPU saved is about 1%
  of box, so this does not fix box load, which is mostly agent sessions.
- Failure mode: ollama reloads the runner when a caller sends different options.
  A caller left on default threads would make every switch reload the model.
  Both claude-grep paths must send the same value.

### B. Skip projects with no transcript inside the date window
Before decoding a gob, stat its project's JSONL files and skip it when the newest
one is older than the cutoff. A message's timestamp cannot be newer than its
file's mtime, so the result set is identical.
- Gain: a default 7-day search reads 0.85 GB instead of 4.35 GB.
- Strongest counterargument: there are only about 18 searches a day, so the CPU
  saved is small. The gain is latency on every session's mandatory first search.
- Failure mode: a transcript whose mtime was set back (restore, copy with
  preserved times from a skewed clock) is skipped silently.

### C. Save only the projects an index run changed
- Gain: about 209 GB a day less SSD writing, and a no-op run drops from 27s to
  the decode time alone.
- Strongest counterargument: the disk is at 45% wear after 2.05 PB written, and
  this job is a small share of that.
- Failure mode: a missed dirty flag leaves new vectors unsaved. A test that
  indexes, appends, reindexes and reloads must cover it.

### D. Switch to `embeddinggemma:300m-qat-q8_0`
Measured slower on this CPU and needs a full reindex. Rejected.

### E. Switch to a stronger model for quality (mxbai-embed-large)
The only lever the 2026-09-10 ADR left for quality. It ran 1.8 chunks/s on box,
6x slower than now, so it moves criterion 1 the wrong way unless the incremental
index also moves off box. Not now.

### F. Do nothing
Indexing is about 2% of box. Box load comes from sessions, builds and type checks.

## Recommendation (Claude, before Eugene's answer)

A, B and C together in one change. None changes results, none needs a reindex,
each is a few lines with a test. Then re-run the bench and the usage-log latency
to confirm. Leave the quality drop for a separate GPU bakeoff if it matters.

## Facts that would change this

- If B's measured latency gain is small, decode is not the bottleneck and the
  query-embed wait or disk reads are.
- If a 30-minute batch at 4 threads ever runs past 30 minutes, raise the cap.
- If the quality drop is costing real lookups, E on a GPU becomes the next step.

## Questions for Eugene

1. (Criteria 1 and 2) Build A, B and C now as one claude-grep change?
2. (Criterion 3) Recall fell from hit@1 10 to 8 of 17. Run a GPU model bakeoff
   for it later, or accept it?

## Answers

- Q2, Eugene, 2026-11-04: "let's try gpu now skypilot". The bakeoff runs now.
- Q1: not answered yet.

## GPU bakeoff — 2026-11-04 (Claude)

`sky/bakeoff-models.yaml`, one L4, `bench/model-bakeoff.py` with the production
shape (512-char chunks, tool output included). Pool: the 17 labeled targets plus
800 random distractor sessions, 25 messages each, staged from box so only those
sessions leave it. Candidates: embeddinggemma (control), qwen3-embedding 0.6b,
4b and 8b, mxbai-embed-large, bge-m3. New models run with a similarity floor of
0 because none is calibrated yet.

A winner must also be affordable on box, where the incremental index and every
query embed run. So a large model that wins on the GPU is a ceiling, and only
becomes a choice if indexing moves off box.

### Pilot result: 17 targets + 100 distractors, 31,846 chunks

    model                   hit@1  hit@3  hit@10   MRR    L4 chunks/s
    embeddinggemma (now)      13     14     16    0.810      98
    qwen3-embedding:4b        13     13     15    0.787      25
    qwen3-embedding:8b        12     14     16    0.775      16
    bge-m3                    11     14     15    0.746      86
    qwen3-embedding:0.6b       9     14     14    0.675      72
    mxbai-embed-large          8     14     16    0.657      80

Raw output: `bench/bakeoff-models-2026-11-04-d100.txt`. Vectors are cached in
`gs://airshelf-cg-index-out/bakeoff-models/cache/` (1.3 GB), so re-scoring
these six on this pool needs no GPU. The cluster ran about 80 minutes and was
torn down.

No candidate beats the current model. The 4B ties it at hit@1 and is 13 times
the size. The 8B, the strongest Qwen3, ranks one query lower. mxbai, which won
the 2026-09 four-query test, is now the worst. The same target
(`youthful-fermi`, "the shell eating part of a URL") is absent for all six, so
that miss is a vocabulary gap, not a model gap.

So the quality drop on the full index (hit@1 10 to 8) is not something a model
swap in reach fixes. It follows pool size, as the 2026-09-10 ADR found. The
pilot pool is small, so it cannot rule out a model that degrades more slowly at
800 distractors. That run is about 80 more L4 minutes for three models.

### Decision input (Claude)

Keep embeddinggemma. Do not run the 800-distractor pool unless a model is
worse here by one query or less, which only qwen3-embedding:4b is, and 4B is
not affordable on box CPU.

## Re-measured 2026-11-05 (Claude, asked "is it worth it?")

Same query, `-a -s`, isolated HOME so usage.jsonl is untouched, box load 7 to 20:

    old binary   2.81  2.87  3.01 s   (first run 4.9 s)
    new binary   1.17  1.20  1.20 s   (first run after old 2.5 to 2.9 s)

B cuts a default search from about 2.9 s to 1.2 s. The first run after switching
binaries pays 1.2 to 1.7 s extra, because a different `num_thread` reloads the
ollama runner. That is A's failure mode, measured. No other code on box calls
embeddinggemma today, so it only bites while old and new binaries alternate.

Revised recommendation: ship B and C. Drop A. A saves about 1% of box and adds a
reload penalty for any future caller that sends different options. B is the one
gain a user feels, on every session's first search. C removes 209 GB a day of
writes with a test behind it.
