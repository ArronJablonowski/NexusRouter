# Commander log collection

The commander can continuously collect private runtime history and security audit
records from explicitly authorized paired NexusRouter instances. HTTPS with pinned
mutual TLS is mandatory; the existing SSH transport can carry the same protocol.
Discovery alone never authorizes collection.

## What is collected

- **Runtime:** the canonical committed event stream, including full recorded user
  prompts and assistant responses, tool calls/results, task/session identities,
  routing decisions, provider/model usage, evaluation events and context changes.
  Original recorded content and existing redactions are preserved. Events retain
  source positions and identities; this is not a lossy summary of chat history.
- **Security:** the remote control-plane audit, including admitted, denied,
  rate-limited, succeeded and failed authenticated requests.
- **Collection health:** last attempted and successful collection, record counts,
  and explicit failures for each instance and stream.

The private replica is evidence for inspection, not authority. Collection does
not dispatch tasks, judge outputs, modify feedback, execute remote text, or merge
remote learning evidence into the commander's own learning database.

This is not an OS log forwarder. It does not collect systemd/journald, failed TLS
handshakes before peer authentication, drafts that were never submitted to the
runtime, or standalone learning-store records that have no canonical runtime
event. Those require separate export contracts. It cannot recover history already
removed at the source. Collection failure is not proof of a security incident.

## Explicit permission

Add `logs` to the paired peer's `operations` on **both** systems, using the existing
pair/replace-trust commands and expected-current-digest checks. The remote must
grant its commander peer this permission; the commander must authorize collecting
from that destination. Existing `info`, `inspect`, and `allow_private` settings do
not implicitly grant it. The Web UI pairing form exposes an unchecked full-chat
collection permission, with the resulting policy shown before pairing.

`logs` grants instance-wide read access, including private chat content from other
callers. It does not grant dispatch or cancellation. Revoking it takes effect on
the next request without a router restart. Previously collected records remain in
the commander's private store. The operation has its own rate-limit bucket, using
the peer's configured `info` limit (60 requests/minute by default).

## Run on the commander

Create an owner-private parent directory first. Use the existing verified pairing
and credential paths; do not copy private keys into the log directory.

```sh
nexus remote collect-logs \
  --trust /private/remote/peers.json \
  --cert /private/remote/commander.pem \
  --key /private/remote/commander.key \
  --ca /private/remote/ca.pem \
  --log-store /private/central-logs \
  --watch --interval 5s
```

Omit `--watch` for one bounded collection pass. Supply `--instance dgx-spark` to
restrict collection; otherwise all currently logs-enabled peers are polled. Each
pass collects up to 100 records from each stream for each peer. The registry is
reloaded between passes, and each request rechecks trust and credentials. A failed
stream or peer does not prevent the others from being polled. Watch mode retries
failures on subsequent passes and emits **metadata only**, never chat bodies.
Use your service manager to keep this command running and restart it after exits.

```sh
nexus remote logs-status --log-store /private/central-logs
nexus remote logs-read --log-store /private/central-logs \
  --instance dgx-spark --stream runtime --task TASK_ID --after 0 --limit 100
nexus remote logs-read --log-store /private/central-logs \
  --instance dgx-spark --stream security --after 0 --limit 100
```

`logs-read` deliberately prints private content. Keep redirected output private.
For subsequent pages, pass the last returned `position` as `--after`. Results are
also bounded to 9 MiB; a nonempty page can therefore contain fewer than `--limit`
records. `logs-status` reports collection times, not a guarantee that all source
history has been caught up or that a task succeeded.

## Storage and recovery

The owner-private directory contains `logs.sqlite` and SQLite sidecars, all
restricted to the owner. The directory must not be a symlink or group/world
accessible. Records are separated by source and stream, with source timestamp,
collection timestamp, original body and SHA-256. Runtime task/session IDs remain
queryable. Cursors and records commit in the same durable transaction, so process
interruption cannot advance a cursor past uncommitted records. Simultaneous
collectors cannot silently append duplicate pages; stale cursors are rejected.

Cursors bind canonical event IDs and security audit hashes. History gaps, source
replacement, changed anchors or pruned history fail closed. Inspect and reconcile
these failures; do not reset a cursor merely to hide a gap. No automatic retention
or deletion is performed. Back up the database consistently and budget disk space
for full chat histories. Full-disk errors preserve the prior cursor for recovery.

The endpoint is `/v1/remote/logs`, with stream/cursor in bounded headers. Page
responses are bounded, validated and served with `Cache-Control: no-store`. Raw
content is excluded from routine collection diagnostics and the normal metadata
and metrics endpoints. A collected hash detects accidental local body changes;
it is not a remote signature or proof that a compromised source told the truth.
