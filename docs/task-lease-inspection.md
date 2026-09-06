# Task lease and recovery inspection

Inspect a task's durable resource-lease and recovery counts without executing,
repairing or reclaiming anything:

```sh
darwin task leases --db /path/to/telemetry.db --task TASK_ID
```

The Go SDK exposes `Client.InspectTaskLeases(ctx, taskID)`. A running daemon
provides authenticated `GET /v1/tasks/{taskID}/leases`. No configuration file, provider
credentials, model connection or background supervisor is needed for CLI/SDK
inspection of an existing database.

For SDK use, initialize the client with the existing database path (for example,
the `telemetry.database` override); inspection does not start its runtime.

## Returned metadata

The version-1 JSON object contains `task_id`, `task_state`, `sequence`,
`observed_at`, `storage_schema`, `leases` and `recoveries`.

`leases` counts live, expired and released readers/writers separately. **Live
means unexpired, not a proven living process.** Both live and expired unreleased
holders can still exclude conflicting work. Released counts include ordinary
cleanup and verified reclamation.

`recoveries` counts validated private receipts for terminal-reader reclamation,
orphan-worker failure and interrupted-child failure. `interrupted_children` is
a subset of `orphan_workers`, not an additional released reader. Terminal-reader
plus orphan-worker recoveries cannot exceed released readers. Running tasks
cannot have recovery receipts; orphan-worker recovery requires a failed task.

Counts cover leases belonging to the requested task only. A child's paired
interruption receipt belongs to its worker's lease, so query the worker task to
see that count. This is not a subtree summary, per-scope holder list, conflicting
task listing or a guarantee of resource availability. Zero counts do not grant
admission or prove no other task holds a conflicting scope.

The response excludes prompts, outputs, tool arguments, lease tokens, owner
identities, scope names, guard paths and private receipt bodies. Task ID remains
visible because it identifies the requested observation. API query/body input is
rejected, errors are generic and responses are not cached. CLI success means
inspection succeeded, not that execution or recovery is safe.

## Consistency, bounds and compatibility

One read-only SQLite transaction validates the full task journal and observes its
lease and receipt metadata. Task replay is limited to 10,000 events and 8 MiB;
at most 1,000 task-owned leases are admitted. A 1,001st lease, malformed record or
inconsistent receipt rejects the entire observation rather than returning partial
counts. Receipt validation reuses the recovery binding checks; it is not a full
historical corruption audit. Private reference validation is syntactic only:
inspection never opens or probes process guard files.

All surfaces use a five-second cooperative deadline. Bounds limit returned rows
and individual payloads, not total SQLite scan cost. CLI/SDK open existing storage
read-only and never initialize or migrate it. SQLite may create normal WAL/SHM
coordination sidecars when reading a WAL database; this does not modify task,
lease or receipt records. Missing stores are never created.

Schemas 1–2 return `leases: null`; schemas 1–22 return `recoveries: null`.
Null means the feature is unavailable, not a measured zero. Available groups
always contain explicit zero counts. The current reader supports schemas 1–24
and refuses unknown future schemas. This feature adds no migration.

## Verification and limits

Tests cover actual crash-recovery receipts, schema-specific physically absent
tables, malformed/oversized data, impossible public status combinations,
context cancellation, no partial output, unchanged database bytes and journals,
authentication, capacity, request-body non-reading and missing-store protection.

```sh
go test -race ./workers ./internal/telemetry -run '^TestTaskLeaseStatus' -count=3
go test -race ./internal/api -run '^TestTaskLeases' -count=3
go test -race ./sdk/v1 ./internal/cli -run TaskLease -count=3
```

Persistent operator attention, per-scope holder inspection, uncertain-write
resolution and idempotent reassignment remain separate requirements. This
diagnostic changes no permissions, fitness, acceptance or recovery authority.
