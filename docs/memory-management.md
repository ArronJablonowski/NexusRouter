# Operator memory management

The authenticated daemon API and Go SDK inspect and maintain factual memory
through the same application service. They never invoke a model, modify routing
fitness, or enable prompt retrieval. Configure `memory.scope`; this is the only
scope these operations may access. Management remains available when
`memory.enabled: false`, so the retrieval kill switch does not prevent cleanup.

Built-in SQLite storage must already exist. Inspection uses a read-only
connection. Writes require existing current-schema WAL storage and never create
or migrate it; normal daemon initialization owns schema setup. An SDK-injected
`MemoryStore` is used instead, and its lifetime remains the embedding host's
responsibility.

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
```

`sdk.MemoryFact` aliases `memory.Fact`; `sdk.ErrMemoryConflict` permits conflict
detection with `errors.Is`. Each operation uses the client's configured scope
and store. SDK calls do not send HTTP requests to a daemon.

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

Current configured credentials are redacted from content and provenance before
write and before output. Credentials in IDs, scopes, cursors or filters cause
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
Secure erasure and coordinated multi-page export snapshots are not provided.
