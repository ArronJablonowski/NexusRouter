# DarwinRouter release notes — unreleased

This is a development summary, not a completed v1.0.0 release announcement. The
deterministic testable-MVP gate passes and 41 of the original 42 MVP issues are
complete, but DAR-46 remains open. The newly required Web UI and integrated
Kanban backlog is also unfinished. A premature annotated `v1.0.0` tag was pushed
outside the guarded publisher and is quarantined; no GitHub Release, signed
production assets, supported-platform decision or canonical publication
approval is claimed.

- DAR-86 adds a required real-Chrome Web UI qualification gate spanning the
  embedded shell, browser authentication/BFF, local/cloud/hybrid application
  paths, durable Workboard scheduling and daemon restart recovery. A hostile
  content fixture proves model/operator markup remains inert across chat,
  cards, criteria and candidate review. Static and computed accessibility
  checks now cover labels, ARIA references, landmarks, keyboard focus, reduced
  motion, WCAG AA text contrast and 3:1 control boundaries; the previously
  under-contrast form border is corrected. The browser EvidenceRecord contract
  now matches the authoritative Workboard reference grammar and rejects
  deterministic abstention consistently in Go, JSON Schema and JavaScript.
  Support claims remain explicit: a recorded no-skip Chrome run is direct
  evidence, while Edge, Firefox and Safari require separate compatibility
  qualification and mobile remains unsupported for v1.

- DAR-83 now adds the first operator mutation controls directly to the
  authenticated Kanban. Operators can create, revise, and archive boards and
  create or revise cards through bounded dialogs with immutable revision
  fences. The browser validates action-specific receipts, scans the durable
  session operation journal before enabling writes, and never automatically
  replays an ambiguous result. Exact browser operation identifiers are safely
  published for reconciliation without exposing request content or domain
  receipt digests. Each card now adds native keyboard move controls for exact
  adjacent reordering and legal Backlog/Ready transitions. They require a full,
  unfiltered snapshot, freeze board/layout/card revision fences, validate the
  exact successor receipt and immutable event, and share the existing no-replay
  ambiguity barrier. Operators can now add or remove one selected-card
  prerequisite from an explicit same-board selector, with complete/unfiltered
  snapshot gating, card/graph fences, exact dependency receipt/event binding,
  and authoritative refresh. Claimed in-progress or blocked cards now expose
  confirmed pause and cancellation request controls with persistent badges,
  exact card/event receipts, and clear non-final status language. Verified stop
  finalization remains open. Review cards now load and validate the exact
  candidate, artifact references, criteria, and evidence before opening a
  decision dialog. Model audits are explicitly advisory; eligible accept/reject
  requests require operator rationale and freeze the card, attempt, candidate,
  evidence, criteria, and policy fences. Acceptance receipts validate the
  bounded primary-plus-successor event range. Authoritative card refetches now
  preserve a bounded keyboard-focus identity across superseding same-board
  refreshes and restore the exact replacement control, its card toggle, or the
  stable Refresh control without retaining stale DOM or stealing newer focus.
  Ordinary Workboard editors now render outside an inert application
  background, exclude overlapping modal state, and restore focus safely.
  Running workers consume exact durable cancellation after a successful
  heartbeat, cancel and join the supervisor callback, persist runtime
  cancellation, and preserve the workboard claim for independent finalization.
  Cooperative pause/resume is now acknowledged only at exact-fenced worker
  safe boundaries while the claim, heartbeat, and supervisor slot remain live.
  Browser end-to-end qualification remains open. Same-board authoritative
  refresh now restores one validated selected/expanded-card anchor and fresh
  detail state without moving focus; board switches and confirmed deletion
  clear it.

- DAR-84 now has its first provider-neutral agent-facing Kanban slice. An
  explicit `tools.workboard_read_enabled` gate exposes bounded
  `workboard_list` and `workboard_read` projections to local root execution.
  The tools use closed schemas, normal durable tool events, a global list scope,
  and exact per-board read scopes shared with writers. A model identity remains
  distinct from operator authority; failures are sanitized and
  existing context/turn limits. Delegated children receive neither tool, and
  extension code cannot shadow their reserved names. A separate
  `tools.workboard_write_enabled` gate adds approval-backed board/card creation,
  board revision/archive, rich card updates, backlog/ready transitions,
  same-lane card reordering, dependency changes, and pause/cancellation requests. Exact
  argument-derived board scopes drive configured policy, approvals, and writer
  leases; caller keys make domain replay idempotent without permitting tool-call
  retries. Proven pre-commit conflicts are recoverable no-effect results, while
  invalid replay receipts and storage ambiguity remain uncertain. Workboard
  events distinguish deterministic task-bound model actors, with selected-model
  provenance retained in the corresponding task journal. Interactive terminal
  chat now presents the exact credential-screened Kanban proposal and one-use
  approval binding. Acceptance, proposal-gated criteria changes,
  durable headless approval presentation, and real-provider UX qualification
  remain open.

- DAR-85 now includes a bounded per-board scheduling cycle over the durable
  supervision projection. It scans the complete configured observation before
  task construction, counts running, stalled, and orphaned claims against its
  WIP limit, schedules only ready cards, binds the authoritative card revision,
  joins cancellation, and contains trusted factory/runner failures. The worker
  runner's transactional claim remains the final duplicate-ownership fence.
  Schema 40 now adds an immutable recovery-to-replacement record derived inside
  the successor claim transaction. It binds the exact recovery, predecessor
  attempt/claim, and successor attempt/claim; clears stale predecessor worker
  assignment on recovery; exposes validated lineage through attempt snapshot,
  detail, and history reads; and conservatively backfills only unambiguous
  legacy successors. Exact replay, competing replacement workers, rollback,
  attempt exhaustion, restart, canonical drift, and migration corruption are
  covered. The worker runner now composes the schema-41 single-journal boundary.
  It uses a capacity-only supervisor slot, binds a trusted runtime request once,
  freezes task/session/parent/worker identity, verifies that the injected live
  store is the exact configured database file, and intercepts the runtime's
  actual redacted first append. That append atomically creates the runtime
  journal/projections and the Kanban attempt/claim/marker; the claim heartbeat
  starts before admission returns. Unbound or bound-but-unstarted callbacks
  leave the card Ready without a task or claim, and successful execution
  produces one real model/tool journal with no synthetic outer task or resource
  lease.

  This integration is intentionally limited to an explicitly selected model.
  It rejects automatic routing, managed residency, worker delegation, provider
  fallback, provider-overflow compaction, and automatic post-run audit. The
  default-disabled scheduler is still not composed into the stock daemon, which
  continues to reject attempts to enable it. Transactional time/token/cost
  budgets, configured independent acceptance judging, and broader
  crash/lease/acceptance qualification remain open.

  Candidate evaluators can no longer claim the operator-owned `user_feedback`
  evidence source. Subjective-only work may enter Review with an exact empty
  evidence-set digest, and the authenticated Web UI Kanban can accept or reject
  it from evidence head zero; that decision appends the first immutable user
  feedback record. Objective acceptance still requires its configured
  deterministic proof. Configuration now publishes an inert, default-disabled
  `workboard.scheduler` boundary. The stock daemon rejects attempts to enable
  it before binding a listener, opening storage, or spawning a managed process,
  pending safe task attribution, global budgets, and judge composition.

- Independent post-publication verification can now reserve durable evidence,
  install through pinned private directories, and retain canonical native
  evidence after executing the host-matching artifact from the exact
  receipt-bound downloaded bytes. The evidence binds the completion time,
  installed binary digest, mode, and exact version output to both the
  immutable-release receipt and final install/migration/backup/rollback
  rehearsal. Execution still requires a disposable, low-privilege,
  credential-free, network-denied host; the minimal child environment is not a
  sandbox. This adds no publication, installation, or rollback authority.

- The production `build-approved-release` command now requires the exact
  independently reviewed candidate-record digest, builds the four-target set
  twice in isolated directories, compares every unsigned byte, and atomically
  retains one compared output. It reports the retained checksum-set digest and
  never substitutes a third build. Signing and publication remain separate
  operator-controlled gates.

- The independent `verify-approved-release` command now checks a production
  signature through one pinned release root while binding the exact candidate,
  candidate-bound license evidence, checksum set, signing authorization, active
  trust record, release-policy URL, key identity and clean source commit. Its
  canonical JSON result contains only public digests and identifiers for the
  operator evidence record.

- Candidate license evidence now records the root MIT license, exact Go
  version/directive, all four target dependency/legal-file closures, Go runtime
  `LICENSE` and `PATENTS`, and target notice hashes. Packaging pins one absolute
  Go executable for version, module verification, notice discovery and builds.
  Production authorization separately requires `project_license: approved` and
  binds the exact evidence digest before signing or independent verification.

## Available for supervised testing

- The Web UI and integrated Kanban now have an accepted architecture decision
  plus reusable version-1 Go/JSON contracts and fixtures. The boundary preserves
  native bearer APIs, requires a same-origin cookie/CSRF BFF, separates
  provisional chat text from committed state, and makes the workboard a durable
  CAS/idempotent domain with typed projections, bounded graph operations,
  stop-proof recovery, and independently evidenced acceptance. A minimal
  versioned shell is embedded and served through one-time CLI-approved browser
  challenges, bounded process-local sessions, same-origin CSRF grants, and
  strict security headers. The browser-safe chat/session surface now adds bounded
  paginated chat and history projections, text-only rendering,
  provisional post-commit redacted model text, and HMAC-bound durable SSE
  replay for typed model, tool, route, worker, error, and terminal state. Chat
  mutations are now available through direct in-process browser facades:
  submit/resume, steering, task and queued-submission cancellation, subjective
  feedback record/revision, and tool-approval allow/deny/revoke. The embedded
  client reconciles task controls, feedback context, approvals, recent
  operations, and submission status after refresh or ambiguous acknowledgement.
  Schema-35 Kanban persistence now exists. The second partial DAR-82 checkpoint
  adds authority-gated board domain/service contracts, board create/revise/list/
  read/archive and redacted event reads, plus card create/revise/move/reorder/
  dependency SQL. Native JSON handlers and browser-session/CSRF BFF contracts
  cover board list/create/read/operations. Canonical events, revision and graph
  fencing, exact replay, aggregate transaction-byte checks, and authenticated
  bounded cursors are covered. A later DAR-82 checkpoint adds live daemon
  composition, reconnectable board SSE, candidate/evidence/acceptance,
  pause/cancel and block/unblock control, bounded lifecycle reads, and durable
  stale-claim attention. Worker-owned mutations run through a fixed-authority
  dispatcher atomically bound to runtime task/session identity. Effect-free
  failures release claims; uncertain effects remain blocked without replay.
  Independently derived stop proof, bounded attention pagination, lifecycle and
  dependency history, durable workspace identity, and cross-session operation
  reconciliation complete the DAR-82 backend. Agent tools and the integrated
  Kanban feature UI remain DAR-84 and DAR-83 work.

- Schema 35 adds the native workboard storage foundation to the primary
  SQLite/WAL database. Normalized bounded tables cover boards, seven canonical
  columns, ordered cards, dependency edges, immutable events, attempts, claims,
  heartbeats and checkpoints, candidates, evidence, acceptance decisions, recovery proofs,
  and scoped idempotency receipts. Database constraints enforce identity,
  lifecycle enums, referential bindings, lease TTLs, row sizes, and local count
  limits where SQLite can do so; cycle, depth, and transaction-wide graph
  checks remain application-service responsibilities. Migration is serialized,
  restart-safe, and fails atomically on partial or forged retained objects.
  DAR-81 itself is storage only. The subsequent partial DAR-82 work now covers
  board metadata lifecycle, core card/dependency persistence, native JSON
  routes, and browser BFF handler contracts. It does not yet provide browser
  SSE, live CLI/daemon composition, the full claim/attempt/evaluation lifecycle,
  agent tools, or the integrated Kanban UI; DAR-82, DAR-83, and DAR-84 still own
  that remaining work.

- Schema 34 adds the session-subject-bound browser operation journal and
  additive browser feedback revision chain to the primary SQLite/WAL database;
  there is no browser sidecar database. Request digests and idempotency keys
  make exact retries replayable and different-body reuse conflicting.
  Definitive sanitized failures are durable `rejected` results, while ambiguous
  interruptions remain `pending`. Pending operations are never silently
  discarded; terminal rows have bounded age/count retention and the entire
  journal has a hard capacity limit. Authenticated, bounded recent-operation
  and submission-status reads support reload reconciliation. Subjective user
  revisions coexist with objective and advisory evaluation evidence rather
  than replacing it. Approval scope is freshly redacted and bounded before it
  enters the browser projection.

- DAR-80 adds a responsive, authenticated inspection surface to the embedded
  Web UI. Bounded GET-only projections show the configured model catalog,
  selected route and candidate dispositions, task usage, paired normalized tool
  lifecycles, redacted audit provenance, daemon health, and host resources. A
  whole projection that cannot be observed is labeled `Unavailable`; an absent
  optional measurement is labeled `Unknown` rather than zero. Usage preserves
  separate routed execution and auxiliary classifier, summarizer,
  orchestrator-audit, and optional-judge totals. Tool rows omit arguments and
  results, while audit rows expose only sanitized findings, bounded evidence
  references, reviewer/model provenance, rubric version, and ordered evidence
  precedence. Model/route/health snapshots and cursor-paged tool/audit reads
  are independently bounded, and the browser applies an aggregate display cap.
  The inspector performs no POST, does not redispatch work, and grants no model,
  routing-policy, approval, or runtime mutation authority.

- An explicitly selected stock summary-integrity validator now emits bounded,
  deterministic advisory evidence without ever approving a model-authored
  summary. It rejects provenance/checkpoint drift, unsafe display controls,
  duplicate normalized entries, and unsupported high-confidence anchors; clean
  or ambiguous drafts abstain for authenticated operator or domain validation.

- The 1.0 PRD now includes an authenticated embedded Web UI for streaming chat
  and a native durable Kanban for operator- and agent-managed long-running work.
  This is specified scope and a dependency-linked Linear backlog, not completed
  runtime functionality in this checkpoint.

- Schema 30 durable provider accounting now records evidence-bound
  `primary_execution`, `fallback`, `summarizer`, `orchestrator_audit`, and
  `optional_judge` operations while reserving `classifier` for a future
  authoritative lifecycle. Task-scoped API/SDK inspection, automatic route
  explanations, and identifier-free metrics
  keep routed and auxiliary totals separate. Missing usage remains unknown,
  configured estimates remain distinguishable from billed/reconciled cost, and
  reconciliation appends immutable corrections. The schema-29 migration does
  not backfill pre-ledger work and preserves the task-duration epoch. See
  [durable usage and cost accounting](usage-accounting.md).

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
  leaves custom-estimator and custom-context-engine decisions fail closed.

- Provider-reported first-turn context overflow can use that policy to start
  one separately linked same-model task with an approved summary. Durable
  replay must prove zero output, tool activity and effects; partial streams and
  ambiguous state remain terminal. Ordered retry lineage, the route-attempt
  ceiling and aggregate configured cost are preserved across an earlier safe
  fallback and the compacted recovery. This does not guarantee that the failed
  provider attempt was unbilled.

- Eligible built-in continuations can retain full history for turn one and
  activate one frozen, currently approved summary only when later completed-turn,
  tool, or steering growth would overflow. The durable `context.compacted`
  boundary precedes mutation and redispatch, revalidates approval/source state,
  preserves the complete live suffix, survives replay, and reserves terminal
  recovery capacity. Custom context engines, delegated tasks, already-compacted
  sources, and the Codex app-server remain fail-closed.

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
- Local four-target release packaging, checksummed manifests, independently
  bound production signing and offline verification. Each supported target has
  a one-host [native evidence contract](native-target-qualification.md).
  Production signing requires exact candidate, license-evidence, checksum,
  trust and external [signing-authorization](release-signing-authorization.md)
  identities plus explicit project-license approval before private-key access;
  signing authority cannot grant publication. See
  [packaging instructions](release-packaging.md).

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

The current durable store uses SQLite schema 43. Schema 30 added the immutable
[usage and cost ledger](usage-accounting.md) without reconstructing earlier
usage; schema 31 adds the routing-key index used by adaptive observation reads,
schema 32 adds immutable submission-wide stream cursors with per-event body
digests for reconnect-safe native task streaming, schema 33 adds the globally
ordered committed-runtime-event ledger used for restart-safe SDK catch-up, and
schema 34 adds browser-session-bound operation and additive subjective-feedback
records in the same SQLite/WAL store. Schema 35 adds bounded normalized native
workboard storage and validates its exact tables, rules, indexes, triggers, and
foreign-key integrity before advancing. Schema 36 adds normalized card identity
to immutable workboard events through a serialized table rebuild; existing
schema-35 board events are preserved with `NULL` card identity, and partial or
forged retained schema fails without advancing. Schema 37 adds a non-secret
persistent workspace identity that survives database backup and restore.
Schema 38 adds exact cross-session browser-operation reconciliation while
preserving the initiating subject and safely isolating legacy pending workboard
operations. Schema 39 adds a durable workboard pause phase while retaining the
version-1 `pause_requested` compatibility bit; legacy true values migrate only
to requested, never to an unproven worker acknowledgement. Schema 40 adds an
immutable recovery-to-replacement link bound to exact predecessor and successor
attempt/claim identities; conservative upgrade backfill links only unambiguous
successors. Schema 41 atomically binds the first runtime event to the Workboard
claim, and schema 42 adds immutable execution admission and settlement records
for time, token, cost, route, configuration, and WIP budgets. Schema 43 adds
immutable card-owned auxiliary-review admissions and settlements bound to the
candidate, reviewer, configuration, and time/output-token/cost ceilings. Browser sessions
remain process-local and are revoked on restart
even though durable workspace authority and operation records survive migration,
backup, and restore. Schema-34 migration validates exact table shape and rules
and fails closed on inconsistent partial objects. Schema-29-and-newer stores
retain the existing task-duration epoch.
Schema-22-and-newer resource
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
third-party dependency notices, a dedicated signing identity with an independent public-key
trust record, approved version-specific notes and explicit publication. Record
the decisions and evidence in the [release checklist](release-checklist.md).
