# Durable lease attention

The daemon records expired, unreleased reader and writer leases for operator
inspection. This includes legacy or unverifiable process ownership: creating an
attention record never requires a guard probe or authorizes release or retry.

```sh
darwin resources attention --db path/to/darwin.db
darwin resources attention --db path/to/darwin.db --state all --limit 25
```

CLI defaults are `--state open` and `--limit 25`. Follow a nonempty
`next_cursor` with `--after`; limit is 1–100. The Go SDK exposes
`Client.ListLeaseAttention(ctx, LeaseAttentionOptions{State: "open", Limit: 25})`.
SDK options are explicit. Both surfaces open existing storage read-only, without
migration, execution, profiling or ownership probes. Missing databases are not
created. SQLite may create ordinary WAL/SHM coordination sidecars.

Authenticated daemon clients can use `GET /v1/resources/attention`, with optional
`state`, `after` and `limit` query parameters and the same defaults as the CLI.
URL-encode cursor values. No request body is accepted. Unknown, duplicate, empty,
malformed or out-of-range parameters are rejected before storage access; limits
must use canonical decimal notation. The response is JSON with `Cache-Control:
no-store`. Normal daemon authentication and browser-origin denial apply.

HTTP inspection shares two bounded control slots and a cooperative five-second
deadline. Capacity exhaustion returns 503 with `Retry-After: 1`; unavailable
storage returns a generic 503 without private errors. Invalid backend metadata
or records outside the requested filter/cursor range fail closed. The endpoint
does not acknowledge, resolve or create attention records; only the daemon's
separate observation sweep maintains them.

Each versioned record contains an opaque attention ID, task ID, writer flag,
first-observed and last-change times, observed lease expiry, state and reason:

| State | Reason | Observed condition |
| --- | --- | --- |
| open | expired_unreleased | Lease has expired but remains unreleased. |
| resolved | lease_renewed | Same lease was renewed beyond the observation time. |
| resolved | lease_released | Lease was released by its normal authority or a separate verified recovery. |

The ID and first-observed time remain stable across renewal, expiry recurrence
and release. Unchanged observations do not rewrite records. `updated_at` means
last evidence change, not last successful scan. Records can be stale: no state
proves a process is alive/dead or grants admission, acceptance, retry or release.
An open attention item does not by itself mark the daemon unhealthy.

## Observation history

Schema 25 retains each changed observation in an append-only runtime history.
The current projection and its new history row commit together; an ignored or
failed history insert rolls back the projection as well. Unchanged observations
append nothing. Before updating an existing record, the observer verifies that
the latest history snapshot matches its current projection.

```sh
darwin resources attention-history --db path/to/darwin.db --id ATTENTION_ID
darwin resources attention-history --db path/to/darwin.db --id ATTENTION_ID --after-sequence 25 --limit 25
```

The SDK exposes `ListLeaseAttentionHistory(ctx, id,
LeaseAttentionHistoryOptions{Limit: 25})`. Authenticated HTTP clients use
`GET /v1/resources/attention/{id}/history`, optionally with `after_sequence` and
`limit`. CLI/HTTP defaults are sequence 0 and limit 25; limits are 1–100 and
integer parameters must use canonical decimal notation. History inspection uses
the same read-only, authentication, privacy and deadline rules as the current
list. A missing record on schema 25 returns an inspection error (HTTP 404).

Each transition contains a version, per-record sequence, kind and complete
content-free observation. Newly observed records start at sequence 1 with kind
`observed`. Migration preserves each existing record byte-for-byte at sequence 1
with kind `baseline`: it represents the last pre-migration state, not a claim
that earlier transitions were recorded. Subsequent changes use `observed`.
Never infer the number or timing of pre-migration changes from a baseline.

Pages validate contiguous sequences, stable record identity, chronological
ordering and current/history agreement in one transaction. Continue with
`NextSequence` while `HasMore` is true; to check for later appended transitions,
retain the last returned item's sequence. An empty page beyond the current head
means caught up at that read. Concurrent appends can appear on subsequent pages.
This is not cryptographic tamper evidence: an operator with direct database
write access can alter storage. The runtime offers no history edit/delete action.
History does not grant approval, retry, release or process-death authority.

Public records omit lease tokens, logical owners, resource scopes, process guard
references, prompts and tool output. Task IDs may still be sensitive. Ordering is
lexical by opaque ID, not chronological. Each page is transactionally consistent;
concurrent insertion or state changes can affect later pages. Restart pagination
from the beginning to refresh; this is not an exactly-once change feed.

## Observation and storage

Schema 24 adds a private table linked to the original lease and task. The daemon
visits up to 32 candidates after each recovery pass, starting immediately and
then on its five-second tick. Previously observed leases remain candidates so
renewal/release can resolve them. A separate private rowid cursor advances each
successful page and wraps at the end. Pages use a five-second cooperative
deadline and commit atomically. This bounds returned rows, not database scan
cost or total delay with a large retained population.

Each observation transaction reserves the SQLite writer before its first schema
read, preventing stale-snapshot write upgrades under concurrent daemon activity.
The reservation changes no rows; the current-schema guard still precedes observation.

Records validate bounded canonical metadata. Task/writer identity drift,
malformed records, clock regression before the previous update or other page
errors roll back the whole page and degrade supervisor health. The cursor is
retained on errors; persistent corruption can block subsequent attention pages.
This is deliberately visible failure, not automatic repair of uncertain state.

Read-only access to schemas 1–23 reports `available: false` and an empty array,
distinct from an observed empty attention table. History inspection on schemas
1–24 reports unavailable without migration. Stop older writers and back up
the database before normal opening migrates it to schema 25. Older binaries
cannot open the new schema; no downgrade or mixed-version writing is supported.
The migration preserves existing tasks, leases and recovery receipts.

## Qualification and remaining work

Tests cover migration preservation, lifecycle changes, stable IDs/bytes,
pagination, privacy, malformed metadata, transaction rollback and read-only
nonmutation. A real dispatcher test creates an expired synthetic writer, records
attention while staying healthy, and preserves its lease/journal without model
calls; the same record survives shutdown and read-only reopening.

HTTP tests cover authentication, query/body rejection, response validation,
control capacity, read-only SQLite access and actual daemon wiring. A
two-connection regression reproduces the former stale-snapshot failure and
checks writer reservation, schema rejection and lock release.

Acknowledgment/assignment, notifications, retention/garbage collection, corruption-tolerant page
advancement and broader stall reasons remain follow-up work. This feature neither
resolves uncertain tool effects nor implements idempotent reassignment.
