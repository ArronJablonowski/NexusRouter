# Explicit OTLP metrics export

The CLI and Go SDK can send one content-free lifecycle snapshot to an
OpenTelemetry collector using OTLP/HTTP JSON. This is an explicit operation,
not a background exporter. The existing `telemetry.opentelemetry_enabled` flag
remains unsupported by runtime startup and must stay false; periodic export,
traces and full instrumentation remain unfinished.

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

## Data and protocol

Only the existing closed-vocabulary snapshot is exported: tasks, submissions,
reviews, evaluations, audits and recoveries. Each available group is a gauge named
`darwinrouter.<group>` with a fixed `state` attribute. Unavailable legacy-schema
groups are omitted, not represented as observed zeros. Counts are gauges of
current durable state, not cumulative activity counters, latency histograms or
quality judgments. Counts and nanosecond timestamps use decimal strings without
floating-point precision loss.

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
automatic retry, exactly-once guarantee or acknowledgement history is introduced.
Explicitly rerunning the command sends a new snapshot and can repeat observations.

Tests cover wire shape and integer limits, legacy availability, real SQLite
snapshots, owned loopback collectors, strict arguments, credential/policy changes,
no storage mutation, cancellation, response bounds, partial rejection and redirect
denial. They do not qualify a production collector deployment, fleet cardinality,
automatic scheduling, traces, histogram coverage or the full PRD telemetry scope.
