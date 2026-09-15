# Configured outcome rollback supervision

DarwinRouter can supervise activation-bound outcome comparisons in the daemon or
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
debugging, math, and structured-JSON domains accept `deterministic`,
`tool_result`, or `user_feedback`. `llm_judge` is rejected. Privacy must be
`local_only` or `cloud_allowed`; selection still applies the configured model,
deployment, privacy, credential, attribution, and current-evidence checks.

## Scan and decision lifecycle

Each iteration reads at most one active skill in lexical name order. Candidate
inspection derives the exact current activation and its immediate predecessor
from one read-only catalog snapshot. The active version must be on its first
activation, and the predecessor must retain passed deterministic validation.
Initial versions, restored/reactivated versions, and malformed timelines are
ineligible and are skipped without creating outcome state.

Readiness selection observes a fresh bounded telemetry snapshot. `waiting` is a
successful read-only result, including sparse or exclusion-heavy windows. It has
a policy-and-activation-derived operation ID, but creates no durable intent,
selection checkpoint, monitor record, or receipt. There is deliberately no
claimed outcome operation while waiting.

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

## SDK ownership and daemon lifecycle

The SDK exposes read-only candidate and readiness inspection plus manual and
owned scheduling:

```go
candidate, err := client.OutcomeRollbackCandidate(ctx, key)
readiness, err := client.InspectOutcomeRollbackReadiness(ctx, key)
next, readiness, err := client.OutcomeSupervisionStep(ctx, after)

monitor, err := client.StartOutcomeSupervision(ctx)
defer monitor.Close()

configured, err := client.StartConfiguredOutcomeSupervision(ctx)
defer configured.Close()
```

`OutcomeSupervisionStep` returns its next lexical cursor even when a later check
fails. The monitor performs one immediate step and then one step per configured
interval. Its cursor exists only in process memory. Reaching the end resets it;
a process restart starts a new scan. There is no durable cursor, named monitor
record, missed-tick replay, lease, or cross-process singleton election. Run only
one supervisor for a catalog scope unless duplicate observation is acceptable;
the catalog's operation/revision fences remain the mutation authority.

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
surface, durable scheduling record, causal inference, statistical correction for
repeated looks, reactivation attribution, or long-term outcome-record retention
policy. The 1,000-record catalog bounds and schema-8 compatibility rules from
[outcome-policy rollback](skill-outcome-rollback.md) still apply. Use
[outcome comparison](skill-outcome-comparison.md) for evidence semantics and
[automatic window selection](skill-comparison-selection.md) for cohort rules.
