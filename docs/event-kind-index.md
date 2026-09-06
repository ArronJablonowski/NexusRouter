# Schema28 event-kind index

Normal write-capable startup now upgrades SQLite to schema28. It creates
`events_task_kind` on `(task_id, json_extract(body,'$.kind'), sequence)` inside
the existing serialized migration transaction. Journal bodies, evaluations,
fitness, task identities and event order are not rewritten. Each subsequent event
append maintains the index in the same SQLite transaction as the journal row.

Back up operational storage and stop older writers before upgrading. Older
binaries reject schema28; mixed-version writing and downgrades are unsupported.
No user database is upgraded by the test suite. Building this index scans existing
events and consumes additional disk space; migration duration and write/disk
overhead on large production histories remain unqualified. A failed migration
rolls back; it does not ignore or replace a conflicting schema object.

Read-only opening supports schemas1–28 without upgrading. Features retain their
existing minimum schemas; skill comparison/source checking accepts27 and28.
An old schema27 database remains readable without the index. Public metrics,
lease inspection and attention records recognize28 without changing their data
versions or granting new authority. The file-based skill catalog remains schema8.

## Query behavior

The index accelerates exact event-kind lookups within a task. SQLite chooses the
plan; queries do not force the new index or require it on legacy storage.
Output validity still selects the newest100 eligible final-turn observations by
evaluation insertion order **after** domain/profile/validation filtering. It
still checks stored verdicts against actual text/Go syntax, complete identities,
sequence order, duplicates, terminal status and tool/steering boundaries.
The preliminary model-start population query now stops at201 because the only
decisions are empty, at most200, and greater than200. It does not cap the eligible
evidence query at201 tasks or invent samples from unfinished turns.

A CPU profile of the prior eight-model/1,000-seed automatic-task fixture attributed
74.46% of sampled CPU to `OutputValidity` cumulatively. An initial attempt to
combine evidence-count queries added allocations and slowed the full benchmark;
that rewrite was discarded. The index reduced the same local full-task mean from
111.97–113.83ms to49.76–49.86ms across three100-task runs. See
[benchmark boundaries and results](benchmarks.md); this is not a production SLA,
an inference speedup, or proof of performance for arbitrary histories.

Tests cover schema27 read-only nonmigration, migration preserving evidence,
repeat opening, index maintenance on append and failure/reopen after a conflicting
schema object. Regression cases straddle199/200/201/350 model starts, retain
filters before the latest100 window despite newer irrelevant tasks, distinguish
insertion order from lexical IDs/timestamps, preserve missing/unfinished evidence,
and reject malformed selected evidence. Full filesystem power-loss recovery and
large-history migration qualification remain separate requirements.
