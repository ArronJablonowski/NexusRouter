# Configured outcome rollback supervision

NexusRouter can supervise activation-bound outcome comparisons in the daemon or
an explicitly started Go SDK lifecycle. The supervisor is disabled by default.
It observes one configured catalog scope and one exact configured execution
model; it does not run a validator, provider, tool, or LLM judge.

The evidence is observational and advisory. Exposure, outcome quality, elapsed
time, workload mix, and task difficulty can be confounded. A configured drop is
permission to apply the existing outcome rollback policy, not causal proof that
the active skill caused the drop. The supervisor never fabricates deterministic
validation evidence. Subjective creative or unknown work requires operator-owned
`user_feedback`; an evaluator cannot manufacture that source. Judge-only evidence
is ineligible and a judge never has supervisor authority.

## Configuration

```yaml
skills:
  enabled: true
  root: /absolute/private/catalog
  scope: project
  rollback_on_regression: true
  outcome_rollback: true
  outcome_rollback_supervisor:
    version: 1
    enabled: true
    interval: 5m
    model_id: local-worker
    domain: creative
    profile: default
    source: user_feedback
    privacy: local_only
    min_samples: 20
    min_drop: 0.1
    tasks_per_version: 20
```

Enabling supervision requires both skill rollback switches, an absolute catalog
root, one scope, and a model ID present in the configured model catalog. The
interval is one second through 24 hours. `min_samples` and `tasks_per_version`
are each 20 through 100, with `min_samples <= tasks_per_version`; `min_drop` is
greater than zero and at most one.

Creative and unknown domains accept only `user_feedback`. Code, coding,
debugging, math, and structured-JSON domains require `deterministic` or
`tool_result`; feedback-only objective rollback is rejected. `llm_judge` is rejected. Privacy must be
`local_only` or `cloud_allowed`; selection still applies the configured model,
deployment, privacy, credential, attribution, and current-evidence checks.

## Scan and decision lifecycle

Each due iteration reserves at most one active skill in lexical name order in a
named durable supervisor record. The pending check binds the exact current
activation, immediate predecessor, policy digest, stable check ID, and stable
outcome operation ID before evidence is selected. The active version must be on its first
activation, and the predecessor must retain passed deterministic validation.
Initial versions, restored/reactivated versions, and malformed timelines are
ineligible and are skipped without creating outcome state.

Readiness selection observes a bounded telemetry snapshot. `waiting` is a
successful no-action result, including sparse or exclusion-heavy windows. It
completes the scheduler check and advances the durable cursor, but creates no
outcome intent, selected-evidence checkpoint, or rollback receipt. The stable
operation binding is scheduling identity only and grants no outcome authority.

When both cohorts meet `min_samples`, the step is `ready` and uses
`OutcomeRollbackPrepared`. All catalog, settings, credential, fixed-source, and
selection guards run before the store atomically persists the outcome intent and
the exact selected-evidence checkpoint in one catalog replacement. Final
adjudication then either records `no_action` or records `rolled_back` together
with restoration of the validated predecessor. A crash after atomic prepare can
resume from only the saved evidence; it never refreshes the window. The SQLite
source checks and catalog replacements are not one distributed transaction, so
the existing fixed-source rechecks and limitations still apply.

The operation ID is deterministic for the activation revision and complete
selection policy. A completed exact retry returns the historical receipt. A
prepared unfinished retry reuses the saved checkpoint. Changed evidence or
activation invalidates completion; it does not authorize replacement selection.
If rollback commits before scheduler acknowledgement, restart finds the exact
receipt through the pending check and settles it without rediscovering the
activation. Stale, sparse, mixed, or unknown evidence completes as no action;
only operational failure degrades supervisor health.

SQLite schema 47 stores a separate immutable lifecycle journal containing only
operation, check, skill, activation-revision, policy, fixed code, sequence, and
timestamp fields. Exact lost-ack retries reconcile to the original event.
Prompts, model output, tool arguments, secrets, and error text have no field in
this record shape.

## SDK ownership and daemon lifecycle

The SDK exposes read-only candidate and readiness inspection plus manual and
owned scheduling:

```go
candidate, err := client.OutcomeRollbackCandidate(ctx, key)
readiness, err := client.InspectOutcomeRollbackReadiness(ctx, key)
next, readiness, err := client.OutcomeSupervisionStep(ctx, after)
state, err := client.DurableOutcomeSupervisionStep(ctx)
state, err = client.OutcomeSupervisionState(ctx)
check, err := client.OutcomeSupervisionCheck(ctx, checkID)

monitor, err := client.StartOutcomeSupervision(ctx)
defer monitor.Close()

configured, err := client.StartConfiguredOutcomeSupervision(ctx)
defer configured.Close()
```

`OutcomeSupervisionStep` remains the manual read-oriented compatibility surface.
The owned monitor uses `DurableOutcomeSupervisionStep`: its catalog cursor,
pending check, due time, exact activation pair, and operation binding survive
restart. Catalog file locking serializes competing processes, and exact retries
return the same pending or completed check. Reaching the end resets the lexical
cursor and records the next due time; missed wall-clock ticks are not replayed.

`StartOutcomeSupervision` starts directly from the client's validated settings.
`StartConfiguredOutcomeSupervision` first freezes settings, performs bounded
read-only catalog and telemetry preflight, and returns a valid disabled handle
when policy is off. Both handles are caller-owned. `Close()` cancels and joins
the background goroutine; a callback is not force-killed outside cooperative
context cancellation.

The stock daemon prepares the configured policy before listener binding, starts
the outcome supervisor after durable service initialization and before the task
dispatcher, includes `outcome_supervision` in readiness health, and cancels and
joins it during shutdown. `unknown`/starting, degraded, stalled, or unavailable
health makes readiness fail; disabled and healthy states compose normally.
Health is operational metadata, not evidence that a skill regressed or rolled
back. A step error is reported generically and later intervals continue.

## Boundaries

There is no automatic model judgment, new HTTP mutation endpoint, CLI approval
surface, causal inference, or statistical correction for repeated looks.
Terminal scheduler checks rotate deterministically at the 1,000-record bound.
An explicit retention operation can replace old, settled `no_action` outcome
records with compact tombstones while retaining operation and activation-revision
ownership; pending work, rollback receipts, and the current revision are never
pruned. Use
[outcome comparison](skill-outcome-comparison.md) for evidence semantics and
[automatic window selection](skill-comparison-selection.md) for cohort rules.
