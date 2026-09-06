# Interrupted worker recovery

The daemon can now fail a verified orphaned in-process worker whose execution
child finished before the worker's terminal commit. This closes the crash gap
between preliminary worker acceptance and durable finalization. It does not
accept the output, rerun inference, resume a task, or reassign work.

## Required evidence

- Exactly one historical reader lease belongs to the worker task, with its
  logical owner matching the worker's durable identity. Writers and ambiguous
  ownership are excluded.
- The original local execution-image guard is independently locked and retained
  through commit. Missing, damaged, unsupported or still-held guards fail closed.
- The running worker journal follows the supported supervisor lifecycle. Its
  preliminary validation/output records remain source facts, not success.
- Exactly one direct, non-worker execution child has a valid terminal projection,
  with no pending tool, interrupted turn, uncertain or confirmed effect. Nested,
  missing, ambiguous and still-running children are unsupported.
- Parent history binds the worker's delegation origin to the actual dispatched
  pending call, including turn, attempt, tool, session, submission and batch index.
  The child may use its own session; submission identities must match. Empty
  submission identities are supported for non-submitted work.

This proof concerns cooperative local in-process lifetime, including exec
replacement. It does not prove that remote generation, detached subprocesses or
external effects stopped. Expiry and preliminary acceptance are never death proof.

## Atomic failure and supervision

One transaction appends `task.failed` with code `worker_owner_interrupted`,
updates the worker head, releases its exact reader and stores a private schema-23
lease receipt with reason `orphan_worker_owner_unlocked`. The receipt binds the
lease metadata, terminal sequence/state and event ID. It contains no model
output, raw lease token or guard path in its body; its private key references the
lease token. No schema migration is needed beyond schema 23.

The transaction revalidates ownership, source event content, child and parent
history before commit. Any failure rolls back all changes. Repeating recovery
does not append another event or rewrite the receipt. Repeat receipt validation
checks its binding and terminal metadata; it is not a full historical corruption
audit. Existing source events and learning evidence are never rewritten.

At startup and each tick, the dispatcher visits at most 32 running worker-reader
candidates using an independent private rowid cursor. It then runs submission
reconciliation and terminal-reader reclamation. This ordering permits the
existing parent reconciler to record the failed delegation and fail the parent;
the parent's separate reader still requires its own ownership proof and receipt.
Parent submission reconciliation remains restricted by configuration and expired
submission claim. Non-submitted parent journal reconciliation is not added here.

Each page has a five-second cooperative deadline. Worker plus child history is
bounded to 10,000 stored events and 8 MiB, including the new terminal. The parent
is independently bounded to 10,000 events and 8 MiB. Bounds limit returned
candidates and payloads, not total database scan cost or filesystem-call latency.
Unsupported histories remain untouched; corruption/operational errors degrade
supervisor health. The public API does not expose private recovery capabilities.

## Qualification and remaining work

The actual app SIGKILL test interrupts worker finalization after child success.
Starting the real dispatcher then produces a failed worker and failed parent,
preserves the child and source prefixes, releases both verified readers, and
allows new writers on both scopes. Fixture coordinator and worker call counts
remain one each. Repeated sweeps preserve journal and receipt bytes.

Storage tests additionally cover held guards, writers, unknown ownership,
unresolved child effects, parent-origin mismatch, duplicate owners, concurrent
recovery, and rollback after event/receipt/release failures, cancellation, guard
damage and reference/child/parent drift. Pure planner tests cover lifecycle
ordering, invalid metadata, independent sessions and non-submitted work.

```sh
go test -race ./sessions -run PlanInterruptedWorker -count=3
go test -race ./internal/telemetry -run '^TestOrphanWorker' -count=3
go test -race ./internal/app -run '^TestWorkerFinalizationProcessDeathRecoveryBoundary$' -count=3
```

Still required: interrupted child recovery, missing outcomes, writer and
uncertain-effect resolution, persistent operator attention, idempotent
reassignment, automatic continuation, stronger isolation and durable guard
retention. This is not full worker recovery or complete PRD acceptance. Native
qualification uses synthetic providers on macOS, not live inference or power loss.
