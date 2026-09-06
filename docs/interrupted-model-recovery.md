# Interrupted model-only submission recovery

The dispatcher can resolve an expired running submission containing exactly one
eligible model-only task journal. This covers a durable task start before model
dispatch, a partial response, and a completed text-only turn without a terminal
task receipt. It is failure resolution, not inference resumption.

Recovery requires the original configuration digest and an expired ownership
claim. The bounded journal must replay consistently and contain no current tool
proposals, effects, approvals, workers, retries, or evaluation/error records.
Complete historical tool pairs in the initial context are not current tool
execution. A continuation parent must match the original submission intake.
Multiple task trees remain ineligible for this single-task submission path.
The separate [orphan-worker path](orphan-worker-recovery.md) now supports a
model-only interrupted child under retained worker process-ownership proof.

One SQLite writer transaction appends a terminal `TaskFailed` event with code
`interrupted_model`, updates the task head and submission, invalidates the old
claim token, and records an `interrupted_model` recovery receipt. A durable
submission or task cancellation instead produces `TaskCanceled`. Concurrent
recoverers cannot both commit; any failed write rolls the transaction back.

The original event prefix remains unchanged. Recovery invents no completed turn,
returns no partial text as an answer, grants no success evidence, and dispatches
no model or tool. Existing incomplete-turn markers remain intact. Existing
submission inspection and recovery-history surfaces expose the terminal state
and receipt. Ordinary failures remain ineligible for continuation.

## Explicit continuation after recovery

An operator may now reuse the saved conversation after an exact model-only
recovery, using `--continue-task TASK_ID` or interactive `/resume TASK_ID` followed
by a new prompt. Readiness inspection reports `history_eligible: true` with
reason `recovered_model`. The shared assessment captures a bounded history
(10,000 events / 8 MiB including the recovery terminal), replays that same owned
history, and recomputes the deterministic recovery plan. The stored terminal
must match the plan exactly; its code alone is not sufficient evidence.

Only initial context and completed messages are imported. Partial model deltas
remain in the original journal but are never supplied as an answer or context.
The raw snapshot still reports `failed` and retains `InterruptedTurn` when
appropriate. Continuation creates a fresh parent-linked task; it neither changes
the failed source/submission nor grants success evidence or automatic retry.
Privacy, provider, context, resource and tool-policy admission still apply.

Canceled or ordinary failed tasks, current tool proposals/effects, read-only
tool-recovery paths and unverifiable terminals remain ineligible for this path.
Historical paired tools in initial context remain reference data, not dispatch
authority. Compaction and stored-summary options remain unavailable for failed
sources. General interrupted-task resumption and automatic retries are still
separate unfinished requirements.

## Verification and limits

Planner and storage tests cover safe boundaries, cancellation, parent binding,
invalid histories, size limits, rollback, concurrent recovery and stale-owner
fencing. Application tests kill an owned subprocess with SIGKILL after a model
delta is persisted, expire its fixture lease, and run the dispatcher recovery
path. They assert prefix preservation, one terminal event and receipt, an empty
result, no redispatch, and idempotent repeated recovery. The running-child crash
test first proves this parent-only path refuses; the full daemon then uses the
separate worker-tree proof to record failure without replaying inference.

The model-only crash test additionally launches a second fresh owned process
after recovery. That process loads the same configuration and exact saved task
ID and requests an explicit follow-up. The test compares the exact outbound
messages, verifies one additional inference and a new completed task with the
original parent/session/privacy binding, and checks the durable final answer.
The failed source journal, complete submission status and recovery receipt must
remain unchanged, including after another recovery pass. A canceled source must
produce an admission denial with no additional inference or task creation.

Both children are joined; the first child's SIGKILL and provider disconnection
are verified before recovery. The second child's environment and stdout are
bounded. Lease expiry is advanced in owned fixture storage rather than waiting
for wall-clock expiry, and hardware profiles are deterministic fixtures. This
qualifies actual process separation and persisted-state reuse, not automatic
daemon restart, physical hardware pressure or live-provider compatibility.

```sh
go test -race ./internal/app -run 'Test(InterruptedModelRecoveredAfterAbruptProcessDeath|UnfinishedDelegationAfterSIGKILLRecoversFailure)$' -count=1
```

These tests use loopback provider fixtures, not live Codex or Ollama generation.
They do not prove power-loss durability, automatic daemon restart, stopping
upstream generation or billing, or recovery of arbitrary side effects. Stronger
process isolation, operator reassignment and general interrupted execution remain
follow-up work.
