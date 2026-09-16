# Durable named skill regression monitors

Trusted Go hosts can explicitly call the versioned SDK's
`StartDurableSkillRegression(ctx, name, validatorID, interval, validator)` or
drive one bounded `DurableSkillRegressionStep` at a time. Construction alone
starts nothing. The caller owns the returned monitor, its health, and `Close()`.
The legacy `StartSkillRegression` remains available with its in-memory cursor.

## Durable contract

The skill catalog records a named monitor's scope, validator identity, policy
digest, interval, revision, cursor, next due time and pending operation. Before a
callback runs it stores the exact expected activation and a unique operation ID.
A reopened host resumes that intent without refreshing its expected revision.
Validator, policy and interval bindings are immutable for an existing name.

The first check is immediately due; completed checks defer the next check by the
configured interval (one second through 24 hours). Selection is lexical, one
active skill at a time. An empty page resets the cursor and waits another
interval. Restart preserves cadence and cursor. Wall-clock jumps can delay or
accelerate due checks; this is not a monotonic distributed scheduler.
The supervisor polls at the configured interval. Because next-due time starts at
completion, a poll may arrive too early and actual checks may be roughly two
intervals apart; the interval is minimum spacing, not an exact frequency promise.

Passing validation produces a regression receipt without changing activation.
Deterministic failure evidence can atomically commit a receipt and rollback to
a validated predecessor. Completion then reconciles that receipt with the monitor
cursor. Interruption between those writes leaves a resumable pending operation;
it does not authorize repeating rollback.

A definite unsuccessful check records `failed` with a fixed `check_failed` or
`stale_activation` code and advances fairly. This is not successful validation
or proof that a skill itself regressed. Callback text is not retained. The SDK
step returns a generic error with safe advanced state; supervisor health remains
degraded. Cancellation, unknown storage failures and unsafe metadata retain pending
work rather than guessing completion.

Retained failure records fence late concurrent callbacks inside the same catalog
transaction that could commit a regression receipt. If the receipt wins first,
reconciliation recognizes completion; if the failure wins first, the late result
cannot commit rollback. Registered monitor IDs cannot be used to bypass that
fence through the standalone mutation API. Concurrent callbacks may still run:
callback execution is not exactly once.

## Authority, privacy and inspection

`skills.enabled` and `skills.rollback_on_regression` must be enabled. Provide
trusted, read-only, repeat-safe, concurrency-safe and cancellation-cooperative
validation code. The existing three-second cooperative validator bound and
ten-second application step bound apply. A callback ignoring cancellation can
stall shutdown; this is not process isolation or a sandbox.

Current policy and cumulative credential-collision checks surround admission,
callbacks, persistence and return values. Core metadata guards run under the
catalog lock: they must be bounded and must not reenter the store. Host secret
resolvers used by these guards have the same non-reentrancy requirement. Secret
rotation cannot retroactively erase previously valid historical records.

`SkillRegressionMonitorState(ctx, name)` and
`SkillRegressionMonitorCheck(ctx, name, operationID)` expose validated metadata
through read-only inspection, including with rollback disabled. Inspection creates
or migrates nothing and does not prove a supervisor is currently alive. Retain a
pending operation ID to inspect its eventual outcome. Existing regression receipt
inspection provides the validation evidence.

## Storage and scope

Catalog schema 5 adds monitor and check maps, each capped at 1,000 records, with
the existing 1,000 regression-receipt and 8 MiB catalog bounds. New work fails
closed at capacity; records are not silently evicted. Retention and compaction
need a future safe policy before sustained monitoring at scale. Schemas 1–4 remain
readable; older binaries cannot read schema 5, so do not downgrade without a
compatible migration. SQLite schema 26 is unchanged.

Tests cover reopened pending work, cadence, fairness, both concurrent commit
orderings, cancellation, guard denial, capacity, corruption, schema preservation,
rotating secrets, SDK inspection and joined lifecycle. These are synthetic
deterministic checks, not semantic skill-quality or physical power-loss evidence.
No user monitor, generated validation command or new inference is enabled by
default. The configured daemon/SDK lifecycle can bind the monitor to a registered
trusted validator, and the stock binary offers only its protected observed-tools
provenance validator. Outcome-based rollback uses a separate configured
supervisor; causal/confounder controls and repeated-look correction, broader
semantic validators, safe retention and cross-store power-loss qualification
remain open.
