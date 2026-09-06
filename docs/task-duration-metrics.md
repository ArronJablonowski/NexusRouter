# Durable task-duration metrics

Schema 29 adds a task timing projection and a persistent instrumentation epoch.
Normal task events, orphan-worker recovery and interrupted-submission recovery
update timing in the same transaction as their event/head changes. A rejected or
rolled-back append cannot leave a timing sample behind. Acknowledgement retries
do not add another sample for the same task.

The measurement is the elapsed **event wall time** from persisted `task.started`
to the terminal task event. It includes model calls, tools, approval waits and
time spent interrupted before recovery. It excludes queue/admission work before
the task starts. It is not inference latency, CPU time, routing overhead, output
quality or proof of meeting an SLA. Forward clock jumps can inflate durations;
backwards timestamps and intervals that overflow Go's duration range are explicitly
unavailable, never clamped to a zero-duration success.

## Inspection and export

Existing `darwin metrics --db path`, application metrics, authenticated
`GET /v1/metrics`, SDK export and periodic daemon export carry the new
`task_duration` field for schema-29 stores. Older schemas omit it rather than
inventing measurements. No additional endpoint or export permission is introduced.

The fixed terminal groups are `completed`, `failed` and `canceled`. Each contains
an observed count, floating-point sum in seconds, eleven bucket counts, and
unavailable counts for `missing_start` and `invalid_time`. The ten upper-inclusive
thresholds, in seconds, are:

```text
0.1, 0.5, 1, 5, 10, 30, 60, 300, 1800, 3600
```

The last bucket contains larger durations. Bucket selection uses integer
nanoseconds; the sum is a floating-point aggregate, not an exact nanosecond sum.
Each terminal state's observed plus unavailable counts must match its lifecycle
count in the same SQLite read snapshot. Running tasks are not finished-duration
samples. No task, session, model or provider identifiers, prompts, tool arguments,
outputs or error messages are exported.

OTLP exports `darwinrouter.task.duration`, unit `s`, as a cumulative histogram with
the persisted migration epoch as its start timestamp and snapshot time as its
observation timestamp. `darwinrouter.task.duration.unavailable` is a gauge with
fixed `state` and `reason` attributes. The existing lifecycle gauges are unchanged.
These representations follow the [OpenTelemetry metric data model](https://opentelemetry.io/docs/specs/otel/metrics/data-model/)
and [OTLP metric schema](https://github.com/open-telemetry/opentelemetry-proto/blob/main/opentelemetry/proto/metrics/v1/metrics.proto).

## Migration and coverage

Back up operational stores and stop older writers before normal startup migrates
them. Schema 29 creates metadata-only tables; it does not rewrite journal bodies,
reconstruct historical timing samples or change routing/evaluation evidence.
Read-only commands never migrate a store. A failed migration rolls back the new
tables and version change together.

Tasks that finished before instrumentation have no retrospective sample and are
counted as `missing_start`. A pre-migration running task that later finishes is
also missing its captured start. The unavailable count makes coverage explicit;
do not interpret a small observed population as representing all historical work.
The migration epoch survives restarts and ordinary repeated opening.

Timing reads inspect bounded projection fields and terminal identity/sequence
joins, never journal payload bodies. The projection's timestamp grammar, shape,
state bindings, counts and histogram totals are checked; reads do not recompute
duration from the original journal. This is an operational projection, not a
tamper-proof audit of an arbitrarily modified database. Integrity failures return
generic unavailability. Reads scan the retained population under the existing
three-second storage deadline and may time out for very large or pressured stores.

## Cumulative-stream limits

Each database needs an independent collector resource identity/stream, and a
single coordinated exporter should own that stream. Multiple processes or SDK
handles exporting the same database can create overlapping observations. Copying,
restoring, deleting or manually editing a store is not an automatic cumulative
reset protocol. Coordinate a new collector stream/resource identity when replacing
or restoring a database. No task-history retention/deletion operation is added.

Tests cover exact bucket edges, missing/invalid timing, atomic normal and recovery
writes, migration rollback, corruption handling, restart persistence, private-body
isolation and public/OTLP propagation. Production collector integration, large-store
read/write throughput, routing/provider/tool-specific latency histograms, costs,
traces and the broader PRD instrumentation remain open.
