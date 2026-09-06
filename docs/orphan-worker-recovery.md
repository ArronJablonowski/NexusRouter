# Interrupted worker recovery

The daemon can now fail a verified orphaned in-process worker before an execution
child was recorded, whose child finished before the worker's terminal commit, whose model-only child was
interrupted, or whose child finished explicitly read-only tools before its
interruption, or whose explicitly read-only calls were dispatched without durable
results. This covers the qualified finalization, model-stream and pending-read
boundaries below. It does not
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
  Alternatively, the strict pre-child lifecycle described below has no linked
  child record at all. The pending-read-only exception below permits a bounded
  set of dispatched calls and same-image child readers. Other pending tools,
  uncertain/confirmed effects, nested and ambiguous children remain unsupported.
  Model-only and resolved-read-only children must have no independently held
  resource lease; worker ownership cannot authorize releasing another holder.
- The resolved-tool/model-interruption path requires at least one completed current-journal
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

## Workers interrupted before child creation

The supported pre-child prefix is `task.started` alone, or `task.started`,
`worker.started`, followed by optional worker heartbeats. The first form covers
the window after reader acquisition but before `worker.started` commits. Any
model/tool event, evaluation, preliminary acceptance, output, error, terminal,
unexpected metadata or heartbeat before worker start makes this path ineligible.

The exact parent delegation must explicitly declare `read_only` and remain
dispatched/pending with the recorded worker origin. Missing/legacy behavior is
not sufficient here. Recovery retains the original unlocked process guard and
the SQLite writer transaction while proving absence of **any** event linked by
`data.parent_task_id` to the worker. A malformed/non-start linked record is not
treated as absence. This check repeats after all writes; a child appearing during
commit invalidates and rolls back the whole recovery.

Recovery appends only the worker's deterministic `task.failed` /
`worker_owner_interrupted`, changes its head, releases its exact historical
reader and writes the distinct `orphan_worker_without_child_unlocked` receipt.
It creates no execution child, tool result, model response or acceptance record.
Existing parent reconciliation can then record a bounded failed delegation and
fail the parent; the parent's reader requires its own ownership proof.

Receipt retry verifies no child linkage, bounds and canonical bytes for every
worker event, and re-derives the exact failure from the strict source prefix.
It rejects altered metadata or extra terminal fields rather than acknowledging
a matching error string. Read queries bound both preflight sizes and actual row
bytes; limits remain 10,000 events/8 MiB including the appended terminal. The
receipt uses existing schema 23 storage; older binaries do not recognize its new
reason and must not be used for recovery after upgrading. No database migration
or change to old receipt reasons is required.

This relies on the trusted store's durable parent linkage and read-only work
contract. It cannot discover an unrecorded external action or an entirely
unlinked corrupt history, prove remote generation stopped, or authorize retries.

## Atomic failure and supervision

### Dispatched read-only calls without recorded results

The pending-tool path requires 1–32 current-journal pending calls, each already
dispatched and explicitly declared `read_only`. All previous tool dispatches and
results must also be read-only, with completed results recording `none` effects.
Undispatched proposals, legacy behavior, delegation tools, writes, acceptance,
evaluation/error records and unresolved effects are excluded. A trusted read-only
declaration is a contract, not evidence that the tool returned a result or that
remote work stopped.

Recovery appends one deterministic `tool.completed` failure per pending call,
with code `tool_failed`, effect `none`, and the fixed JSON body
`{"error":"read_only_tool_interrupted"}`. It then appends child `task.failed` /
`interrupted_read_only_tool` and worker failure in the same transaction. These
records report unavailable results; no actual output, successful execution, new
dispatch or acceptance is invented. Tool identities and ordering remain paired.
The child terminal's causation identifies the original last event, and receipt
inspection re-derives every synthetic completion and terminal from canonical,
bounded original-prefix bytes.

This path alone permits 0–64 unreleased child leases, all readers with the
worker's exact process identity and full retained guard reference. Their ordered
metadata snapshots must remain identical before and after recovery writes.
Foreign/unknown owners, writers, excess readers or lease drift abort recovery.
The worker transaction releases only its own reader, never child readers. The
terminal-reader sweep subsequently proves ownership loss and reclaims each
eligible child reader independently. This grants no retry, reassignment, remote
cancellation or general uncertain-effect recovery authority.

The new child terminal uses existing schema-23 receipt storage without migration.
Older recovery binaries do not recognize it and must not be used to recover
these histories after upgrading.

### Existing terminal and model-interruption paths

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
does not append another event or rewrite the receipt. Existing model/resolved-read
receipt validation checks its binding and terminal metadata; it is not a full
historical corruption audit. Pre-child receipts validate their strict worker
prefix; pending-read receipts validate their full original prefix and synthetic
suffix as described above. Source events and learning evidence
are never rewritten.

At startup and each tick, the dispatcher visits at most 32 running worker-reader
candidates using an independent private rowid cursor. It then runs submission
reconciliation and terminal-reader reclamation. This ordering permits the
existing parent reconciler to record the failed delegation and fail the parent;
the parent's separate reader still requires its own ownership proof and receipt.
Parent submission reconciliation remains restricted by configuration and expired
submission claim. Non-submitted parent journal reconciliation is not added here.

Each page has a five-second cooperative deadline. Worker plus child history is
bounded to 10,000 stored events and 8 MiB, including all new failure events. The parent
is independently bounded to 10,000 events and 8 MiB. Bounds limit returned
candidates and payloads, not total database scan cost or filesystem-call latency.
Unsupported histories remain untouched; corruption/operational errors degrade
supervisor health. The public API does not expose private recovery capabilities.

## Qualification and remaining work

New actual application SIGKILL fixtures run built-in `read_file` and pause after
its handler returned, either before `tool.completed` INSERT (reader already
released) or before workspace-reader release (reader held). They kill and join
only the owned subprocess, then run the real dispatcher. Recovery records fixed
failed results and fails child, worker and parent without another tool execution
or provider request. Counts remain one fixture coordinator and one child request;
discarded file contents never become recovered output. The held-reader case
verifies separate terminal-reader reclamation. Source prefixes, repeat receipts
and writer admission on recovered scopes are checked. These are test-only SQLite
pause points, not interruption inside a running callback, production-model
inference, remote termination or power-loss qualification.

The new app cases passed native race tests three times (4.106s); focused session
and telemetry tests passed. Linux amd64 production cross-build passed. The new
session, telemetry and both actual app SIGKILL cases also passed as CGO-free Linux
arm64 binaries in existing Alpine 3.22 containers, with an unprivileged user,
read-only root and no network; Linux tests were not race-instrumented. Full native
`make check` (format/LOC, vet, full race suite and production build) and `make build`
passed. Final focused session tests passed three runs (17.517s).

Actual application SIGKILL tests pause inside the first execution-child INSERT
and separately inside `worker.started` INSERT after reader acquisition. They kill
and join only the owned subprocess, then reopen the database and start the real
dispatcher. Both cases retain exactly parent+worker, fail both without creating a
child, release both proven readers, preserve source prefixes and acknowledge
repeat recovery without journal or receipt changes. A new writer can acquire
both recovered scopes. Provider counts stay at one fixture coordinator request
and zero execution-child requests. These are test-only SQLite pause points,
not production fault hooks or power-loss simulation.

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

Still required: undispatched or mixed/unsupported pending-tool child recovery, ambiguous/malformed missing
outcomes, writer and
uncertain-effect resolution, persistent operator attention, idempotent
reassignment, automatic continuation, stronger isolation and guard garbage
collection/reboot qualification. New guards now use durable private storage;
see [ownership retention](process-lifetime-ownership.md).
This is not full worker recovery or complete PRD acceptance. Native
qualification uses synthetic providers, not live inference or power loss. The
pre-child cases additionally execute on isolated Linux arm64, including actual
owned-process SIGKILL at both pre-child boundaries; Linux tests are CGO-free
without race instrumentation, while the macOS suite runs with race detection.
