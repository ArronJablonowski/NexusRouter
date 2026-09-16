# Opt-in outcome-policy rollback

Trusted Go hosts and the configured daemon supervisor can apply one
outcome-policy decision to an inspected skill activation. This is a separate
authority from deterministic validation: the recorded comparison remains
observational and advisory, and configuration explicitly permits rollback from
that signal. An LLM judge is never supervisor authority.

This policy can roll back a good version because workloads, time and task
difficulty can confound outcomes. It is not causal proof or a statistically
guaranteed repeated-monitoring procedure. Read the [selection limitations](skill-comparison-selection.md)
before enabling it. No user configuration is changed by installing this code.

## Configuration and SDK

Enable only for a scoped catalog whose predecessor has already passed trusted
activation validation:

```yaml
skills:
  enabled: true
  root: /absolute/private/catalog
  scope: project
  rollback_on_regression: true
  outcome_rollback: true
  outcome_rollback_supervisor:
    version: 1
    enabled: false
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

`outcome_rollback` defaults to false. Drafting and new activation can remain
disabled. The new false value is omitted from JSON policy fingerprints, preserving
existing default policy bindings; explicit true participates in the fingerprint.
YAML retains the false field so layered environment/flag overrides can resolve it.
The supervisor is independently disabled by default. Enabling it also requires
the catalog root and scope, both rollback switches, and an exact configured
model. Intervals range from one second to 24 hours; sample and per-version task
limits range from 20 to 100. Creative and unknown domains require
`user_feedback`. Coding, debugging, math and structured-JSON domains may use
only `deterministic` or `tool_result`; feedback-only objective rollback is
rejected. `llm_judge` is rejected for every domain. Privacy is fixed to
`local_only` or `cloud_allowed` and remains
subject to the normal model/evidence admission checks.

The versioned SDK exposes:

```go
receipt, err := client.OutcomeRollbackOnce(ctx, operationID, expected, selectionRequest)
historical, err := client.OutcomeRollbackOperation(ctx, expected.Key, operationID)
intent, err := client.OutcomeRollbackIntent(ctx, expected.Key, operationID)
selected, err := client.OutcomeSelectionCheckpoint(ctx, expected.Key, operationID)

candidate, err := client.OutcomeRollbackCandidate(ctx, expected.Key)
readiness, err := client.InspectOutcomeRollbackReadiness(ctx, expected.Key)
next, readiness, err := client.OutcomeSupervisionStep(ctx, after)
monitor, err := client.StartOutcomeSupervision(ctx)
configured, err := client.StartConfiguredOutcomeSupervision(ctx)
```

`expected` is an inspected `skills.ActivationState`; `selectionRequest` is the
same `SkillComparisonSelectionRequest` used for read-only window selection.
Retain the operation ID, expected activation and request before calling. There
is no new CLI/HTTP mutation endpoint. Constructing a client starts no work.
`StartOutcomeSupervision` and `StartConfiguredOutcomeSupervision` return owned
handles; callers must call `Close()` to cancel and join them. The stock daemon
starts the configured handle when enabled and joins it during shutdown. See
[configured outcome supervision](configured-outcome-supervision.md).
The application builds the report through its actual SQLite selector; callers
cannot submit an invented report through the SDK.

Direct `skills.FileStore` hosts that attach a configured model ID must now use
`OutcomeRollbackOnceGuarded` and supply it before selection, together with the
intent permission guard. Fresh calls through the simpler core
`OutcomeRollbackOnce` require an empty configured model ID; it cannot discover
that binding after dispatch. Exact completed retries retain their stored model
binding, including older nonempty-ID receipts. Those direct-core entry points
retain their non-checkpointing behavior for fresh execution. To persist selected
evidence, use `OutcomeRollbackOnceCheckpointed` with an explicit
`OutcomeSelectionGuard` in addition to the intent and receipt guards. The
application and SDK use this checkpointed entry point automatically. Reports
are never newly persisted under an implicit no-op selection permission guard.
An unfinished checkpoint must also be resumed through the checkpointed entry
point; older entry points cannot silently skip its selection guard. They can
still acknowledge an already-completed receipt through the receipt guard.

## Decision and retry rules

The candidate must be the exact active version at the expected activation
revision. The baseline must be the actual undo predecessor, not an arbitrary
older version or a caller-selected parent. It must retain its passed deterministic
activation proof. Both version files are checked, and nonempty comparison cohort
digests must match their immutable catalog metadata.

Only the candidate's first activation is supported. Reactivated or restored
versions are rejected because recorded references identify versions/digests, not
individual activation revisions. Historical version-level evidence must not be
presented as current-activation evidence.

The host observes an outcome-independent window and then commits one decision:

- `rolled_back`: a valid `regression_signal` restores the validated predecessor.
- `no_action`: all other valid reports, including empty windows or insufficient
  evidence, write a receipt without changing the activation revision.

Before reading outcomes, the host now commits an immutable preselection intent
that binds the operation ID, configured model ID, policy, exact activation and
activation-history position. A guarded permission check runs before that write.
The write consumes this activation revision's single attempt: another ID or
changed binding cannot select evidence, even if the original attempt failed.
Only the call that successfully created the intent proceeds to selection.

Do not call the manual `OutcomeRollbackOnce` path prematurely if the host intends
to wait for sufficient samples.
Selection failure, cancellation, panic, process death or a later guard denial
leaves an inspectable intent without a decision receipt. It does not become
`no_action`, positive validation, or permission to select again. An exact retry
with only an intent returns an error and invokes no selector. This favors an
explicit unresolved result over silently rereading changed feedback. There is no
automatic clearing, lease takeover, force retry or operator-reset endpoint.

Either completed decision also consumes the revision's adjudication. The new
path provides at most one selector invocation per durable intent under supported
catalog locking, not exactly-once completion. A crash after claim persistence
can mean zero invocations. Older receipts created before intents remain readable
and retryable, but do not gain a retroactive single-attempt guarantee. These
guarantees do not establish a general repeated-monitoring policy.

The checkpointed path saves a validated selection report before the final
decision. A checkpoint contains the exact intent, aggregate report and selection
save time; it is **not a completion receipt**. If that save succeeds but final
commit is interrupted or denied, retrying the same binding uses the saved report
without invoking the selector. The application/SDK reopens SQLite read-only and
checks only the report's saved task IDs in one current snapshot, before saving
the checkpoint and again before final completion. Original attribution, privacy,
ordinal bounds, journal outcomes, current evaluation revisions and global
same-session correlation must still reproduce the saved comparison and evidence
digest. Corrected feedback, changed task results, missing evidence, or new
same-session correlation prevents completion. The checkpoint and claim remain
inspectable; no replacement window, fabricated result or automatic reset follows.
Unrelated new tasks do not expand the saved membership. Known empty sources stay
empty, rather than triggering a fresh selection.

Current policy, credentials, candidate/predecessor content and the exact
activation revision are also checked. A stale activation prevents completion;
its expected state is never refreshed to make the old report fit. Unfinished
legacy checkpoints without recorded source IDs remain inspectable but cannot
complete through the application/SDK. Direct core hosts remain responsible for
their explicit selection guard; the core itself has no SQLite dependency.

This provides fixed-source invalidation, not a refreshed latest-window report or
checkpoint age/expiry policy. The configured supervisor performs repeated
monitoring, but workload-drift controls and statistical correction for repeated
looks remain open.

An intent with no checkpoint and no receipt remains unresolved and cannot
reselect. This includes old schema7 attempts and death before checkpoint save.
Selection and receipt guards may run again during recovery and must be trusted,
read-only and retry-safe. Recovery for that legacy/manual shape is an explicit
host retry.

The supervisor avoids that premature-claim shape. Candidate and readiness
inspection open the catalog and telemetry database read-only. `waiting` is a
successful observation and creates no intent, selection checkpoint or receipt.
Once both cohorts satisfy the configured minimum, the supervisor passes the
already-selected report to `OutcomeRollbackPrepared`. That path validates all
guards before atomically storing the intent and exact selection checkpoint in
one catalog replacement, then attempts final adjudication. A crash after prepare
can resume only from that fixed report; it cannot select newer evidence.

An exact retry returns the saved historical receipt without reading SQLite or
selecting newer outcomes, even after later activations or loss of the evidence
database. Current scope, model mapping, policy and credential checks still apply.
Mutation retries require both rollback flags; read-only receipt inspection remains
available with rollback disabled and scoped skills still configured.

## Atomicity, evidence and privacy

The manual checkpointed path may write the intent and selection checkpoint as
separate earlier catalog replacements. The prepared supervisor path writes both
records atomically in one replacement after the evidence is decision-ready. In
both paths, the final catalog replacement commits the receipt and optional
rollback together. The
receipt retains operation ID, expected state, selection policy/report, after state,
decision time and activation-history position. A distinct `outcome_operation_id`
on the rollback transition links it to that receipt. It cannot also carry
deterministic regression or activation evidence. Existing validation proofs are
not rewritten, and no `Evidence.Deterministic` success/failure is fabricated.
New checkpoint-backed receipts link the saved selection with `selection_id`;
legacy receipts are not given invented checkpoint provenance.

SQLite selection/source checks and the later catalog write are not one distributed
transaction. Each check records a coherent snapshot; a writer can commit changed
feedback after the last check and before the catalog replacement.
`SelectedAt` is the checkpoint save time, and `CheckedAt` is the catalog decision
time, not a claim that feedback is still
current then. The catalog activation is checked again under its write lock before
commit; stale expected state is never refreshed automatically.

Complete selected observations undergo the existing scope/secret checks. The
action additionally checks candidate/predecessor content, policy and receipt
metadata, cumulative credential changes, and per-call catalog/database path
bindings. Intent, selection-save and final metadata guards run under the catalog lock and must be bounded,
non-reentrant and cancellation-cooperative. Selector and guard values are copied
to prevent retained mutable aliases. The core has an eight-second cooperative
deadline; the application has ten seconds and each SQLite selection/source check
has five seconds, bounded by the enclosing deadline.

An error after commit can suppress an already-persisted response. Inspect or retry
the same binding; do not assume no action occurred or invent another operation ID.
The receipt's `After` state is historical, not necessarily the current activation.

## Catalog compatibility and qualification

The first preselection intent promotes the file catalog to schema7; saving a
selection checkpoint promotes it to schema8. Existing schema6/7 receipts remain
readable without invented historical intent or checkpoint records.
Schemas1–7 remain readable; subsequent activation, publication and deterministic
monitor writes do not downgrade it. Stop older writers before adopting schema8.
This operation does not migrate SQLite, whose selector accepts schema27 or28.
Source membership is an additive optional report field; catalog schema8 and
the source-record format are unchanged. SQLite schema28 adds only an unrelated
query index; schema27 remains readable. Completed historical receipts, including those
without sources, remain acknowledgeable without reopening the evidence database.
Outcome records reserve at most 1,000 distinct activation revision slots across
intents and legacy receipts, with at most one selection checkpoint per intent,
and share the catalog's existing 8 MiB limit;
capacity failure does not silently delete audit records.

Ordinary catalog reads validate structural receipt/transition bindings without
rehashing every historical prefix. Requested receipt and intent reads fully
verify their historical binding. Admission of a new claim validates all existing
outcome intent/receipt prefixes before trusting the revision fences, so a damaged
revision or moved key cannot silently free an attempt slot. This heavier check
runs for new claims, not unrelated ordinary reads, with cancellation checks
between records. New commits also verify the exact before/after history and
version binding. These are local-storage
consistency checks, not cryptographic attestation of an experiment.

Tests cover actual catalog/SQLite feedback-driven rollback, no-action decisions,
concurrent revision fencing, policy/secret/path changes, receipt corruption,
independent kill switches, reactivation rejection, deterministic-evidence
separation, catalog candidate inspection, read-only waiting, atomic prepared
evidence, SDK-owned lifecycle, and daemon startup/health/shutdown composition.
An owned subprocess is killed after commit but before wrapper
acknowledgement; retry invokes no selector and adds no rollback. This does not
qualify interruption during rename/fsync, physical power loss, production domain
validators, causal outcome attribution, or repeated-look correction. Durable
supervisor scheduling is qualified separately through restart, readiness and
shutdown fixtures described in
[configured outcome supervision](configured-outcome-supervision.md).
