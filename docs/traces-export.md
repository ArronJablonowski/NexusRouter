# OTLP trace export

DarwinRouter can explicitly export a bounded, content-free view of recent
terminal task lifecycles as OTLP/HTTP JSON. Snapshot schema version 2 added the
fixed queue-residency observation; version 3 adds fixed tool-effect evidence:

```sh
darwin traces export --config config.yaml \
  --endpoint http://127.0.0.1:4318/v1/traces --limit 16
```

The equivalent Go SDK calls are `Client.TraceSnapshot(ctx, limit)` and
`Client.ExportTraces(ctx, options)`. Export is a one-shot operator action. It
does not itself enable periodic delivery, start a daemon controller, retry a
request, dispatch inference, create storage or migrate a database.

Periodic export is independently opt-in:

```yaml
telemetry:
  trace_export:
    enabled: true
    endpoint: http://127.0.0.1:4318/v1/traces
    api_key_env: OTEL_TRACE_TOKEN
    interval: 1m
    limit: 16
```

The daemon starts immediately, waits the configured interval after each
completed attempt, and never overlaps attempts within its owned handle. SDK
hosts may explicitly call `Client.StartTraceExport`. Scheduling is ephemeral:
restart begins a fresh cadence and failures are not durably queued or retried.
Health is reported as the supplemental `trace_export` component. Collector
failure degrades overall status but does not make an otherwise usable daemon
unready. Configuration rotation fences an in-flight delivery; callers must
restart the owned exporter to apply new settings. The legacy
`opentelemetry_enabled` switch continues to control only the metrics exporter.

## Scope and privacy

Each trace contains one terminal task root plus successfully paired
provider-turn, tool-call and worker child spans. Fixed zero-duration
observations additionally represent route selection/exploration, evaluation
acceptance/rejection, fallback lineage, compaction, progressive skill-context
loading, steering application, recorded errors and each paired tool completion's
authoritative `none`, `confirmed` or `uncertain` effect classification. Tool
identity and result content remain private, and the effect observation does not
assert that a failed or uncertain operation is safe to retry. A top-level task created by
the durable submission queue also receives one `queue_residency` observation at
task start, classified as `lt_1s`, `lt_10s`, `lt_1m`, `lt_5m`, `lt_30m`,
`lt_1h` or `gte_1h`. The observation is instantaneous: the bucket describes
pre-start waiting without moving the task root before its durable start. Retry
and delegated child tasks do not emit this observation. Each route also emits one
fixed `route_constraint` observation per present mode, privacy, health, policy,
credential, capacity, context, budget or capability exclusion reason. Candidate
identity and counts are not exported. An unknown exclusion reason fails the
snapshot rather than opening label cardinality. Names and outcomes use a closed
vocabulary. Prompt/output text,
messages, tool arguments/results, error details, model/provider/tool names and
all durable task, submission, session, event, route, worker, turn, attempt and
call IDs are never selected into the public snapshot. Exact submission arrival
times are used only inside the bounded storage read and are not exported.

OTLP trace and span IDs are freshly generated for every serialization. They are
not hashes or stable pseudonyms for durable DarwinRouter records. Consequently,
separate exports cannot be joined by their wire IDs. Operators requiring
cross-export correlation must add it outside this privacy-preserving interface.

The reader selects at most 32 terminal tasks (16 by default), in reverse task
creation order, and accepts at most 512 relevant lifecycle events per task and
512 exported spans total. It reads a single SQLite snapshot with a three-second
deadline. The application deadline is four seconds and delivery is bounded by
ten seconds. Invalid terminal timing, contradictory state, duplicate operation
starts, mismatched tool pairs or any exceeded bound fail the export. Unpaired
legacy/interrupted child operations are omitted; the separate metrics snapshot
retains explicit missing-start and missing-end counts.

This trace slice does not include running tasks, model deltas, worker
heartbeats, resource leases/pressure, provider health, fitness mutations,
automatic skill draft/activation/rollback operations or queue arrival/service
rates. Queue residency is historical only for a successfully linked top-level
task start; current queue pressure remains available through aggregate metrics.
Fallback observations come from canonical safe-retry lineage; they do not
assert that an arbitrary failed operation was retried. Skill-context loading
does not prove semantic use. Paired spans measure retained lifecycle wall time,
not provider server latency. Durable delivery, sampling policy, stable
correlation, retention controls and the remaining span families are unfinished.

## Network and credentials

The destination must be an explicit HTTP(S) URL with no user info, query or
fragment. Plain HTTP is loopback-only. Fully local mode additionally permits
only a loopback collector. The same origin-restricted transport used by metrics
export prevents redirects and pins loopback destinations.

`--api-key-env` names a credential; the value is resolved immediately before
delivery, placed only in the Authorization header, checked again for rotation,
and never put in the trace snapshot or request body. The collector must return
HTTP 200 with a bounded valid OTLP JSON acknowledgement. A lost acknowledgement
can occur after collector acceptance, so callers must not assume exactly-once
delivery.
