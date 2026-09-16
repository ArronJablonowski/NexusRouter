# Retry-safe skill activation operations

Trusted Go hosts can bind one logical activation to a durable operation ID:

1. Inspect `Client.SkillActivationState(ctx, key)` and retain the exact result.
2. Choose a stable operation ID for this decision and retain the candidate version.
3. Call `Client.ActivateSkillVersionOnce(ctx, operationID, expected, versionID, validator)`.
4. After a lost response or restart, retry with the **same** ID, expected state and
   candidate. Do not replace the saved precondition with a newly read revision.

The first successful call runs deterministic validation, checks the full current
activation revision, and commits activation plus its receipt in one catalog
replacement. A matching committed receipt acknowledges the historical operation
without invoking the validator or changing the catalog. This remains true after
a later rollback or activation: retry is not a request to make the old candidate
active again. An already-active candidate without this operation's receipt is
not treated as a newly completed operation.

Operation IDs are catalog-global, 1–64 ASCII letters/digits/underscores/hyphens,
starting with a letter or digit. Reusing an ID for another key, candidate or
precondition conflicts. Keep IDs free of secrets and user content.

`Client.SkillActivationOperation(ctx, key, operationID)` inspects the receipt
without creating storage or changing state. A receipt binds the exact expected
revision, candidate, timestamp and passed deterministic evidence. Its historical
precondition is checked against the stored timeline prefix. It is not current
state, approval for another operation or cryptographic tamper evidence. Read
`SkillActivationState` separately when current activation matters.

## Validation and policy

The configured scope and `skills.auto_activate_after_validation` must permit the
operation. Disabled policy and nil validators still reject mutation calls,
including retries; receipt inspection does not enable mutation. Injected retrieval
stores are not implicitly writable catalogs.

Validators remain trusted host Go code. They must actually check the candidate,
return attributable deterministic evidence, obey privacy/tool policy, be
read-only and safe to repeat, and cooperate with cancellation. Schema validity,
model self-assessment or user-supplied `passed: true` is not validation authority.
Concurrent calls may both run validators, but only one matching transition
commits. A concurrent caller can acknowledge a matching receipt already committed
by another caller; that acknowledgement does not certify its own callback result.
Callbacks are joined, not abandoned. The core deadline is a cooperative three
seconds, inside the application's ten-second bound.

Failed validation, panic, cancellation, stale revisions or credential collisions
do not create a new receipt. Application guards check operation IDs, candidate
content and evidence against observed credentials, including rotation during
validation. No CLI/HTTP endpoint accepts untrusted validation proof.

## Storage and remaining work

The first operation-keyed activation upgrades the **file skill catalog** to
schema 3. This is independent of SQLite's schema. Stop older catalog writers and
back up the private skill directory before use. Schemas 1 and 2 remain readable;
legacy activation revisions are unchanged until a transition occurs. Publication
never downgrades an upgraded catalog. No automatic deletion or downgrade exists.

Operation receipts are part of the existing bounded activation timeline, not a
second mutable acknowledgement store. Receipt and active state share the same
atomic replacement and durability boundary. Tests cover restart/retry, rollback,
concurrent callers, ABA changes, corruption, policy and secret rotation. Power-loss
qualification remains separate.

The opt-in Go-host learning controller now uses these receipts with a durable
activation intent and a named trusted validator; see
[background learning](background-learning.md). The configured daemon/SDK
lifecycle can select a registered trusted validator and own durable automatic
regression monitoring; the stock binary exposes only the protected
`darwin_observed_tools_activation_v1` provenance validator. General semantic
domain validators, causal/confounder controls, repeated-look correction, safe
long-term receipt retention and cross-store power-loss qualification remain
open. Outcome-based rollback has a separate disabled-by-default configured
supervisor; see [configured outcome supervision](configured-outcome-supervision.md).
