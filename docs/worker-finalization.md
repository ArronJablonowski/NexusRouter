# Worker finalization and crash boundaries

Production worker execution and validation must return before the supervisor
requests terminal finalization. The application's redacting journal retains
submission fencing and commits three changes in one SQLite transaction:

- The `task.completed`, `task.failed`, or `task.canceled` event.
- The corresponding task-head projection.
- Release of the exact worker reader lease.

Only acknowledged finalization permits successful output delivery. A failed or
ambiguous acknowledgement suppresses output. Adapter panics during supervision
cancel and join active callbacks before the slot or lease can be released.
Legacy journals without the atomic capability still use separate cleanup, but
cleanup failures and panics cannot silently return success.

The new storage capability requires one historical lease for the task/owner,
matching the supplied reader token. Production supervisors generate a fresh
owner and acquire one lease. Ambiguous historical ownership is refused rather
than inferred. Exact acknowledgements require the same event and released
lease; no separate finalization receipt is stored. The ownership query returns
at most two rows, but may scan the lease table without a dedicated index.
An expired lease allows cancellation finalization only, not successful or failed
completion. Normal submission, steering and cancellation rules remain in force.

Earlier evaluation and `worker.completed` events are separate transactions.
Their presence does not prove terminal completion. A finalization failure can
therefore leave preliminary acceptance evidence in a running work journal.
Neither that evidence nor lease expiry authorizes retry or acceptance.

## Process-crash qualification

The tests kill only their owned helper processes with SIGKILL, verify signal
termination, reap the processes and reopen the database afterward. Private test
SQLite functions/triggers pause at specific boundaries; there are no production
crash hooks and graceful cleanup cannot create the tested state.

Storage tests pause before terminal insertion, before reader release inside the
same transaction, and immediately after commit. Before commit, the terminal and
release both roll back, leaving the reader as a writer blocker. After commit,
both survive. Repeated exact acknowledgement changes neither journal nor release.

Application tests run actual coordinator/delegation/worker code against synthetic
loopback providers and cover two distinct boundaries:

1. **Inside worker finalization, before reader release:** the child execution and
   preliminary worker acceptance have completed, but the worker terminal rolls
   back. The parent stays at its pending tool call. Reconciliation leaves the
   running work unresolved, produces no recovery receipt or model call, and
   refuses continuation. Expired unreleased readers continue to exclude writers.
2. **After worker finalization, inside enclosing tool-reader release:** the worker
   terminal and its lease release survive, while the parent reader remains held.
   Existing interrupted-delegation recovery reconstructs the parent tool result
   and closes the parent as interrupted/failed, never as a successful answer.
   It preserves the child histories and the enclosing orphan lease. Repeating
   reconciliation changes no journal, receipt or lease and dispatches no model.

These checks distinguish the per-worker `delegation-<parent>` scope from the
parent tool's `delegation` scope. Completing one is not proof that every enclosing
resource holder has stopped or that every lease is released.

Run the focused qualification with:

```sh
go test -race ./internal/telemetry -run '^TestWorkerFinishSIGKILLAtomicity$' -count=3
go test -race ./internal/app -run '^TestWorkerFinalizationProcessDeathRecoveryBoundary$' -count=3
```

SIGKILL qualification targets macOS and Linux; execution evidence for this
checkpoint is macOS. It uses fixture responses and manually expires claims and
unreleased readers. Linux compilation is not Linux execution. The tests do not
establish power-loss behavior, remote generation cessation, live model behavior,
automatic daemon restart, or all failed/canceled terminal process boundaries.

## Remaining recovery work

No general process-death proof, orphan release, idempotent reassignment, automatic
continuation or forced in-process termination is provided by this change.
Unresolved readers can still block availability. Even when the test knows its
child died, production recovery has no such trusted observation and does not
release those leases. Stronger lifecycle ownership and safe reconciliation are
still required by the PRD. See [interrupted delegation recovery](interrupted-delegation-recovery.md)
and [reader/writer execution](reader-writer-execution.md).
