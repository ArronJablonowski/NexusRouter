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
Multiple task trees and unfinished delegated children remain ineligible.

One SQLite writer transaction appends a terminal `TaskFailed` event with code
`interrupted_model`, updates the task head and submission, invalidates the old
claim token, and records an `interrupted_model` recovery receipt. A durable
submission or task cancellation instead produces `TaskCanceled`. Concurrent
recoverers cannot both commit; any failed write rolls the transaction back.

The original event prefix remains unchanged. Recovery invents no completed turn,
returns no partial text as an answer, grants no success evidence, and dispatches
no model or tool. Existing incomplete-turn markers remain intact. The ordinary
failed history does not become eligible for continuation. Existing submission
inspection and recovery-history surfaces expose the terminal state and receipt.

## Verification and limits

Planner and storage tests cover safe boundaries, cancellation, parent binding,
invalid histories, size limits, rollback, concurrent recovery and stale-owner
fencing. Application tests kill an owned subprocess with SIGKILL after a model
delta is persisted, expire its fixture lease, and run the dispatcher recovery
path. They assert prefix preservation, one terminal event and receipt, an empty
result, no redispatch, and idempotent repeated recovery. The existing running-child
crash test still requires refusal.

```sh
go test -race ./internal/app -run 'Test(InterruptedModelRecoveredAfterAbruptProcessDeath|UnfinishedDelegationAfterSIGKILLRemainsUnresolved)$' -count=1
```

These tests use loopback provider fixtures, not live Codex or Ollama generation.
They do not prove power-loss durability, automatic daemon restart, stopping
upstream generation or billing, or recovery of arbitrary side effects. Stronger
process isolation, operator reassignment and general interrupted execution remain
follow-up work.
