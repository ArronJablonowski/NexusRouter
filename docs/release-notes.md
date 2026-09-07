# DarwinRouter release notes — unreleased

This is a development summary, not a v1.0.0 release announcement. The
deterministic testable-MVP gate passes and 41 of 42 MVP issues are complete, but
DAR-46 remains open. No signed production release, release tag, supported-platform
decision or publication approval is claimed.

## Available for supervised testing

- Versioned configured routing metadata through `darwin models list`, Go SDK
  `ConfiguredModelCatalog`, and authenticated `GET /v1/routing/models`. The
  snapshot carries the redacted configuration fingerprint and declared
  model/provider aliases, capabilities, context, optional cost, resource hints
  and failure domain without discovery or inference. It excludes endpoints,
  credential references/values and live health/capacity claims. The CLI now
  returns a versioned envelope instead of the bare array used by earlier
  development builds; `/v1/models` retains its minimal OpenAI-compatible shape.
  See [configured model metadata](configured-model-catalog.md).

- Content-free durable task discovery through `darwin task list`, interactive
  `/tasks`, Go SDK `ListTasks`, and authenticated `GET /v1/tasks`. Newest-first
  opaque pages freeze their insertion boundary while reporting live state; a
  listed ID still requires an explicit continuation-readiness check before
  `/resume`. See [task discovery](task-discovery.md).

- Metadata-only automatic-route inspection through `darwin task route`, the Go
  SDK's `InspectRouteExplanation`, and authenticated
  `GET /v1/tasks/{id}/route`. The reader reconstructs only the fixed initial
  route boundary and validates policy weights, candidates, ranking, exclusions,
  fallbacks and exploration before returning anything. It contains no prompt,
  model output, endpoint, credential value/reference or tool payload; explicit
  tasks correctly have no route explanation. See
  [route explanation inspection](route-explanation.md).

- Authenticated `GET /v1/models` returns an OpenAI-shaped, configuration-only
  catalog of Darwin model aliases. It is independently capacity-bounded, does
  no discovery or inference, and omits endpoints, credential references,
  routing cost and host resource metadata. Invalid backend catalogs fail closed.
  The compatibility `created` field is zero because upstream creation time is
  unknown; `shutdown_date` is null and `owned_by` identifies DarwinRouter's
  virtual route record rather than ownership of upstream model weights.

- Explicit one-shot OTLP/HTTP trace export through CLI and Go SDK for bounded
  recent terminal task/provider/tool/worker lifecycles plus fixed route,
  evaluation, fallback, compaction, skill-context, steering and error
  observations. It uses fresh wire identities, exports no session content or
  durable IDs, and now has an independently
  configured sequential daemon/SDK supervisor with supplemental health.
  Route constraints expose only nine fixed exclusion reason classes and reject
  unknown labels.
  Linked top-level submissions expose pre-start queue residency through seven
  fixed buckets; submission IDs and exact arrival times remain private, and
  retries/delegated children are excluded.
  Paired tool completions expose only their fixed none/confirmed/uncertain
  effect class, without tool identity, result content or retry claims.
  Delivery remains ephemeral and broader lifecycle spans are open. See
  [trace export](traces-export.md).

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

- Bounded newest-approved-summary discovery for one immutable source task,
  plus an off-by-default automatic-routing policy that retries admission once
  with that draft only when full history has no route. Current rejected heads
  are skipped, corrupted records fail closed, and the task-start transaction
  still rechecks the selected approval. It never drafts or approves summaries.

- The same policy now covers explicit-model initial admission. It measures the
  frozen history, memory, skill and tool-schema assembly before resource or
  residency mutation, selects an approved checkpoint only when it fits, and
  leaves custom-estimator and mid-task decisions fail closed.

- Provider-reported first-turn context overflow can use that policy to start
  one separately linked same-model task with an approved summary. Durable
  replay must prove zero output, tool activity and effects; partial streams and
  ambiguous state remain terminal. Ordered retry lineage, the route-attempt
  ceiling and aggregate configured cost are preserved across an earlier safe
  fallback and the compacted recovery. This does not guarantee that the failed
  provider attempt was unbilled.

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
- Fixed advisory audit-outcome and durable queue-age population gauges,
  privacy-safe live host-resource availability, and paired provider/tool
  lifecycle histograms in metrics snapshot v7. Audit verdicts remain advisory
  and do not replace objective evidence or user feedback.

- Coordinator same-model output audits now run as separate bounded invocations.
  They explicitly target empty, nonresponsive and promise-only output. Accepts
  and abstentions cannot create positive routing evidence; rejections are capped
  at 0.25 advisory confidence, and later direct user feedback still supersedes
  the entire audit signal.
- Adaptive eligibility/ranking, a maximum32-attempt safe provider fallback
  chain with crash-recoverable immediate lineage, local resource admission,
  bounded delegation, validation evidence and advisory output audits. A
  loopback cross-provider fixture qualifies Ollama retryable failure to an
  OpenAI-compatible SSE fallback at the exact cumulative budget boundary and
  proves local-required requests never contact the cloud fixture. Native task,
  stream, CLI, SDK and detached-submission results expose the cumulative
  top-level route estimate. Usage is withheld when any fallback attempt lacks
  durable counts instead of mislabeling final-attempt tokens as a total.
  Provider HTTP413 and exact OpenAI-compatible context-length codes produce a
  durable non-retryable `context_overflow`; automatic fallback is suppressed.
  Local pre-turn overflow converges on that code, while estimator unavailability
  is distinguished as `context_estimation_failed`.
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

The current durable store uses SQLite schema 29. Schema-22-and-newer resource
leases carry private
[execution-image ownership](process-lifetime-ownership.md); foreign processes
cannot mutate a bound lease merely by copying its token. Legacy leases remain
unbound. Schema 23 added the recovery receipts used when the daemon reclaims only
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

The deterministic DAR-45 testable-MVP gate passes and DAR-46 is the only open
MVP issue, but full PRD qualification,
built-in side-effecting CLI tools,
automatic skill validation/activation, learning-attention controls,
configuration reload and supported-platform qualification remain
open. This list is not exhaustive. Distribution also requires an approved
license and notices, a dedicated signing identity with an independent public-key
trust record, approved version-specific notes and explicit publication. Record
the decisions and evidence in the [release checklist](release-checklist.md).
