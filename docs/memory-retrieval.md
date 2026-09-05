# Task-relevant factual memory

Both automatic routing and explicit execution retrieve memory using the current
prompt, or the last user message in a message request. Assistant/tool output and
historical session content do not become the retrieval query. Automatic routing
freezes its selected context for candidate admission and execution.

## Selection

The engine reads ID-cursor pages within the configured scope and privacy policy.
Expired facts are excluded. Each returned fact is validated, including identity,
scope, privacy, UTF-8 and cursor advancement; malformed custom-store results fail
closed. Retrieval has a cooperative three-second deadline, 1,024-fact ceiling
and 8 MiB encoded-fact ceiling. It fails rather than silently ranking a truncated
candidate set. A custom engine must honor context cancellation.

Fact content and query are credential-redacted before matching. Lowercased
Unicode letter/digit tokens of at least two characters form unique sets, excluding
a small fixed English stopword list and the redaction marker. Only positive
overlap qualifies. More matching distinct query terms ranks higher, then higher
confidence, then ascending immutable ID. Repeated words do not add score.
Whole facts are packed under the existing serialized-message byte budget and
`max_facts`; oversized facts are skipped without truncation. Only packed private
facts constrain routing to local models. The original facts are not reordered or
modified by selection.

This is a deterministic lexical baseline, not embeddings, synonym matching or a
language-independent semantic retriever. A preference with no shared query term
will not be injected. Empty/unmatched requests receive no new memory context.
Existing continuation snapshots are separate and remain in session history.

## Recording use

The optional `memory.UseStore` interface exposes `TouchMemoryFact`. SQLite
implements it; existing SDK memory stores without it remain read-only during
execution. The provider wrapper invokes it once per selected fact at the first
model `Stream` boundary, after runtime context admission and durable turn start.
Routing, inspection, query and context rejection do not touch use metadata.

SQLite compares every selected fact field except `LastUse` with the current
record in a serialized writer transaction. Changed revision, content, privacy,
provenance, confidence, creation/update/expiry times, deletion or expiry rejects
use. This also rejects deletion followed by recreation of different content with
the same ID and revision. Concurrent uses only move `LastUse` forward; they do
not change revision, content or correction time.

Store failures, panics, stale snapshots and cancellation block model dispatch
with a generic admission failure. Touches are individual transactions, not an
atomic multi-fact batch: if a later fact fails, earlier timestamps may remain.
The timestamp means an admitted dispatch attempt, not proof of provider receipt
or successful completion. There is no lock held over inference; corrections
after checking cannot recall already selected/sent context. No automatic retry,
fact correction, fitness credit or memory learning is implied.

## Evidence

Tests cover later-page relevance, deterministic ordering, Unicode/repeated terms,
secret-independent ranking, whole-fact budgets, invalid cursors, scan ceilings,
cancellation, read-only legacy SDK stores, privacy routing, selection invalidated
between model discovery and inference, stale/recreated facts, monotonic metadata,
and actual application dispatch updating only the selected SQLite fact.

These are local fixture tests. They do not establish semantic retrieval quality,
large-corpus performance, a model's factual understanding, or secure deletion of
copies already persisted in session history, WAL or backups.
