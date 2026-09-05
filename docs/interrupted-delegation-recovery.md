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
The experimental Codex CLI adapter is still fresh-task-only, so this checkpoint
does not establish Sol CLI history continuation or automatic coordinator resume.

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
journals remain running. Repeated recovery leaves those journals and the expired
submission unresolved, produces no receipt or redispatch, and denies continuation
of the unfinished history. This verifies a safe refusal, not automatic recovery
of running children.

Run the process-boundary tests with:

```sh
go test -race ./internal/app -run 'Test(InterruptedDelegationRecoveredAfterAbruptProcessDeath|UnfinishedDelegationAfterSIGKILLRemainsUnresolved)$' -count=5
```

These SIGKILL tests target macOS and Linux; local execution evidence is macOS.
They use synthetic loopback provider responses, not live model inference, and
manually expire the fixture claim rather than waiting for a production lease.
They do not establish power-loss durability, automatic daemon restart, recovery
from arbitrary transaction instructions, or stopping remote generation/billing.
General interrupted inference, running children, missing batch slots, uncertain
effects, automatic continuation and broader crash qualification remain required
follow-up work.
