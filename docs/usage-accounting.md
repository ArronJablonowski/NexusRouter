# Durable usage and cost accounting

SQLite schema 30 adds an immutable, evidence-bound ledger for provider usage
and normalized cost. The ledger separates the model calls that execute a routed
task from auxiliary calls that classify, summarize, audit, or judge it. This
prevents coordinator/auditor spend from being silently attributed to the model
whose answer is under review.

This is retained-operation accounting, not an invoice. Provider usage can be
missing, configured prices can differ from billed prices, and legacy tasks can
predate the ledger. Every such gap remains explicit.

## Records and roles

One version-one record represents one durable operation and binds:

- task, session, operation, route, and evidence identities;
- optional candidate-attempt and audit identities;
- provider, model, role, terminal disposition, and retry classification;
- optional provider-reported input/output tokens; and
- optional normalized USD cost with versioned pricing provenance.

The exact role vocabulary is:

| Role | Group | Current lifecycle source |
| --- | --- | --- |
| `primary_execution` | Routed | A terminal task with no retry predecessor |
| `fallback` | Routed | A terminal task with a validated retry predecessor chain |
| `classifier` | Auxiliary | Reserved contract; no authoritative classifier lifecycle writes it yet |
| `summarizer` | Auxiliary | A terminal summary attempt |
| `orchestrator_audit` | Auxiliary | A public, independently attributed output-audit attempt |
| `optional_judge` | Auxiliary | A terminal legacy optional-judge review attempt |

`routed` is exactly primary plus fallback. `auxiliary` is exactly classifier,
summarizer, orchestrator audit, and optional judge. `overall` is the sum of
those two groups. Per-role totals remain available so an operator does not have
to infer the split from a combined number.

The routed record is created in the same SQLite transaction as its terminal
task event. Summary and review accounting is created in the same transaction as
the corresponding terminal attempt and, for a completed review, its audit
record. Exact acknowledgement retries return the existing fact. A changed
reuse of an ID or of the same task/role/operation identity conflicts rather
than adding another charge.

The ledger stores one aggregate routed operation per terminal task, not a bill
per individual model turn. When a safe fallback starts a new linked task, that
new task receives its own `fallback` record and session attribution. Multiple
completed provider turns within one task are summed only when all pairings and
reported measurements are valid.

Fallback attribution is not inferred from a predecessor ID alone. The complete
lineage is bounded to 32 route attempts and each predecessor must be the exact
durable first-turn lifecycle for `provider_retryable_no_output`, with no output,
steering, tool proposal, or effect. Completed, canceled, context-overflow,
partial-output, unsafe-effect, cyclic, and overlong lineages remain unaccounted
rather than being mislabeled as safe fallback spend.

## Known, unknown, estimated, and reconciled values

Missing usage is `null`, not zero. A record can therefore have known cost but
unknown token usage. This occurs, for example, when a failed or canceled
provider call has no trustworthy terminal usage measurement but its configured
maximum/estimate is still known. Explicit reported zero tokens remain a known
measurement.

Each total reports:

- record count;
- known and unknown usage-record counts;
- known input/output token sums;
- known and unknown cost-record counts; and
- the sum of known normalized USD cost.

Complete `input_tokens`, `output_tokens`, or `normalized_cost` fields appear
only when every record in that total has that dimension. The `known_*` sums are
still useful for partial coverage, but must not be presented as complete totals.

A normalized cost always carries a version-one pricing snapshot: opaque rule
identity and source, SHA-256 digest, currency, method, basis, rates or fixed
cost, and effective time. The closed pricing bases distinguish:

- `configured_estimate`: a route or auxiliary-attempt price captured from
  configuration;
- `provider_reported`: an amount reported by the provider;
- `provider_reconciled`: an amount reconciled to provider billing evidence; and
- `operator_reconciled`: an operator-approved correction.

The current automatic task, summary, and review lifecycle projections use
configured estimates. They are budgeting evidence, not proof of a billed
charge. A configured estimate must never be relabeled as provider-reported or
reconciled merely because the operation completed.

## Corrections and immutable history

The base accounting record is never overwritten. A provider or operator
reconciliation appends a version-one correction that names the base record,
the exact revision it supersedes, an opaque evidence receipt, a closed reason,
and the revised record. Only usage and pricing measurements may change;
execution, attribution, role, evidence, disposition, retry, and occurrence
identity stay fixed.

The current head changes with compare-and-swap semantics, so competing writers
cannot both supersede the same revision. Exact correction replay is
acknowledgement-safe. History reads validate the entire bounded chain and the
stored current head before releasing a result. The current HTTP and Go SDK
inspection surfaces are read-only totals; they do not expose correction
authority. Trusted storage hosts must retain and authorize their own
reconciliation workflow.

Exact base-record acknowledgement replay performs the same integrity read and
revalidates the immutable source measurement. Aggregate inspection validates
base records, heads, and correction chains with two bounded set scans, avoiding
database work proportional to the number of records while retaining fail-closed
corruption detection.

## Inspection surfaces

For one task, an authenticated daemon client can request:

```sh
curl -H "Authorization: Bearer $DARWIN_API_TOKEN" \
  http://127.0.0.1:9786/v1/tasks/TASK_ID/usage
```

The route is a bodyless, queryless `GET` with the same browser-origin denial,
bounded control capacity, five-second deadline, and generic error handling as
other task inspections. An unknown task returns 404. The result is a validated
version-one `Totals` value whose scope contains the authoritative task and
session IDs. It performs no inference, correction, migration, or retry.

Embedded Go hosts use:

```go
totals, err := client.InspectTaskUsage(ctx, taskID)
```

The SDK returns an owned copy with the same validation and read-only behavior.
Automatic route explanations additionally include a point-in-time `usage`
field for that exact task when inspected. It does not roll linked predecessor
or successor fallback tasks into one report; inspect each task in the retry
chain separately. The route decision itself remains immutable: accounting
can arrive later or be corrected later, so the attached totals are an observed
view rather than part of the admission-time routing decision. Explicit-model
tasks still have no automatic route explanation; use the task-usage endpoint or
SDK method instead.

Metrics snapshot version 8 adds identifier-free, global retained-population
accounting for schema-30 stores. It includes the six exact roles plus `routed`,
`auxiliary`, and `overall`, but no task, session, route, provider, model, or
evidence identity. OTLP exports fixed gauges named:

- `darwinrouter.accounting.records`;
- `darwinrouter.accounting.usage.known_records` and `.unknown_records`;
- `darwinrouter.accounting.input_tokens.known` and
  `.output_tokens.known`;
- `darwinrouter.accounting.cost.known_records` and `.unknown_records`; and
- `darwinrouter.accounting.normalized_cost.known`.

Each uses one closed `bucket` label for the nine role/aggregate groups. The
known-cost gauge can mix estimate and reconciled bases; inspect task accounting
and its pricing provenance before interpreting it as billed spend. These are
snapshot gauges over retained state, not monotonic billing counters.

The OpenAI-compatible streaming `include_usage` response remains narrower: it
reports successful model turns for that task only. It excludes failed route
attempts, linked fallback tasks, delegated children, summaries, and audits. Use
the Darwin-native accounting surface for the durable routed/auxiliary split.

## Schema 29 migration and coverage

Normal writable startup migrates schema 29 to schema 30 in the serialized
migration transaction. The migration creates ledger metadata, base records,
heads, corrections, and bounded indexes. It does not rewrite event, review,
audit, summary, fitness, or task-timing records, and it does not fabricate
usage for earlier operations. Unexpected pre-existing ledger objects cause the
migration to fail closed.

For a schema-29 database, a read-only totals query reports
`legacy_unavailable` when matching terminal routed tasks exist. The count is
published as `unaccounted_routed_operations`; all accounting record totals stay
empty. A legacy scope with no terminal task can report complete empty coverage.
After migration, previously terminal tasks remain unaccounted rather than
appearing as zero-cost work. A schema-30 scope can report `partial` when it has
both ledger records and terminal routed operations without a valid accounting
record.

Back up the stopped schema-29 writer before migration. Older binaries cannot
open schema 30. Rollback means pairing the older binary with a separate copy of
the immutable schema-29 backup; there is no supported in-place downgrade. See
[installation, migration and rollback rehearsal](install-migration-rehearsal.md).

Schema 29 introduced task-duration instrumentation. The 29→30 migration keeps
that projection and its cumulative epoch unchanged; usage accounting has its
own metadata epoch and does not reconstruct historical duration or usage.

## Privacy, integrity, and limits

Accounting records contain no prompts, responses, tool arguments/results,
free-form errors, credential references, or provider endpoints. Public
inspection checks every returned identity against current configured secrets
and fails closed rather than partially redacting a record into a misleading
identity. Metrics discard all durable identities and release only fixed buckets.

Provider and model names are still operational metadata and can be sensitive.
The task-level API therefore remains authenticated, and the route explanation
remains an authenticated inspection. Database access can expose the complete
ledger and must follow the same private-path and backup policy as session data.

Current limitations:

- classifier accounting is defined but inactive until DarwinRouter has an
  authoritative model-classifier lifecycle;
- configured costs are estimates, not invoices, quota observations, or proof
  that a failed request was unbilled;
- provider token usage remains unknown when no verified terminal measurement
  survives (for example cancellation, provider/stream failure, abnormal finish,
  or an interrupted stream); a clean terminal measurement is retained when
  later host-side schema validation rejects otherwise malformed output;
- no public API or CLI grants correction authority or exposes full correction
  history;
- route explanations expose accounting only for automatic routes;
- global metrics do not separate configured estimates from later reconciled
  charges; and
- retention/deletion, invoice import, price-catalog refresh, and production
  billing reconciliation remain future work.
