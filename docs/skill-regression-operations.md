# Durable skill-regression operations

Trusted Go hosts can bind an explicit regression check to a stable operation ID:

```go
receipt, err := client.RevalidateSkillVersionOnce(
    ctx, operationID, "workflow-validator-v1", expected, validator,
)
```

`expected` is a previously inspected `skills.ActivationState`. The host must
retain the operation ID, validator ID and expected state before dispatch, then
reuse all three after an uncertain response. A fresh ID means a fresh check, not
a retry. Changing the validator's implementation or validation contract requires
a new validator identity. Names alone do not authenticate code or prove quality.

This API requires the existing enabled scoped file store, rollback policy and
trusted `skills.Validator`. Callbacks must perform meaningful deterministic
checks, be read-only, safe to repeat, concurrency-safe and cancellation-cooperative.
No default success validator, model judgment, generated-command execution or
new daemon authority is introduced. New activation can remain disabled while
rollback protection is enabled.

## Commit and retry rules

A passing check writes a separate immutable receipt without changing the skill's
activation revision or history. A failing deterministic check commits its receipt
and rollback to the validated predecessor in one catalog replacement. An initial
version has no predecessor; this case requires attention and produces no successful
operation receipt. Callback errors, panic, cancellation and nondeterministic
evidence do not authorize rollback or manufacture a completed check.

An exact retry returns the saved historical receipt before rechecking current
activation. It invokes no validator and does not roll back again, including after
an unrelated later activation or rollback. Reusing an operation with a different
validator identity, skill or expected revision conflicts. Policy and credential
checks still apply to retries; disabling rollback does not turn receipt replay
into mutation authority. Read-only receipt inspection remains available with
rollback disabled.

Concurrent callers may both validate outside the lock. Only one bound result
commits; an exact competing caller may acknowledge that result. This is durable
operation recognition, not exactly-once callback execution. The existing
cooperative three-second core deadline and ten-second application deadline apply.
An application preflight racing a matching commit can return a generic conflict;
retrying the same binding then recognizes the receipt. A successful response from
every concurrent caller is not guaranteed.
An error after a storage write or late credential check may suppress a committed
response: inspect or retry the same operation instead of assuming no mutation.

## Receipt and inspection

`Client.SkillRegressionOperation(ctx, key, operationID)` reads the existing
receipt without creating, migrating or repairing a catalog. The SDK aliases its
type as `SkillRegressionOperation`; the public `skills.FileStore` also exposes
`RevalidateAndRollbackOnce` and `RegressionOperation` through the optional
`RegressionOperationStore` extension.

Version-one receipts contain operation and validator identities, `Expected`,
the checked `Result`, `After`, `ActivationCount` and UTC `CheckedAt`. The count
is the activation-history length **before** the check, including when rollback
adds a transition. Retrieval verifies the historical prefix and, for a rollback,
the exact following transition, timestamp and failure evidence. `After` is
historical, not necessarily current. Inspect current activation before deciding
on another operation. Records contain private skill/evidence identifiers; protect
exports. Known configured credentials cause rejection, not identity rewriting.

## Storage compatibility and limits

The first committed regression receipt promotes the file catalog to schema 4;
read-only operations never promote it. Schema 1–3 catalogs remain readable.
Existing publication and activation receipts survive, and later activation writes
must not downgrade the schema. Older binaries that do not understand schema 4
must not write this catalog. Back up and stop old writers before adopting it.
This does not change the SQLite schema.

The separate receipt collection holds at most 1,000 operations across the catalog
and shares the existing 8 MiB file bound. IDs are global within regression checks,
in a namespace separate from activation-operation IDs. Receipts are not silently
evicted or reused; retention/export policy remains future work. Full receipt
binding is checked on retrieval rather than repeatedly hashing every historical
prefix during all unrelated reads. These are consistency checks on trusted local
storage, not cryptographic attestation of executed validation.

The existing periodic monitor still uses its in-memory cursor and legacy check
method. Persisted scheduling identities/fairness, standalone daemon validator
configuration, statistical outcome regression detection and crash/power-loss
qualification remain open. No user learner or monitor is enabled by this change.

An owned-subprocess test qualifies the narrower boundary after the catalog commit
returns but before its wrapper delivers the result: SIGKILL, reopen, exact receipt
recovery and retry produce no additional rollback or callback. This is not a kill
during rename/directory sync, a power-loss test or proof that arbitrary validators
are safe to repeat.
