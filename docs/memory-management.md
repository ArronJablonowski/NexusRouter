# Operator memory management

The configured CLI, authenticated daemon API and Go SDK maintain factual memory
through the same application service. They never invoke a model, modify routing
fitness, or enable prompt retrieval. Configure `memory.scope`; this is the only
scope these operations may access. Management remains available when
`memory.enabled: false`, so the retrieval kill switch does not prevent cleanup.

Built-in SQLite storage must already exist. Inspection uses a read-only
connection. Writes require existing current-schema WAL storage and never create
or migrate it; normal daemon initialization owns schema setup. An SDK-injected
`MemoryStore` is used instead, and its lifetime remains the embedding host's
responsibility.

## Configured CLI

Use an explicit project configuration to apply the same configured scope,
credential redaction and existing-store-only controls as the application service:

```sh
darwin memory list --config config.yaml --limit 20
darwin memory list --config config.yaml --after language --include-expired
darwin memory show --config config.yaml --id language
darwin memory export --config config.yaml
darwin memory put --config config.yaml --expected 0 < fact.json
darwin memory delete --config config.yaml --id language --expected 1
```

Use the complete fact shown below for creation. A correction supplies the next
fact revision and its current revision in `--expected`; optional `--id` must match
the input. Put requires explicit `--expected`, including zero for creation. The
fact's scope must match configuration. `--scope` and `--db` cannot accompany
`--config`, even as empty flags. Only list accepts `--after`, `--contains`,
`--limit` and `--include-expired`; duplicate or unrelated flags are rejected instead of
silently ignored. Configuration uses normal environment-over-project precedence.

Put reads at most 128 KiB of UTF-8 JSON from stdin, with all required fact fields,
no duplicates, unknown/case-alias keys, null fields, extra JSON values or unpaired
Unicode surrogate escapes. This bounds bytes, not how long an input pipe may wait.
Once admitted, service operations have a cooperative five-second deadline. No
model, daemon startup, database initialization/migration or automatic retry occurs.
Reads work with retrieval disabled and never touch `last_use`.

List emits a JSON array, show a complete fact, and writes a small ID/revision or
deletion acknowledgement. Page limits are 1–100. List pages are live observations,
not a consistent multi-page snapshot; use the explicit export command below for
a bounded consistent snapshot. An output failure returns nonzero but may follow a committed mutation;
inspect current state before deciding whether to write again. Errors omit raw
configuration/backend details. The explicit legacy `--db ... --scope ...` form
remains direct storage access without these configured redaction controls.

### Consistent export

`darwin memory export --config config.yaml` emits one compact JSON object with
`version: 1`, configured `scope`, `captured_at`, and an ordered `facts` array.
It includes all current facts in that scope, including private and expired facts;
it excludes deleted/expired-away facts, retired-ID tombstones and historical
payloads. An empty scope produces `facts: []`. No filter, pagination, ID, revision,
raw database or scope override flags are accepted. Export never reads stdin.

SQLite reads the complete result from one read transaction, so concurrent WAL
writers cannot mix revisions within an export. The timestamp is the operation's
observation time, not a database revision or a claim that the snapshot was pinned
at that precise instant. Fact revisions and last-use values are preserved; export
does not mutate, initialize or migrate storage. Current and newly observed
configured credentials are redacted before output; sensitive identifiers fail
instead of being rewritten. Other personal data remains sensitive.

The complete encoded envelope must fit 8 MiB and contain at most 1,000 facts.
Exceeding either limit or encountering invalid storage fails the whole export:
there is no silent truncation or fallback to live pages. CLI adds one newline
outside that envelope limit and buffers validation before writing stdout. A
failed output write can still leave a partial external copy and returns nonzero.
No output file is created by Darwin; secure any redirected copy yourself.

This is a factual-memory export, not a database backup, restore format or secure
erasure mechanism. Larger snapshot exports and an HTTP export endpoint remain
future work. Exported facts do not authorize import or reuse of retired IDs.

## HTTP contract

All four routes use POST with `Content-Type: application/json`, the daemon's
Bearer token, and an explicit version of 1. They reject browser origins, query
strings, chunked/unknown-length bodies, duplicate/unknown/case-alias keys, null
fields, malformed UTF-8, and unpaired Unicode surrogate escapes. Request bodies
are limited to 128 KiB, including JSON escaping. All displayed top-level fields
are required, including empty cursors/filters and `include_expired`.

| Route | JSON body | Success response |
| --- | --- | --- |
| `/v1/memory/get` | `{"version":1,"id":"language"}` | Complete versioned fact |
| `/v1/memory/query` | `{"version":1,"after_id":"","contains":"","limit":20,"include_expired":false}` | `{"version":1,"facts":[...]}` |
| `/v1/memory/put` | `{"version":1,"fact":{...},"expected_revision":0}` | `{"version":1,"id":"language","revision":1}` |
| `/v1/memory/delete` | `{"version":1,"id":"language","expected_revision":1}` | `{"version":1,"id":"language","deleted":true}` |

A new fact can be supplied in the put request as:

```json
{
  "version": 1,
  "id": "language",
  "scope": "project",
  "revision": 1,
  "content": "Prefers Go for local services.",
  "provenance": "operator-confirmed preference",
  "confidence": 1,
  "privacy": "local_only",
  "created": "2026-09-05T12:00:00Z",
  "updated": "2026-09-05T12:00:00Z"
}
```

The fact's scope must equal configured `memory.scope`. `last_use` and `expires`
are optional timestamps; all other fact fields are required. The body limit may
reject a heavily escaped fact even when its decoded fields fit field limits.

Get includes private and expired records for operator inspection. Query returns
private records too, but omits expired records unless requested. Its filter is
a literal, case-sensitive content substring, not semantic search. Results are
ordered by immutable ID; use the last returned ID as the next `after_id` and
continue until an empty page. Limits are 1–100 facts and 8 MiB encoded output;
an oversized page fails rather than silently truncating (reduce `limit`). Pages
are live observations, not a consistent export snapshot across concurrent edits.
Inspection never updates `last_use`.

All successful responses are HTTP 200 and `Cache-Control: no-store`. Invalid
input receives 400, unsupported media 415, and oversized declared bodies 413.
Missing facts and revision conflicts receive 409. Unavailable stores, denied
configured scope, invalid backend records, cancellation and generic backend
failures receive 503 without raw backend details. A dedicated two-request pool
and cooperative five-second deadline bound these operations; capacity rejection
includes `Retry-After: 1`. That header is not permission to replay a write.

## SDK

```go
fact, err := client.Memory(ctx, "language")
facts, err := client.Memories(ctx, "", "Go", 20, false)
err = client.PutMemory(ctx, fact, expectedRevision)
err = client.DeleteMemory(ctx, "language", expectedRevision)
snapshot, err := client.ExportMemory(ctx)
```

`sdk.MemoryFact` aliases `memory.Fact`; `sdk.ErrMemoryConflict` permits conflict
detection with `errors.Is`. Each operation uses the client's configured scope
and store. SDK calls do not send HTTP requests to a daemon.

`sdk.MemoryExport` aliases `memory.ExportSnapshot`. Custom memory stores must
implement the optional `memory.Exporter` / `sdk.MemoryExporter` interface to
support export, guaranteeing one complete consistent observation and cooperative
cancellation. Unsupported stores fail rather than approximating a snapshot with
`QueryMemory` pages. The service validates and copies custom output before
redaction; it cannot prove a custom backend's isolation or completeness.

## Mutation, privacy, and recovery boundaries

Creation requires `expected_revision: 0`, fact revision 1, equal created/updated
timestamps, and no last-use timestamp. Corrections require the current expected
revision and the next fact revision. SQLite preserves creation/last-use metadata,
rejects backward update times, and refuses to weaken `local_only` to `shareable`.
Deletion requires the current revision. These are direct authenticated operator
actions, not model tools or automatic learned mutations.

SQLite schema 20 retains a minimal `(scope, id)` tombstone when a fact is deleted
or removed by expiry. The retired ID cannot be recreated in that scope: use a
new ID to restore a fact. This prevents stale revision checks from targeting a
replacement whose revision restarted at 1. Tombstones retain no factual content
or provenance, but their scope/ID metadata remains until the database is retired;
automatic tombstone pruning would reintroduce the stale-mutation risk. Migration
cannot reconstruct IDs deleted before this protection
existed; discard outstanding pre-upgrade mutation requests rather than replaying
them against a newly created record. Stop older daemon/CLI writers before
upgrading: an already-open connection from an older binary cannot enforce the
new lifecycle rule. A custom store must provide equivalent
identity-lifecycle protection if it permits operator mutations.

Observed configured credentials are redacted from content and provenance before
write and before output. Reads retain admission credentials and recheck after
backend access, so credentials rotated during a read are also redacted. This
does not erase old stored payloads or promise observation of future rotations.
Credentials in IDs, scopes, cursors or filters cause
rejection instead of identity rewriting, preserving pagination and revision
semantics. Other personal data is not automatically removed; exports remain
sensitive. Stronger identifier validation now rejects invalid UTF-8, control
characters, and surrounding whitespace; old records with such identifiers are
not silently normalized. The legacy `darwin memory --db ...` commands are
separate direct-store operations, not this configured redaction boundary.

No write is automatically retried. A timeout, crash or lost response may happen
after a write commits: inspect the record before deciding what to do next.
Revision checks are not durable idempotency receipts. Trusted custom stores must
implement atomic revision checks, privacy invariants, cancellation and local-only
storage policy themselves; they are not sandboxed or forcibly interrupted. The
adapter cannot make a custom store's delete/recreate lifecycle safe on its behalf.

Deletion removes the active fact, not historical task prompts, WAL pages, backups
or filesystem copies. It cannot recall context already dispatched to a provider.
Secure erasure and exports exceeding the bounded single-snapshot limit are not provided.
