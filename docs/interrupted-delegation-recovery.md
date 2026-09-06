# Interrupted delegation recovery

The dispatcher can repair a specific crash boundary: all workers have durable
terminal outcomes, but the coordinator's corresponding `tool.completed` record
was never committed. It reconstructs the result without running workers again.

## Recovery conditions

- The durable submission is still running, its claim expired, and its
  configuration digest matches the current dispatcher.
- One coordinator is between model turns with exactly one dispatched pending
  `delegate` or `delegate_batch` call. Other incomplete shapes are not repaired.
- Every requested slot has exactly one origin-bound work record. All work and
  execution descendants are terminal and their lineage, privacy, validation,
  acceptance and output evidence agree. Missing work is not inferred as a
  generic failure. Earlier completed delegations must also agree with evidence.
- There are no unresolved child tools, uncertain child effects or earlier
  uncertain parent tool results. An interrupted failed model stream with no
  outstanding tools can supply failure evidence, not successful output.
- The complete tree fits the shared 66-task, 10,000-event and 8 MiB bounds.

In one writer transaction, recovery appends the missing tool result and a
linked terminal interruption record, updates the parent head, clears the stale
submission token, and records an immutable recovery receipt. Failed transactions
roll back the entire repair. Repeated or concurrent recovery does not duplicate
records. Old owners cannot subsequently append with their revoked claim.

The parent is **failed/interrupted**, not completed: worker output is not a final
coordinator answer. The submission result identifies the parent but contains no
invented answer. Its recovery receipt has reason `interrupted_delegation`.
Submission cancellation or a durable parent-task cancellation instead produces
a canceled parent and submission.

Recovery dispatches no model, tool, evaluator or retry, and never requeues the
original prompt. It does not release resource leases. Claim expiry fences
durable writes; it does not establish that an in-process callback, provider
request or external process has stopped.

## Explicit continuation

Check history readiness without exporting conversation content:

```sh
darwin task continuation --db ./data/darwin.db --task TASK_ID
```

SDK `InspectTaskContinuation` and authenticated HTTP
`GET /v1/tasks/{task}/continuation` expose the same metadata-only status. A
restored eligible checkpoint reports `history_eligible: true` and
`reason: "recovered_delegation"`, with its observed task ID, state and sequence.
This is one bounded, replay-validated database observation; it never creates or
migrates storage, repairs a task, or checks a selected provider's capabilities.

After inspecting the restored tool result, an operator may supply the parent
task ID through the existing `ContinueTaskID` API/SDK field or CLI
`--continue-task TASK_ID` option. This starts a new task using the restored
conversation, not a replay of the original tool call. New model turns use the
normal configuration, privacy, permissions and tool budgets; the model may
propose new work subject to those policies.

Only the exact failed recovery checkpoint is eligible: a
`tool.completed` record with code `delegation_recovered`, followed by
`task.failed` with code `interrupted_after_delegation` causally linked to it.
Replay must show a complete tool pair and no unresolved effects. Ordinary
failures, canceled tasks, unfinished tool calls, and compaction/summary requests
on this special checkpoint remain ineligible. Normal completed-task continuation
is unchanged.

This relies on trusted journal writers, not cryptographic protection against
database tampering. The continuation provider must support conversation history.
The experimental Codex CLI adapter now supports explicit typed-history import;
see [its limits and qualification](codex-history-continuation.md). Recovered
checkpoint admission is tested with fixtures; live Sol testing has used a
completed source, not a crashed/recovered source. Automatic coordinator resume
is still unimplemented.

## Verification scope

Tests inject an SQLite failure at the actual parent tool-result commit after
single and batch worker execution against HTTP fixtures. They verify restored
records, unchanged child journals, zero inference during repair, idempotency,
and an explicitly continued coordinator receiving the restored result without
the fixture requesting repeated work. Storage tests cover stale claims,
concurrent recovery, cancellation, rollback, invalid provenance and bounds.

Subprocess qualification also runs the actual service and worker runtime against
HTTP fixtures in a test-owned executable. A test-only SQLite function pauses
inside a `BEFORE INSERT` trigger for the parent tool result. The test sends
SIGKILL to that exact child process and verifies its signal termination before
opening the database. This prevents graceful cleanup from manufacturing the
state under test. Both single and batch cases prove that terminal worker records
survive while the parent result transaction remains uncommitted. After removing
the fixture trigger and advancing the claim to expired, the dispatcher's recovery
page restores only the two missing parent records. Child journals and the
original parent prefix remain unchanged; repeated recovery adds no receipt or
inference call.

A complementary subprocess test kills the owner while the child's HTTP request
is still in progress. After the connection closes, the parent, work and execution
journals remain running. Repeated parent-only recovery leaves those journals and the expired
submission unresolved, produces no receipt or redispatch, and denies continuation
of the unfinished history. The test then starts the full daemon: retained process
proof and a model-only child prefix permit atomic child/worker failure, followed
by parent failure reconciliation and separate parent-reader reclamation. It
preserves interrupted-turn evidence and never repeats either model request.

[Worker-finalization crash qualification](worker-finalization.md) additionally
kills the application inside the worker terminal/release transaction and after
that transaction while the enclosing tool reader is being released. Preliminary
worker acceptance alone does not enable recovery. A committed worker can enable
parent journal repair without releasing a separate orphaned parent reader lease.
Journal repair and safe resource-holder reconciliation remain distinct concerns.
The daemon now follows journal recovery with a separate
[terminal-reader reclamation page](terminal-reader-recovery.md), which requires
verified unlocked ownership and effect-resolved terminal replay. A preceding
[orphan-worker sweep](orphan-worker-recovery.md) can now fail an interrupted
supervisor when its one execution child is terminal and effect-resolved or has a
provably model-only interruption. Interrupted tool-aware children, uncertain
effects, legacy/missing guards and writers remain
unresolved.

Run the process-boundary tests with:

```sh
go test -race ./internal/app -run 'Test(InterruptedDelegationRecoveredAfterAbruptProcessDeath|UnfinishedDelegationAfterSIGKILLRecoversFailure)$' -count=5
```

These SIGKILL tests target macOS and Linux; local execution evidence is macOS.
They use synthetic loopback provider responses, not live model inference, and
manually expire the fixture claim rather than waiting for a production lease.
They do not establish power-loss durability, automatic daemon restart, recovery
from arbitrary transaction instructions, or stopping remote generation/billing.
Separate [model-only interruption recovery](interrupted-model-recovery.md) can
close eligible single-task inference journals as failed or canceled, without
resuming inference. Tool-aware interrupted children, missing batch slots, uncertain
effects, automatic continuation and broader crash qualification remain required
follow-up work.
