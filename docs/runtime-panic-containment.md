# Runtime panic containment

NexusRouter treats provider, tool, journal, and dispatcher implementations as
failure boundaries. A panic is not evidence that an operation had no effect and
is never exposed through returned errors, durable events, submission results, or
route evidence.

## Provider and journal boundary

A provider panic is normalized to a non-retryable `adapter_failure`. Text already
committed as a durable model delta marks the failure partial. An observed but
uncommitted tool-call proposal or completion marker does not, and is not released
as a completed turn after the provider panics, so no proposed tool can execute.

The provider callback retains stronger boundary errors. Persistence ambiguity,
durable cancellation, and execution-lease loss take precedence even when the
provider ignores the callback result and then panics. A journal panic is treated
as `ErrPersistence`: NexusRouter cannot know whether the append committed and
therefore does not fabricate a terminal event or authorize a retry.

## Tool boundary

Both the legacy and scoped tool interfaces are guarded at invocation. A panic
produces no result content and is conservatively recorded as
`tool.completed{code: tool_failed, effect: uncertain}`. The task then fails, or
becomes canceled when cancellation already won. The same call is not retried,
fallback is not attempted, and no subsequent model turn receives the result.

If the uncertain completion cannot be persisted, the runtime returns
`ErrPersistence` and does not append a task terminal. Operators must inspect the
durable journal before deciding what happened.

## Dispatcher boundary

The daemon adds a final per-claim guard around execution. An unexpected panic
marks supervisor health degraded, stops and joins the claim heartbeat, and keeps
the worker loop alive for unrelated queued submissions. It does not immediately
terminalize or redispatch the ambiguous claim. Normal lease-expiry reconciliation
replays durable history and may requeue only when no `task.started` boundary was
ever committed; started work follows the existing interrupted-work rules.

This is process reliability, not process isolation. A fatal runtime error,
operating-system termination, or malicious extension can still terminate the
process; stronger subprocess/container worker backends remain roadmap work.
