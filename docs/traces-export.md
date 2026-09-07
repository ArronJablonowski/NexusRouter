# OTLP trace export

DarwinRouter can explicitly export a bounded, content-free view of recent
terminal task lifecycles as OTLP/HTTP JSON:

```sh
darwin traces export --config config.yaml \
  --endpoint http://127.0.0.1:4318/v1/traces --limit 16
```

The equivalent Go SDK calls are `Client.TraceSnapshot(ctx, limit)` and
`Client.ExportTraces(ctx, options)`. Export is a one-shot operator action. It
does not enable periodic delivery, start a daemon controller, retry a request,
dispatch inference, create storage or migrate a database.

## Scope and privacy

Each trace contains one terminal task root plus successfully paired
provider-turn and tool-call child spans. Names and attributes use a closed
vocabulary: `darwinrouter.task`, `darwinrouter.provider`,
`darwinrouter.tool`, and their fixed completion outcome. Prompt/output text,
messages, tool arguments/results, error details, model/provider/tool names and
all durable task, session, event, route, worker, turn, attempt and call IDs are
never selected into the public snapshot.

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

This first trace slice does not include running tasks, model deltas, routes,
workers, evaluations, retries, fallbacks, compactions, fitness or skill
changes. It is retained lifecycle wall time, not provider server latency.
Periodic trace scheduling, durable delivery, sampling policy, stable
correlation, retention controls and broader span families remain unfinished.

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
