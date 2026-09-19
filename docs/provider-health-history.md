# Provider health history

DarwinRouter stores a bounded local history of its validated provider and model
health probes. This supplies restart-safe operational evidence without turning
health GETs, the Web UI, or SDK inspection into write operations.

```yaml
telemetry:
  database: /absolute/private/path/darwin.db
  provider_health_history:
    enabled: true
    interval: 30s
    retain: 2880
```

The interval accepts 5 seconds through 1 hour. `retain` accepts 1 through
100,000 complete reports. The defaults retain 24 hours at the default cadence.
Sampling begins when the daemon's supervisors are composed, runs sequentially,
and waits a full interval after every completed attempt; slow probes therefore
cannot overlap or accumulate a queue of missed ticks. A failed attempt is not
replayed and does not stop task serving. A later successful sample recovers the
history naturally.

Schema 53 stores canonical validated health reports and normalized lookup rows
for only `provider` and `model` checks. Persisted values are limited to configured
safe IDs, timestamps, readiness, and closed status/code enums. Provider
endpoints, credentials, prompts, responses, tool content, and provider error
text are never part of the record. Exact retries are idempotent. Retention
deletes an old report and all of its normalized checks in the same transaction.

The storage reader revalidates canonical JSON, its SHA-256 identity, duplicated
report metadata, every normalized check, schema bounds, and report semantics
before returning history.
Malformed, mismatched, partially migrated, or future-version data fails closed.
The current slice provides the durable store and daemon sampler; a later
operator-facing history view may consume the read-only store method without
changing these write boundaries.
