# Automatic skill comparison windows

`darwin skills compare-select --config config.yaml < request.json` selects recent
recorded exposures automatically. The Go SDK exposes `SelectSkillComparison` and
the authenticated HTTP API exposes `POST /v1/skills/comparison/select`.

```json
{
  "version": 1,
  "model_id": "local-worker",
  "domain": "coding",
  "profile": "default",
  "name": "review-code",
  "baseline_version": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "candidate_version": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "source": "user_feedback",
  "min_samples": 20,
  "min_drop": 0.1,
  "privacy": "local_only",
  "tasks_per_version": 100
}
```

All twelve fields are required; substitute actual configured model and recorded
skill version IDs. Bodies are strict JSON, bounded at 64 KiB. Tasks are not
accepted from the caller. Configuration supplies scope and resolves the actual
execution model/provider. `privacy` must be `local_only` or `cloud_allowed`;
unknown legacy privacy does not enter either stratum. Each window cap is 20–100.
The comparison minimum remains 20–100 samples per version, independently of the
window cap; a smaller cap than minimum can only yield insufficient evidence.

## Outcome-independent selection

Within one SQLite read snapshot, the selector pins the highest durable task
insertion ordinal and takes the latest matching exposures for each version,
scope, name and privacy class, up to the configured cap. Ordinals are insertion
order, not wall-clock timestamps: databases predating schema18 have their old
task IDs assigned ordinals in lexicographic order during that migration.

Selection happens **before** filtering for completion, success, quality source,
or actual execution profile. This deliberately leaves canceled, running, failed,
unjudged and wrong-execution tasks visible in the window's exclusion counts.
It never searches farther back to fill the window with passing or rated examples.
The aggregate comparator still requires exact model/provider/domain/profile and
the independent evidence rules described in [outcome comparison](skill-outcome-comparison.md).

Every selected task's original journal and current feedback chain are read and
validated in the same snapshot. Version/digest/privacy/ordinal index metadata
must agree with the recorded context. All-task session lookup excludes a selected
task if any other task in the snapshot shares its session—even if that sibling
is outside the window or has no skill reference. Child/retry lineage and ambiguous
fresh exposure retain the existing conservative exclusions.

Reports include the insertion watermark, per-version selected counts, oldest and
newest selected ordinals, and `has_more` for older matching exposures. When both
windows are empty, `comparison` is absent rather than a fabricated zero-success
result. Otherwise the nested comparison is always advisory. Correlation metadata
is included in the evidence digest, without returning sibling IDs. Empty global
correlation metadata preserves prior explicit-comparison digest encoding.

New reports also contain `sources: {"version": 1, "tasks": [...]}`: the exact
sorted selected task IDs, including excluded observations, captured in that same
snapshot. These are inspectable metadata, not task messages or sibling IDs; treat
exports as sensitive if your task identifiers carry sensitive information.
A known empty selection records an empty list. Older reports without `sources`
remain readable but do not establish recoverable source membership.

## Persistence and bounds

Schema27 stores fresh exposure metadata atomically with TaskStarted and adds
indexed scope/name/version/privacy/insertion-order and session lookups. Migration
backfills validated recorded references using bounded body reads and cancellation;
legacy missing, incomplete or empty attribution creates no exposure. Migration
failure rolls back its schema/index changes. Existing journals are not rewritten.
Append failures roll back the event, task head and index together; exact retries
do not duplicate exposure. The index accelerates lookup; it is not independent
proof that a workflow was executed or that an output was correct.

Selection inspection accepts schema27 or28 but never creates or migrates storage.
Normal write-capable daemon/store startup performs the migration. Older supported
read-only features remain available on their compatible schemas. No live user
database is migrated merely by running the test suite.

At most 200 selected tasks are examined. Existing aggregate limits remain:
10,000 journal events / 8 MiB, 10,000 evaluation metadata rows / 8 MiB, and 4 MiB
of projected observations, plus per-task chain limits. Over-budget or corrupt
selected evidence fails the whole operation; it is not silently skipped. The
configured service checks secrets against complete selected evidence before
returning aggregate metadata. HTTP/SDK/CLI share the same read-only service;
HTTP uses bounded control capacity and a cooperative five-second deadline.

## Interpretation and remaining work

These are automatically selected **latest-window observations**, not a random
sample, full historical population, or controlled experiment. Task difficulty,
workload changes and differences in time between versions remain confounders.
The per-cohort Wilson interval diagnostic does not establish causal regression
or guarantee validity under repeated testing. Privacy stratification and global
session exclusions reduce specific ambiguities; they do not prove independence.

This operation does not activate or roll back skills, change routing fitness,
run validation commands or dispatch models. A separate
[configured outcome supervisor](configured-outcome-supervision.md) now owns
durable repeated monitoring and activation-bound rollback for an explicitly
enabled policy. Qualified semantic validators, causal/confounder controls and
repeated-look correction remain required; read-only selection itself grants no
mutation authority.
