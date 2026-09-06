# Opt-in outcome-policy rollback

Trusted Go hosts can apply one outcome-policy decision to an inspected skill
activation. This is a separate authority from deterministic validation: the
recorded comparison remains advisory, and the operator explicitly permits a
rollback based on its observational regression signal.

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
```

`outcome_rollback` defaults to false. Drafting and new activation can remain
disabled. The new false value is omitted from JSON policy fingerprints, preserving
existing default policy bindings; explicit true participates in the fingerprint.
YAML retains the false field so layered environment/flag overrides can resolve it.

The versioned SDK exposes:

```go
receipt, err := client.OutcomeRollbackOnce(ctx, operationID, expected, selectionRequest)
historical, err := client.OutcomeRollbackOperation(ctx, expected.Key, operationID)
```

`expected` is an inspected `skills.ActivationState`; `selectionRequest` is the
same `SkillComparisonSelectionRequest` used for read-only window selection.
Retain the operation ID, expected activation and request before calling. This
interface is trusted-host-only: there is no new CLI/HTTP mutation endpoint and
no automatically started daemon loop. Constructing a client starts no work.
The application builds the report through its actual SQLite selector; callers
cannot submit an invented report through the SDK.

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

Either decision consumes this activation revision's adjudication. Another
operation ID or changed policy cannot create a second committed decision for it.
Do not call prematurely if the host intends to wait for sufficient samples.
Policy/validation/callback errors commit no decision. Concurrent or interrupted
callbacks can run more than once before a decision commits: this is **not** a
strict cap on statistical looks or exactly-once callback execution. Durable
preselection intents and repeated-monitoring policy remain future work.

An exact retry returns the saved historical receipt without reading SQLite or
selecting newer outcomes, even after later activations or loss of the evidence
database. Current scope, model mapping, policy and credential checks still apply.
Mutation retries require both rollback flags; read-only receipt inspection remains
available with rollback disabled and scoped skills still configured.

## Atomicity, evidence and privacy

The catalog replacement commits the receipt and optional rollback together. The
receipt retains operation ID, expected state, selection policy/report, after state,
decision time and activation-history position. A distinct `outcome_operation_id`
on the rollback transition links it to that receipt. It cannot also carry
deterministic regression or activation evidence. Existing validation proofs are
not rewritten, and no `Evidence.Deterministic` success/failure is fabricated.

SQLite selection and the later catalog write are not one distributed transaction.
The report records a coherent historical snapshot; feedback can change afterward.
`CheckedAt` is the catalog decision time, not a claim that feedback is still
current then. The catalog activation is checked again under its write lock before
commit; stale expected state is never refreshed automatically.

Complete selected observations undergo the existing scope/secret checks. The
action additionally checks candidate/predecessor content, policy and receipt
metadata, cumulative credential changes, and per-call catalog/database path
bindings. Metadata guards run under the catalog lock and must be bounded,
non-reentrant and cancellation-cooperative. Selector and guard values are copied
to prevent retained mutable aliases. The core has an eight-second cooperative
deadline; the application has ten seconds and SQLite selection five seconds.

An error after commit can suppress an already-persisted response. Inspect or retry
the same binding; do not assume no action occurred or invent another operation ID.
The receipt's `After` state is historical, not necessarily the current activation.

## Catalog compatibility and qualification

The first committed outcome decision promotes the file catalog to schema6.
Schemas1–5 remain readable; subsequent activation, publication and deterministic
monitor writes do not downgrade it. Stop older writers before adopting schema6.
This operation does not migrate SQLite, whose selector requires schema27.
Outcome receipts are capped at1,000 and share the catalog's existing8 MiB limit;
capacity failure does not silently delete audit records.

Ordinary catalog reads validate structural receipt/transition bindings without
rehashing every historical prefix. Requested receipt reads and new commits fully
verify the exact before/after history and version binding. These are local-storage
consistency checks, not cryptographic attestation of an experiment.

Tests cover actual catalog/SQLite feedback-driven rollback, no-action decisions,
concurrent revision fencing, policy/secret/path changes, receipt corruption,
independent kill switches, reactivation rejection and deterministic-evidence
separation. An owned subprocess is killed after commit but before wrapper
acknowledgement; retry invokes no selector and adds no rollback. This does not
qualify interruption during rename/fsync, physical power loss, production domain
validators, or a complete unattended learning lifecycle.
