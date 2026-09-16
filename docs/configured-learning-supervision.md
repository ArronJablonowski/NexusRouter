# Configured learning and regression supervision

The daemon and Go SDK share an explicitly started lifecycle for the existing
learner and optional durable regression monitor. No validators are loaded from
configuration, model output, shell commands, URLs or dynamic plugins. A trusted
Go host must register a concrete validator implementation under a stable identity.
Changing validation semantics requires a new identity; the registry does not
authenticate code or infer its correctness.

The stock `darwin` binary ships one deliberately narrow validator under the
protected identity `darwin_observed_tools_activation_v1`. It is available only
when configuration explicitly selects that identity; the default remains
draft-only. Unknown identities and attempts by a host callback to claim the
protected identity fail before listener binding or database initialization. A
custom host can still supply other validators, and the SDK exposes the same
lifecycle without starting an HTTP server. This is provenance validation, not a
generic proof that generated workflows are safe, useful or correct.

## Publication provenance boundary

Production validation begins from the exact generated-version
publication binding exposed by `skills.PublicationStore`. The read-only lookup
checks the receipt, catalog metadata and immutable version body together and
returns the owning generation-attempt ID plus the canonical attempt digest.
Missing receipts on manual, legacy or unpublished versions fail closed, and the
lookup performs no repair, validation callback, activation or other write.

That binding is necessary but not sufficient validation evidence. It does not
read the telemetry database, authenticate source events, prove workflow quality
or treat model-authored validation cases as trusted. A host validator must still
reconstruct and verify the durable workflow evidence before it can make any
activation decision. The stock binary therefore remains draft-only unless the
protected validator is explicitly selected with every activation prerequisite.

The runtime contains the narrow `darwin_observed_tools_activation_v1` validator
needed for that reconstruction. It checks the complete stored version and receipt before and after
one coherent read-only SQLite snapshot, re-derives current accepted source records
and paired successful tool events, and rejects concurrent database revisions. It
also binds the attempt digest, content-addressed selection policy identity, exact
tool sequence, source sessions/evaluation digests, current local-only policy and
every declared required tool. It returns deterministic evidence only for this
provenance claim; it never interprets `validation_cases`, calls a provider/tool or
claims semantic workflow correctness.

A readable change to the selected evaluation set, judge-only evidence, privacy
violation, or unobserved required tool returns attributable deterministic failed
evidence and can drive rollback of a previously activated version. Malformed or
missing state, catalog/version drift, cancellation, storage failure, panic, and
concurrent revision remain validation errors and never authorize rollback.

The daemon and SDK construct this product-owned binding lazily from immutable
service settings before configured-learning preflight. Construction opens no
catalog or database, and validation opens the catalog and telemetry store read
only. A selection's saved policy identity is content-addressed, but the validator
does not independently reproduce the entire historical configuration or generation
input digest, authenticate direct database tampering, or attest tool arguments and
implementation versions.

## Explicit selection

These optional fields supplement an already valid learning/model/budget setup:

```yaml
skills:
  auto_activate_after_validation: true
  rollback_on_regression: true
  learning:
    validator_id: darwin_observed_tools_activation_v1
    regression_name: project-regression
    regression_interval: 5m
```

`validator_id` enables validated learning only when the named implementation is
available. The value above selects the protected stock provenance validator;
custom Go hosts may select a separately registered identity. An empty identity
preserves draft-only behavior. The regression name
and interval must both be set or both omitted; they require a validator identity,
and intervals range from one second to 24 hours. Enabled validated learning
requires activation policy, and enabled regression requires rollback policy.
Disabling `skills.learning.enabled` stops both configured controllers on restart,
without needing to resolve a retained validator selection.

Empty fields are omitted from policy JSON, preserving existing unconfigured
learning policy digests. Opting in changes the policy: existing named learner or
monitor bindings are not reset or silently rebound. Inspect their durable state
and make an explicit operator policy/identity decision before restarting.

## Trusted Go host

Supply a validator that performs actual deterministic checks appropriate for the
workflow, not an always-pass callback, structural check alone, or LLM self-review.
Given an application-owned `projectValidator` and configured SDK client:

```go
registry, err := sdk.NewSkillValidatorRegistry(map[string]skills.Validator{
    "project-tests-v1": projectValidator,
})
// Handle err before starting anything.
supervisor, err := client.StartConfiguredLearning(ctx, registry)
// Handle err; after a successful start, retain and close the supervisor.
defer supervisor.Close()
checks := supervisor.Health()
```

For the stock validator, configure its protected identity and pass `nil` to
`StartConfiguredLearning`, or inspect the immutable merged registry first:

```go
registry, err := client.ConfiguredSkillValidatorRegistry(nil)
supervisor, err := client.StartConfiguredLearning(ctx, registry)
```

Direct SDK callback methods reject a caller-supplied validator that claims the
protected stock identity. Use a distinct versioned identity for host policy.

The registry copies at most 64 entries and rejects invalid identities and nil,
including typed-nil, validators. It retains callback state by reference: hosts
must make it concurrency-safe, read-only, repeat-safe and cancellation-cooperative.
Learning and regression may validate concurrently. The registry never invokes a
callback during lookup. Existing direct SDK validation/monitoring methods remain
available; do not accidentally start duplicate controllers.

## Startup and shutdown boundaries

Configuration, registry identity, privacy and policy are checked before daemon
listener/database initialization. Before either controller starts, read-only
storage preflight checks any existing learner and named monitor policy bindings
under a separate ten-second deadline; that deadline does not limit controller lifetime.
The task database must already exist for SDK startup; normal daemon initialization
owns migration. Configured regression additionally requires an existing compatible
catalog. It never creates a catalog merely to hide a missing-store error.

Missing bindings may begin fresh; conflicting or corrupt bindings fail without
starting either controller. These reads are preflight, not an atomic transaction
across SQLite and the skill catalog. Each tick's durable intent, operation receipt,
policy and activation-revision checks remain authoritative if state changes later.
Custom skill stores are not supported by this built-in learning pipeline.

The daemon starts the learning bundle before the task dispatcher so an existing
learning-policy conflict cannot be discovered only after queued tasks dispatch.
Startup failure cleans up any controller already owned. One shared cancellation
tree cancels both learning and regression before either is joined. Concurrent and
repeated `Close` calls join the same lifecycle. Cooperative callbacks must return;
this is not a sandbox or forced termination of arbitrary Go code.

Health includes `learning` and, when configured, `skill_regression`. Both must be
healthy or disabled for daemon readiness. Health is metadata, not proof that a
particular skill passed. Durable check receipts and activation/rollback history
provide that evidence. Shutdown cannot undo model inference already submitted.

The separate disabled-by-default outcome rollback supervisor has its own
configuration, SDK handle, health component, and shutdown join. It does not use
the validator registry or the durable regression-monitor cursor. See
[configured outcome supervision](configured-outcome-supervision.md).

## Remaining qualification

New model-generated drafts retain their verified source domain as a discovery
tag even when the model omits it, without duplicating an existing matching tag.
The complete tag set must fit the runtime metadata bound. This does not activate
a draft, expand permissions, or rewrite existing generated records and receipts.
Qualification combines stock daemon admission tests with a real observed-tools
lifecycle fixture: two accepted tool-using sessions drive generation and immutable
publication, the protected validator activates the candidate, later runtime context
loads it progressively, restart does not regenerate, and a revised source evaluation
produces deterministic failed evidence and restores the validated baseline. A
second restart preserves rollback and immutable history. Context retrieval does
not prove semantic execution of arbitrary generated workflows.

Outcome-based rollback uses its own disabled-by-default
[configured supervisor](configured-outcome-supervision.md). Causal/confounder
controls, repeated-look correction, safe long-term receipt retention and broad
live-model qualification remain open. No user configuration is enabled by
installing this change.
