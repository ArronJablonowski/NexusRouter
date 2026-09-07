# Crash, replay, and lease-recovery qualification

DarwinRouter recovers from durable observations; it does not infer safety from
an expired timer or automatically repeat an operation whose effects are known or
uncertain. The current MVP qualification covers these boundaries:

| Boundary | Restart evidence | Recovery rule |
| --- | --- | --- |
| Before `task.started` | A killed claim owner is fenced and a fresh dispatcher requeues the unchanged submission. | At most three requeues; cancellation wins. |
| Provider stream after durable start | A real daemon is killed after a partial model delta and restarted on the same SQLite store. | Preserve the prefix, close the task as interrupted, return no partial answer, and never redispatch inference. |
| Completed task before submission acknowledgement | A killed owner leaves a terminal journal; another process reconstructs the result. | Validate bounded history and publish the original result without model, tool, evaluation, audit, or fitness work. |
| Read-only tool and delegated worker | SIGKILL fixtures cover work before child start, pending reads, completed children, batches, and parent-result gaps. | Recover only explicitly read-only, completely evidenced work; retain and validate leases, child journals, acceptance, and parent linkage. |
| Side-effecting tool | SIGKILL fixtures retain consumed approval, an open call, and the writer lease both before and after a synced artifact effect. | Never retry automatically. Report the durable effect/ownership observation for operator reconciliation. |
| Fitness update | An injected failure between immutable evidence and its fitness projection is followed by database close/reopen. | The transaction leaves neither evidence nor a projection. |
| Reviewed compaction start | An injected event-write failure after review validation is followed by database close/reopen. | The new task head and event both roll back; the source task, draft, and review remain unchanged. |
| Worker finalization | Process death is injected during the terminal-event and lease-release transaction. | Reopen observes either the whole finalization or none of it; uncertain ownership remains fenced. |

All automatic paths are bounded by task/event/byte limits and configuration
identity. Recovery receipts are immutable and idempotent. A lease expiry is an
observation trigger, not evidence that a process or effect stopped.

This does not prove storage-device or kernel crash durability, power-loss
behavior, remote workers, hostile in-process extensions, general recovery of
side effects, or automatic OS daemon restart. SQLite uses WAL and `synchronous`
`FULL`, but those deployment properties still require qualification on the
operator's actual filesystem and hardware.
