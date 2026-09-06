# DarwinRouter release notes — unreleased

This is a development summary, not a v1.0.0 release announcement. No signed
production release, release tag or complete PRD acceptance is claimed.

## Available for supervised testing

- Verified orphan-worker failure recovery when its execution child is already
  terminal and effect-resolved. Atomic failure/reader release/receipt, followed
  by independently validated parent reconciliation; no output acceptance or
  model retry. See [orphan worker recovery](orphan-worker-recovery.md).

- Declared read-only/idempotent-write/non-idempotent-write tool behavior, with
  durable event/approval binding and no added retry authority. Both writer
  classes retain approvals and writer leases; see [tool behavior](tool-behavior.md).

- Explicit effect-free read/delegation failure feedback with bounded model repair,
  durable failed-step history and learning exclusion. Native Codex failure
  handoff and invalid-worker→corrected-worker delegation passed bounded live
  Sol tests with controlled local output; see
  [recoverable tool failures](recoverable-tool-failures.md).

- Opt-in local `create_file` with terminal per-call approval, complete bounded
  previews, durable one-use authority and atomic no-overwrite publication.
  Existing-file editing and unattended approval remain unfinished; see
  [reviewed file creation](reviewed-file-creation.md).

- Go runtime, CLI, Go SDK and authenticated loopback HTTP service with durable
  SQLite/WAL task/session records and explicit configuration.
- Provisional, incrementally redacted live text through the OpenAI-compatible
  HTTP endpoint, Go SDK and interactive CLI chat, with a combined lifecycle/text
  SDK interface and stateful terminal filtering in chat.
- Adaptive eligibility/ranking, provider fallback, local resource admission,
  bounded delegation, validation evidence and advisory output audits.
- Experimental signed-in Codex coordinator integration: an actual GPT-5.6 Sol
  → local Ollama → Sol round trip, historical continuation and a separate Sol
  output audit have been qualified. See [the integration guide](codex-coordinator-integration.md).
- Scoped lexical memory retrieval and operator memory management, progressive
  skills, and opt-in background workflow learning that creates **inactive**
  drafts rather than automatically trusting generated instructions.
- Replaceable context planning, bounded safe compaction paths, daemon lifecycle
  controls and recovery for the specifically qualified interruption boundaries.
- Opt-in managed residency for dedicated Ollama servers. Sharing a managed
  endpoint with other clients is not supported.
- Local four-target release packaging, checksummed manifests, independent-key
  signing and offline verification. See [packaging instructions](release-packaging.md).

Each item has narrower limits than the eventual PRD. [Implementation evidence](progress.md)
records tests and remaining work; fixtures and cross-builds are not proof of all
live-provider, hardware or crash scenarios.

## Upgrade cautions

- Built-in file tools now share `workspace` reader/writer leases across all
  roots, with legacy `create_*` scopes retained as conflicting. Expired readers
  no longer stop blocking writers until explicitly released after termination.
  Do not run older and newer binaries against the same database concurrently.
  See [reader/writer execution](reader-writer-execution.md) for availability and
  crashed-holder limits.

The current durable store uses schema 23. New resource leases carry private
[execution-image ownership](process-lifetime-ownership.md); foreign processes
cannot mutate a bound lease merely by copying its token. Legacy leases remain
unbound. The daemon can now reclaim only
[verified terminal orphan readers](terminal-reader-recovery.md), with atomic
audit receipts and no model/tool replay. Running work, writers and unknown
ownership remain unresolved. Guard storage currently
requires a supported local temporary filesystem and retained guard paths; review
the documented platform and retention limits before unattended use.

Stop older writer processes and back
up task databases before opening them with a newer build. Restoring an older
binary alone does not downgrade a migrated database. Preserve the matching
pre-upgrade data backup for rollback; no automatic destructive downgrade exists.

Background learning and managed model residency default off. Do not enable
either merely to try a newer binary: review scope, budgets, privacy, dedicated
server ownership and recovery semantics first. Keep credentials outside source
configuration and logs. Existing Codex login is used without copying credentials
into DarwinRouter or artifacts.

## Required before announcing v1.0.0

Full PRD qualification, built-in side-effecting CLI tools,
automatic skill validation/activation, learning-attention controls,
configuration reload, performance and supported-platform qualification remain
open. This list is not exhaustive. Distribution also requires an approved
license and notices, a dedicated signing identity with an independent public-key
trust record, approved version-specific notes and explicit publication.
