# DarwinRouter release notes — unreleased

This is a development summary, not a v1.0.0 release announcement. No signed
production release, release tag or complete PRD acceptance is claimed.

## Available for supervised testing

- Operation-keyed deterministic skill checks with durable pass receipts,
  atomic failure/rollback receipts and historical retry recognition. Explicit
  Go-host control only; daemon validation and durable monitor scheduling remain
  open. See [regression operations](skill-regression-operations.md).

- Linux thermal trip-point observations now gate new local reservations,
  preserving unknown readings and existing cgroup limits. Fixture tests ran on
  Linux/arm64; physical hot-sensor qualification remains open. See
  [Linux thermal profiling](linux-thermal-profiling.md).

- Embedded Go SDK summary drafting, read-only proposal inspection/pagination,
  operator review/history and explicit approved-summary continuation. Operator
  authentication belongs to the host; automatic approval remains disabled. See
  [SDK session summaries](sdk-session-summaries.md).

- Explicit Sol continuation with manual or approved stored compaction, preserving
  canonical checkpoints, historical tool pairs and the durable-start approval
  boundary. One synthetic live recall check passed. See
  [native compacted continuation](codex-compacted-continuation.md).

- Signed-in Sol session-summary drafting with durable-before-launch execution,
  schema-bound generation and unchanged source provenance. One synthetic live
  check passed; automatic application remains open. See
  [session-summary drafting](codex-session-summaries.md).

- Opt-in local existing-file replacement after exact old/new approval, with
  checked preimages, shared writer leases and private recovery copies. No
  delegated or unattended writes, external-writer fencing or extended-metadata
  preservation are claimed. See [reviewed replacement](reviewed-file-replacement.md).

- Signed-in Sol skill drafting through the native Codex app-server adapter,
  with schema-inclusive context admission, durable-before-launch execution,
  scoped privacy gates and inactive proposals. One bounded live synthetic check
  passed; this does not enable user learning or validate generated workflows.
  See [Codex skill drafting](codex-skill-generation.md).

- Opt-in periodic skill regression monitoring for trusted Go hosts: paginated
  active-state inspection, revision-fenced deterministic rollback, failure
  isolation, sticky health and joined cancellation. No daemon validator or
  statistical outcome detector is enabled. See
  [regression monitoring](skill-regression-monitor.md).

- Opt-in validated learning for trusted Go hosts, with a named validator,
  schema-26 activation intents and restart-safe operation reconciliation.
  The standard daemon remains draft-only. See
  [background learning](background-learning.md).

- Operation-keyed skill activation for trusted Go hosts, with atomic receipts,
  retry recognition after restart/rollback and deterministic validation gates.
  This upgrades the file skill catalog to schema 3; it does not enable background
  activation. See [activation operations](skill-activation-operations.md).

- Corrupt-tolerant daemon attention sweeps: candidate-local failures remain
  visible but no longer block observation of later leases. No automatic repair,
  release or task retry is authorized. See [lease attention](lease-attention.md).

- Schema-25 append-only attention history, including labeled migration baselines,
  atomic projection/history changes and CLI/SDK/authenticated HTTP inspection.
  Schema-24 durable attention for expired unreleased leases is maintained by the
  daemon and inspectable through CLI/SDK/authenticated HTTP without granting release authority.
  Back up databases and stop old writers before migration. See
  [lease attention](lease-attention.md).

- Failure-only orphan recovery after a worker child's completed read-only tools,
  preserving paired results and later interrupted inference without tool/model
  retries. Explicit behavior, no-effect results and retained ownership proof are
  required. See [interrupted workers](orphan-worker-recovery.md).

- New process-ownership guards default to durable private application storage,
  with an explicit location override and continued support for existing temporary
  references. No expiry-based recovery or cleanup authority is added. See
  [execution-image ownership](process-lifetime-ownership.md).

- Bounded read-only resource-scope holder lists through CLI, SDK and authenticated
  HTTP, preserving expired holders and admission overlap rules without granting
  release authority. See [scope holder inspection](scope-holder-inspection.md).

- Read-only task lease and recovery counts through CLI, SDK and authenticated
  HTTP, with explicit legacy availability and no execution capabilities or
  conversation content. See [task lease inspection](task-lease-inspection.md).

- Verified orphan-worker failure recovery when its execution child is already
  terminal and effect-resolved or has a provably model-only interruption.
  Atomic child/worker failure, reader release and receipt, followed
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
- Fixed durable queue-age population gauges, reconciled with submission state,
  plus privacy-safe live host-resource availability in metrics snapshot v5.
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

The deterministic DAR-45 MVP gate passes, but full PRD qualification,
built-in side-effecting CLI tools,
automatic skill validation/activation, learning-attention controls,
configuration reload and supported-platform qualification remain
open. This list is not exhaustive. Distribution also requires an approved
license and notices, a dedicated signing identity with an independent public-key
trust record, approved version-specific notes and explicit publication.
