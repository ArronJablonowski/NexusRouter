# Background procedural learning

The daemon can discover repeated successful tool workflows, generate an
untrusted procedural draft, and publish it as an **inactive** skill version.
The ordinary daemon path does not manufacture validation evidence or activate a
skill automatically. An explicitly configured Go host can additionally supply
a trusted validator through the opt-in path described below.
The default remains off; upgrading DarwinRouter does not enable model calls.

## Enable deliberately

Configure an existing model and a private, canonical skill-root path. This
partial example supplements the provider/model catalog in `config.yaml`:

```yaml
skills:
  enabled: true
  auto_draft: true
  root: /absolute/private/skills
  scope: my-project
  local_only: true
  generation_budget:
    enabled: true
    window: 24h
    max_cost: 1
    max_attempts: 5
    max_in_flight: 1
    cooldown: 1h
  learning:
    enabled: true
    name: workflows
    domain: code
    model_id: local-generator
    interval: 1m
    max_cost: 0.1
    scan_limit: 20
```

`model_id` refers to a configured Ollama or OpenAI-compatible HTTP model with
context and cost metadata, plus a RAM estimate for local execution. Native
Codex coordinator authentication remains supported for tasks and audits, but
native Codex skill generation is not implemented; enabling that combination
is rejected in configuration. Local-only policy applies to generation transport
and source material. Cost numbers must use the same units as model estimates;
they are reservations, not measured provider invoices.

Start or restart `darwin serve --config config.yaml` or the managed daemon to
load these settings. Normal foreground task runs and SDK construction do not
start a background loop. To stop new learning, set `skills.learning.enabled:
false` and restart the daemon. Shutdown cancels and joins its current operation;
it cannot undo inference already sent or a draft already published.

The learner reads the single-operator task database. `scope` is a destination
catalog namespace, not proof that source tasks belong to a particular project
or tenant. Do not use one shared database for mutually untrusted tenants.

## Durable progression

SQLite schema 21 stores a bounded, revision-checked learner cursor. One phase
runs per tick, with a cooperative 45-second step deadline and the existing
tighter bounds for scanning, grouping, inference and publication:

1. Discover one bounded page using the durable scan revision as its retry key.
2. Consume that saved page into workflow buckets. Complete the scan epoch before
   attempting generation; singleton buckets are not repeated workflows.
3. Recheck grouped source tasks, save a deterministic selection, and persist its
   selection ID and bucket ID in the learner cursor **before** inference.
4. Claim that exact selection in the generation ledger, with aggregate cost,
   attempt, in-flight and per-skill cooldown checks. A later tick publishes a
   drafted result through the idempotent publication receipt and advances the
   cursor. Skill names derive from the stable workflow-group identity, not the
   scan epoch or current time.

Stop older writer processes before upgrading. Normal daemon startup migrates
supported older schemas transactionally; read-only inspection never migrates.

Source feedback, provenance, privacy, credentials, tools, context limits and
resources are rechecked by the existing generation path. Discovery is not
permanent permission to use a source. The cursor's policy digest binds the full
configured policy, including endpoint and path changes, without storing or
hashing credential values. A conflicting policy does not silently reset progress.

Concurrent Services cannot claim the same selection twice. Lost cursor updates
revisit saved scan/consumption records, a pinned generation claim or an existing
publication receipt; they do not imply that the model should run again. An
unresolved `started` generation retains its in-flight reservation regardless of
age and stops the learner for operator inspection. Failed generation,
publication failure, stale sources, invalid storage or policy mismatch also
require attention rather than silently skipping work or inventing a new ID.
These failures do not create positive fitness evidence.

## Opt-in validated learning for Go hosts

The Go SDK exposes `LearningStepWithValidation(ctx, validatorID, validator)` for
one bounded phase and `StartLearningWithValidation(ctx, validatorID, validator)`
for an explicitly started background supervisor. The latter returns a
caller-owned `LearningSupervisor`; retain it, inspect `Health()`, and call
`Close()` to cancel and join it. SDK construction does not start learning.

Use an existing, tested `skills.Validator` implementation that performs actual
deterministic checks of the candidate. It is trusted, read-only, repeat-safe,
concurrency-safe and cancellation-cooperative host code, not sandboxed commands.
Generated validation cases, syntactic validity and model judgments are not proof
of workflow quality. No default validator returning success is supplied.

`validatorID` is a stable 1–64 character identifier for the validation policy,
including its implementation and test-suite version. The host must change it
when that policy changes. It participates in the learner policy digest; it is
not a cryptographic attestation of host code. Switching between draft-only and
validated learning, or changing validator identity, conflicts with an existing
learner cursor. Use a deliberately configured new learner name for a new policy;
do not delete or rewrite the old cursor or activation intent to bypass a conflict.

Validated learning still requires `skills.learning.enabled`, generation budgets,
and `skills.auto_activate_after_validation`. Disabling learning stops progression;
disabled activation policy rejects the validated entry point. Standard
`darwin serve` continues to use draft-only learning because it has no configured
trusted validator. These methods do not expose a remote proof-submission endpoint.

After a drafted result is published, validation adds two separate phases while
retaining the pinned generation cursor:

1. Persist an immutable SQLite schema-26 activation intent: selection ID, learner
   revision/policy, validator identity, candidate and exact activation revision.
   No validator is invoked in this phase.
2. On a later tick, load that same intent and call operation-keyed activation.
   Only a passed deterministic check may produce the atomic catalog receipt.
   Then advance the learner cursor past the pending bucket.

Intent creation is bound transactionally to the current learner and its completed
generation. It never updates a previous precondition. The generation selection ID
is the activation operation ID, so a restart after catalog commit but before cursor
acknowledgement recognizes the existing receipt. Even a subsequent rollback is
not undone by this retry. A conflicting independent activation stops progression;
being active without the matching receipt is not acknowledgement of this intent.

Validation errors, panic, non-deterministic or failed evidence stop the supervisor
with operator attention; the intent and pending cursor remain inspectable. Manual
phase retries may rerun the same read-only validator when no receipt exists, but
never regenerate the proposal or silently refresh a stale activation revision.
If the generation record is missing but its activation intent remains, the
learner treats that as corruption and stops; it cannot dispatch the model again.
`SkillLearningActivationIntent(ctx, selectionID)` provides scoped read-only SDK
inspection, including when learning is disabled. It does not initialize storage
or confer activation authority. See [activation operations](skill-activation-operations.md).

Stop older writers and back up the database before schema-26 migration. The
activation itself separately upgrades the file catalog to schema 3. Only trusted
Go-host integration is supported here; standalone daemon validator configuration,
operator intent resolution, statistical outcome regression monitoring, and power-loss
qualification across both stores remain open.

Trusted Go hosts can separately start [periodic deterministic regression
monitoring](skill-regression-monitor.md). Its rollback policy is independent of
learning and new activation; it does not start automatically with a learner.

Ordinary aggregate-budget or cooldown exhaustion waits and retries the same
pinned identity at the configured interval. Health reports show
`learning_budget_wait` as a healthy policy pause, not a daemon outage. Other
learning errors/stalls make readiness unhealthy, while task execution endpoints
remain present for inspection and operator-controlled work.

## Inspect and recover

```sh
darwin skills learning status --config config.yaml
```

This command prints persisted phase, revisions, epoch and pending identities.
It is read-only, creates neither a database nor a skill root, and remains usable
with learning or automatic drafting disabled. The Go SDK exposes the same read
through `Client.SkillLearningState(ctx)`. Live daemon health includes a separate
`learning` component; it contains no task text, provider errors or private paths.

Inspect a pending attempt with `darwin skill-generations show` and inspect
inactive catalog versions with `darwin skills history`. Restore compatible
settings to resume a recoverable phase. There is no automatic discard/reset of
an uncertain generation, no CLI that marks it successful, and no automatic
source-repair policy in this checkpoint. Do not change scan names, attempt IDs
or the database to evade an unresolved claim or budget. Disable the learner
while investigating a durable attention condition.

## Evidence and limits

Controlled HTTP tests exercise actual accepted tool workflows through scanning,
grouping, model generation and one inactive catalog publication. Restart tests
and a failed terminal-write trigger verify no redispatch of an uncertain claim;
two independent Services share exactly one generation claim. Actual daemon
process tests verify that ticks advance and resume durable empty-workflow state
without contacting a provider. Migration, CAS, corruption and rollback tests
cover storage; CLI/SDK tests cover scoped read-only inspection.

This is not semantic equivalence proof, standalone daemon validation/activation,
cross-tenant isolation, large-history performance qualification, or live learning
enabled on the user's data. Recovery controls for discarding permanently stale
sources and reconciling uncertain generation outcomes remain future work.
