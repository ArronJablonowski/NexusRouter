# OTLP metrics export

The CLI and Go SDK can send one content-free lifecycle snapshot to an
OpenTelemetry collector using OTLP/HTTP JSON. This is an explicit operation,
not a background exporter. Optional periodic export is separately available
through daemon configuration or an explicitly owned SDK handle below. The
`telemetry.opentelemetry_enabled` switch is a compatibility alias for enabling
the configured periodic metrics exporter; it does not enable traces, which
remain unfinished.

```sh
darwin metrics export --config config.yaml --endpoint http://127.0.0.1:4318/v1/metrics
darwin metrics export --config config.yaml --endpoint https://collector.example/v1/metrics --api-key-env OTEL_COLLECTOR_TOKEN
```

The endpoint is the complete URL, including `/v1/metrics` or the collector's
configured custom path. The CLI requires exactly one config and endpoint, with
an optional environment-variable **name**, never a literal token. It uses the
normal project/environment configuration loader. Set credentials privately in
the process environment; they are sent only in the Bearer authorization header.
The SDK resolves the same name through the client's secret lookup:

```go
err := client.ExportMetrics(ctx, sdk.MetricsExportOptions{
    Endpoint: "https://collector.example/v1/metrics",
    APIKeyEnv: "OTEL_COLLECTOR_TOKEN",
})
```

The collector examples are placeholders, not services contacted by default.
`darwin metrics --db path` remains an independent, local-only JSON inspection
command. No new daemon HTTP endpoint accepts arbitrary export destinations.

## Periodic daemon and SDK export

Daemon export is off unless either `opentelemetry_enabled` or
`metrics_export.enabled` is true. In both cases the `metrics_export` block and
endpoint are required. To opt in, add the following before starting the daemon:

```yaml
telemetry:
  database: ./data/darwin.db
  opentelemetry_enabled: true
  metrics_export:
    # enabled: true is the equivalent newer spelling.
    enabled: false
    endpoint: http://127.0.0.1:4318/v1/metrics
    interval: 60s
    # api_key_env: OTEL_COLLECTOR_TOKEN
```

The interval defaults to 60 seconds and must be between one second and 24 hours.
The normal user/project/environment/flag precedence applies. For example,
`DARWIN__TELEMETRY__METRICS_EXPORT__ENABLED=false` disables configured delivery.
Configuration is snapshotted at startup: change it and restart the daemon to
apply it. The collector endpoint is redacted in configuration display; credential
values stay in the secret resolver and join task/context redaction rules even
when delivery is disabled. A remote collector is invalid in fully local mode.

The daemon starts one immediate attempt only after acquiring its listening socket
and opening storage. It owns the exporter through shutdown. Each attempt reads a
new snapshot, and the interval begins after that attempt completes. There are no
overlapping attempts within a handle, queued missed ticks, failed-body retries or
shutdown flush. Scheduling and delivery history are not durable; restarting
starts a new immediate observation. Separate daemons/SDK handles are independent
and require operator coordination to avoid duplicate streams.

The Go SDK exposes explicit lifecycle ownership:

```go
exporter, err := client.StartMetricsExport(ctx, sdk.MetricsExportOptions{
    Endpoint: "https://collector.example/v1/metrics",
    APIKeyEnv: "OTEL_COLLECTOR_TOKEN",
}, time.Minute)
if err != nil {
    return err
}
defer exporter.Close()
// exporter.Health() reports only bounded status/code metadata.
```

The caller must keep the context alive and close the handle. `Close` is
idempotent, cancels and joins the current attempt, and returns the last recorded
attempt's generic error if it has not since recovered. Cancellation alone does
not assert a delivery failure or non-delivery. Trusted secret callbacks must
return promptly; the runtime cannot forcibly interrupt arbitrary Go callbacks.

The optional `metrics_export` health component starts unknown, becomes healthy
after an acknowledged snapshot, degrades on failure, and recovers after a later
success. Collector failure degrades the health report and daemon status but does
not block task readiness. A stopped enabled exporter reports unavailable. No
collector error text, endpoint or credential enters health metadata.

## Data and protocol

Snapshot schema version 5 adds a fixed `queue_age` population gauge to version
4's optional live host-resource observation and version 3's runtime activity.
For schema-12-and-newer databases, every queued submission is classified once
as less than 1 second, 10 seconds, 1 minute, 5 minutes, 30 minutes, or 1 hour;
at least 1 hour; or invalid time. The fixed buckets are derived inside the same
read transaction as submission state counts and must reconcile exactly with the
queued count. Future or malformed timestamps become `invalid_time` rather than
negative age. Classification reads at most the configured 128-item durable
queue and fails closed if a corrupt store exceeds that bound. The gauges expose
neither submission identity nor exact arrival time.

The application,
daemon, HTTP API and SDK attach fixed CPU-thread, RAM, swap, aggregate VRAM,
thermal-pressure and unified-memory measurements. Each value has an explicit
availability bit: an unsupported or failed probe is unavailable, never an
observed zero. Cloud-only and disabled-profiling services publish the same
explicit unavailability block without invoking a profiler. Storage-only
`darwin metrics --db` snapshots omit the block because reading a database is not
a host observation.

Available resource values export as individual gauges named
`darwinrouter.resource.<measurement>` with fixed units, plus
`darwinrouter.resource.available` for every fixed measurement. No device ID,
GPU inventory, profiler source, thermal-state string or host identity is
released. These are point-in-time host readings, not model residency,
reservation, queue-depth or provider-load measurements. A profiling failure
does not suppress the durable lifecycle snapshot.

The closed-vocabulary lifecycle snapshot contains tasks, submissions,
reviews, evaluations, audits, recoveries and durable runtime-event counts. The
`runtime_events` group reports the sixteen canonical event kinds, covering task
terminals, provider turns, model deltas, tool calls, workers, routes, evaluation
events, errors and steering without exporting an envelope or payload field.
The `runtime_operations` group counts fallback-linked task starts, compacted
continuations, skill-context uses, explored routes, and capacity, budget,
privacy, or health exclusions. Exclusion counts are per excluded candidate;
they are not inferred hardware samples. The submission and queue-age groups are
current durable population gauges, not arrival/service rates or wait-time
histories.
Each available group is a gauge named
`darwinrouter.<group>` with a fixed `state` attribute. Unavailable legacy-schema
groups are omitted, not represented as observed zeros. Counts are gauges of
current durable state, not cumulative activity counters, latency histograms or
quality judgments. Counts and nanosecond timestamps use decimal strings without
floating-point precision loss. Schema29 additionally supplies a cumulative
[task-duration histogram](task-duration-metrics.md) and unavailable timing gauges;
its floating-point sum is in seconds. Legacy schemas omit that instrumentation.

The resource is fixed `service.name=DarwinRouter` and the instrumentation scope
is `darwinrouter.metrics`, version1. No task/session/model identifiers, prompts,
tool arguments, outputs, paths, endpoints or credentials enter the payload. Use a
collector stream per database or configure collector-side resource identity to
distinguish multiple databases; the exporter does not invent a persistent host or
instance identifier. These per-database gauges must not be mistaken for a merged
global count.

Wire fields and response handling follow the [OTLP specification](https://opentelemetry.io/docs/specs/otlp/)
and [official metric schema](https://github.com/open-telemetry/opentelemetry-proto/blob/main/opentelemetry/proto/metrics/v1/metrics.proto).
The package also exposes pure `metrics.MarshalOTLP(snapshot)` for trusted hosts
that own delivery. It performs no network or filesystem access.

## Safety and delivery semantics

- The application reads existing SQLite storage without creating or migrating it.
- Fully local mode allows only literal loopback or pinned localhost collectors;
  hybrid/cloud modes require HTTPS for non-loopback destinations.
  Plaintext destinations and localhost are pinned to loopback in every mode,
  so host resolver configuration cannot redirect them off loopback.
- The owned transport disables proxies; redirects are never followed. URLs with
  user information, queries or fragments are rejected.
- There is one POST and no automatic retry, including on429/503 or partial success.
- The operation has a ten-second cooperative deadline; request and decoded
  response bodies are each bounded to64KiB. Secret callbacks must cooperate.
- Only HTTP200 with a valid JSON export response acknowledging all data counts
  as success. A zero-rejection warning is accepted, but its text is not exposed.
  Partial rejection, malformed acknowledgement, oversize response and failed
  transport return a generic error without private diagnostic text.
- Unknown response fields are ignored for forward compatibility; duplicate
  top-level or partial-success keys and malformed known fields fail closed.

CLI success returns `{"exported":true}`. This means collector acknowledgement,
not downstream persistence. A timeout, cancellation or output failure can occur
after acceptance; failures do not prove zero delivery. No durable delivery ledger,
automatic failed-body retry, exactly-once guarantee or acknowledgement history is introduced.
Explicitly rerunning the command sends a new snapshot and can repeat observations.

Tests cover wire shape and integer limits, all canonical runtime-event kinds,
unknown-kind rejection, legacy and resource availability, real SQLite
snapshots, owned loopback collectors, strict arguments, credential/policy changes,
no storage mutation, cancellation, response bounds, partial rejection and redirect
denial. Periodic tests additionally exercise sequential scheduling, cancellation,
failure recovery, disabled defaults and actual daemon lifecycle wiring. They do
not qualify a production collector deployment, fleet cardinality, durable
export delivery, physical thermal-sensor accuracy, queue arrival/service rates, traces, full
histogram coverage or the full PRD telemetry scope.
