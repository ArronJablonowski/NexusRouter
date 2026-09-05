# Background procedural learning

The daemon can discover repeated successful tool workflows, generate an
untrusted procedural draft, and publish it as an **inactive** skill version.
It does not manufacture validation evidence or activate a skill automatically.
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
schema 20 transactionally; read-only state inspection never performs migration.

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

This is not semantic equivalence proof, automatic validation/activation,
cross-tenant isolation, large-history performance qualification, or live learning
enabled on the user's data. Recovery controls for discarding permanently stale
sources and reconciling uncertain generation outcomes remain future work.
