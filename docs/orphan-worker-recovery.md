# Interrupted worker recovery

The daemon can now fail a verified orphaned in-process worker whose execution
child finished before the worker's terminal commit, whose model-only child was
interrupted, or whose child finished explicitly read-only tools before its
interruption. This covers these finalization and model-stream crash boundaries. It does not
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
  or an eligible running history with no pending tools or acceptance records.
  Pending tools, uncertain/confirmed effects, nested, missing and ambiguous
  children remain unsupported. A running child must have no independently held
  resource lease; worker ownership cannot authorize releasing another holder.
- A tool-aware running child must have at least one completed current-journal
  tool pair. Every dispatch and result must explicitly declare `read_only`, and
  every result must record `none` effects. A dispatch's initial `uncertain`
  marker is permitted only when its matching result resolves it. Legacy behavior,
  writes, delegation tools, missing results, evaluation and error records remain
  excluded. Initial-context tool pairs alone are not current execution evidence.
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

For a running model-only child, the same transaction first appends
`task.failed` / `interrupted_model` to the child and updates its head. Both child
and worker failure, the reader release and the receipt commit or roll back
together. The receipt additionally binds the child task, terminal sequence and
event ID. The model-interruption terminal must match the exact deterministic
plan derived from its source prefix; a matching error string alone is not proof.
Partial deltas and the replayed `InterruptedTurn` flag remain intact. No
`turn.completed`, output, evaluation or success is invented. Worker preliminary
acceptance is inconsistent with an interrupted child and is rejected.

For a child with resolved read-only tool history, the child terminal code is
`interrupted_read_only_model`. Its exact deterministic terminal is independently
re-derived from the full source prefix, just like the model-only terminal. Both
tool-call/result pairs and partial later model deltas remain unchanged. The same
transaction, zero-unreleased-child-lease checks and worker process proof apply.
This does not broaden model-only submission recovery or release any child tool
lease; the completed tool's lease must already have been released normally.

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
bounded to 10,000 stored events and 8 MiB, including all new terminals. The parent
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

The model-stream SIGKILL test waits for actual child request dispatch, kills and
reaps the owned helper, and verifies request disconnection before recovery. The
real daemon then fails the interrupted child, worker and parent without another
provider request. Model-stream prefixes remain unchanged and interrupted;
the repaired parent contains bounded failure evidence, not a partial answer.
Provider disconnection does not attest remote generation or billing cessation.

Storage tests additionally cover held guards, writers, unknown ownership,
unresolved child effects, parent-origin mismatch, duplicate owners, concurrent
recovery, and rollback after event/receipt/release failures, cancellation, guard
damage and reference/child/parent drift. Pure planner tests cover lifecycle
ordering, invalid metadata, independent sessions and non-submitted work.

```sh
export DARWIN_PROCESS_OWNER_DIR="$(mktemp -d)"
go test -race ./sessions -run PlanInterruptedWorker -count=3
go test -race ./internal/telemetry -run '^TestOrphan(Worker|Child|ReadOnly)' -count=3
go test -race ./internal/app -run '^Test(WorkerFinalizationProcessDeathRecoveryBoundary|UnfinishedDelegationAfterSIGKILLRecoversFailure|ReadOnlyWorkerAfterSIGKILLRecoversFailure)$' -count=3
```

The read-only SIGKILL case uses the actual built-in `read_file`, verifies its
released reader and recorded result before interrupting the next child request,
then checks the real daemon's child/worker/parent failure and idempotent receipts.
Provider counts remain one coordinator request and two child requests. Preserved
source pairs and no additional read lease establish that recovery did not rerun
the read tool. This qualifies synthetic loopback execution, not live model
behavior or remote-generation cessation.

Still required: pending tool-aware child recovery, missing outcomes, writer and
uncertain-effect resolution, persistent operator attention, idempotent
reassignment, automatic continuation, stronger isolation and guard garbage
collection/reboot qualification. New guards now use durable private storage;
see [ownership retention](process-lifetime-ownership.md).
This is not full worker recovery or complete PRD acceptance. Native
qualification uses synthetic providers on macOS, not live inference or power loss.
