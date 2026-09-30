# Crash, replay, and lease-recovery qualification

NexusRouter recovers from durable observations; it does not infer safety from
an expired timer or automatically repeat an operation whose effects are known or
uncertain. The current MVP qualification covers these boundaries:

| Boundary | Restart evidence | Recovery rule |
| --- | --- | --- |
| Before `task.started` | A killed claim owner is fenced and a fresh dispatcher requeues the unchanged submission. | At most three requeues; cancellation wins. |
| Provider stream after durable start | A real daemon is killed after a partial model delta and restarted on the same SQLite store. | Preserve the prefix, close the task as interrupted, return no partial answer, and never redispatch inference. |
| Completed task before submission acknowledgement | A killed owner leaves a terminal journal; another process reconstructs the result, including a contiguous chain of up to32 safe route attempts. | Validate bounded history and publish the original result without model, tool, evaluation, audit, or fitness work. |
| Read-only tool and delegated worker | SIGKILL fixtures cover work before child start, pending reads, completed children, batches, and parent-result gaps. | Recover only explicitly read-only, completely evidenced work; retain and validate leases, child journals, acceptance, and parent linkage. |
| Side-effecting tool | SIGKILL fixtures retain consumed approval, an open call, and the writer lease both before and after a synced artifact effect. | Never retry automatically. Report the durable effect/ownership observation for operator reconciliation. |
| Fitness update | An injected failure between immutable evidence and its fitness projection is followed by database close/reopen. | The transaction leaves neither evidence nor a projection. |
| Reviewed compaction start | An injected event-write failure after review validation is followed by database close/reopen. | The new task head and event both roll back; the source task, draft, and review remain unchanged. |
| Extended compaction plan preparation | A Unix child is SIGKILLed both inside the prepared-plan transaction and after commit but before normal caller acknowledgement. | Reopen observes either the original started lifecycle or one exact prepared plan/fact; retry commits or reconciles the same digest without re-running summary inference. |
| Planned compaction activation | A Unix child is SIGKILLed both inside the atomic `context.compacted` transaction and after event/fact commit but before normal acknowledgement. | Reopen observes either no activation or the complete event, task head, lifecycle fact, lineage, and normalized companions; exact retry produces one activation only. |
| Delegated compaction evidence | Single/batch integration, reopen integrity, and adversarial cancellation, timeout, panic, oversize, revocation, policy, engine, and uncertain-effect fixtures rederive the completed child tree. | Only exact completed accepted work can remain in the suffix; failure or drift leaves the plan inactive and never redispatches the parent. |
| Stateful provider rollover | Deterministic fixtures inject completed-turn check, close panic/error, replacement request mismatch, launch/import failure, and cancellation. A submitted Codex fixture is also SIGKILLed after durable activation but before retirement, after retirement but before replacement dispatch, and after terminal commit but before submission acknowledgement. | Activation precedes retirement; restart terminalizes interrupted work or reconstructs the exact completed result without opening or dispatching another provider generation. Ambiguous retirement or replacement remains sticky. |
| Worker finalization | Process death is injected during the terminal-event and lease-release transaction. | Reopen observes either the whole finalization or none of it; uncertain ownership remains fenced. |

All automatic paths are bounded by task/event/byte limits and configuration
identity. Recovery receipts are immutable and idempotent. A lease expiry is an
observation trigger, not evidence that a process or effect stopped.

This does not prove storage-device or kernel crash durability, power-loss
behavior, a live external provider's post-crash behavior, remote workers, hostile
in-process extensions, general recovery of side effects, or automatic OS daemon
restart. The deterministic rollover fixtures do not prove that an external
stateful provider process was terminated. SQLite uses WAL and `synchronous`
`FULL`, but those deployment properties still require qualification on the
operator's actual filesystem and hardware.
