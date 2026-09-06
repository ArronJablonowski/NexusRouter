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

Records validate bounded canonical metadata. Task/writer identity drift,
malformed records, clock regression before the previous update or other page
errors roll back the whole page and degrade supervisor health. The cursor is
retained on errors; persistent corruption can block subsequent attention pages.
This is deliberately visible failure, not automatic repair of uncertain state.

Read-only access to schemas 1–23 reports `available: false` and an empty array,
distinct from an observed empty schema-24 table. Stop older writers and back up
the database before normal opening migrates it to schema 24. Older binaries
cannot open the new schema; no downgrade or mixed-version writing is supported.
The migration preserves existing tasks, leases and recovery receipts.

## Qualification and remaining work

Tests cover migration preservation, lifecycle changes, stable IDs/bytes,
pagination, privacy, malformed metadata, transaction rollback and read-only
nonmutation. A real dispatcher test creates an expired synthetic writer, records
attention while staying healthy, and preserves its lease/journal without model
calls; the same record survives shutdown and read-only reopening.

HTTP endpoints, acknowledgment/assignment, notifications, immutable attention
transition history, retention/garbage collection, corruption-tolerant page
advancement and broader stall reasons remain follow-up work. This feature neither
resolves uncertain tool effects nor implements idempotent reassignment.
