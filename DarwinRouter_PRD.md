# Product Requirements Document: DarwinRouter

**Version:** 1.0.0

**Author:** Arron Jablonowski

**Date:** September 4, 2026
**Status:** Draft / Specification

---

## 1. Executive Summary

DarwinRouter is a Go-based, local-first agent runtime whose defining capability is adaptive model routing. It evaluates tasks, chooses among heterogeneous local and cloud models, executes provider-neutral agent and tool loops, measures outcomes, and improves future routing from durable evidence.

The product combines a compact event-driven runtime, persistent sessions and memory, progressively loaded procedural skills, bounded worker delegation, hardware-aware scheduling, auditable policy enforcement, and restart-safe telemetry. It runs as a persistent daemon controlled through a CLI, a versioned Go SDK, an OpenAI-compatible HTTP surface, Darwin-native task-management APIs, and an authenticated Web UI for chat and durable work planning.

DarwinRouter supports fully local, fully cloud, and hybrid operation. Fully local mode is an enforced privacy boundary: unauthorized outbound transports must be denied, not merely left unconfigured. Hybrid mode favors local execution when it satisfies task, privacy, quality, and resource constraints, then uses cloud capacity where policy permits.

## 2. Product Vision and Success Criteria

### 2.1 Vision

Provide one trustworthy control plane for agents and applications that need to use multiple LLMs without hard-coding a provider, oversubscribing local hardware, losing task state after a crash, or treating subjective model self-assessment as proof of success.

### 2.2 MVP Success Criteria

DarwinRouter 1.0 is successful when it can:

1. Accept tasks through the CLI, Go SDK, and HTTP API and stream a common typed event sequence.
2. Execute a provider-neutral model/tool loop against at least one OpenAI-compatible cloud endpoint and one local Ollama/OpenAI-compatible endpoint.
3. Select primary and fallback routes using policy constraints followed by normalized fitness ranking.
4. Persist sessions, task state, routing evidence, evaluations, skills, and memory transactionally in SQLite/WAL.
5. Resume or resolve interrupted work without duplicating uncertain side effects.
6. Run one orchestrator with bounded in-process workers while enforcing parallel-read/single-writer rules.
7. Demonstrate zero unauthorized external requests in fully local mode.
8. Meet routing-overhead targets of under 150 ms for deterministic classification and under 500 ms for auxiliary-model classification, excluding provider inference.
9. Create and revise procedural skills automatically within configured scope, with validation, version history, and rollback.
10. Explain every model route without persisting secrets, full prompts, or sensitive output.
11. Provide an authenticated, responsive Web UI for streaming chats, session
    history, approvals, feedback, route inspection, and runtime status.
12. Provide an integrated Kanban board whose durable cards, dependencies,
    leases, acceptance evidence, and lifecycle state can be used by both an
    operator and policy-constrained DarwinRouter workers for long-running work.

### 2.3 Non-Goals for 1.0

- Messaging-platform gateways, voice interfaces, and personal-assistant features.
- General-purpose cron or workflow automation.
- Automatic model deletion, disabling, or policy changes without operator approval.
- Parallel side-effecting agents without isolated execution.
- Built-in subprocess, Git-worktree, container, SSH, or remote worker backends; these are post-MVP adapters.
- Multi-tenant enterprise administration, native mobile clients, or direct
  synchronization with third-party boards such as Linear.

### 2.4 Licensing and Attribution

DarwinRouter is distributed under the MIT License, matching Hermes Agent's
public license family while retaining DarwinRouter's own copyright ownership.
Architectural ideas and independently implemented behavior do not transfer an
upstream copyright. If DarwinRouter later copies or substantially adapts Hermes
Agent code or documentation, the applicable Nous Research copyright and MIT
permission notice must be preserved with those portions.

Every binary distribution must include DarwinRouter's root `LICENSE` and a
candidate-bound third-party notice bundle for the exact dependencies, toolchain,
targets, and build inputs being shipped. Mechanical notice generation and
verification do not replace human review. Production signing and publication
remain blocked until an operator records approval of both the project license
and all applicable third-party license, notice, patent, and attribution terms.

## 3. Research Basis

DarwinRouter adapts proven ideas from three open-source agent systems while retaining routing as its core product identity.

### 3.1 Hermes Agent

Hermes distinguishes durable factual memory from reusable procedural skills, loads skills progressively, separates stable prompt material from volatile session state, and permits auxiliary models for bounded tasks. DarwinRouter adopts those separations, automatic skill drafting, tiered prompt assembly, replaceable context and memory engines, and strict credential hygiene.

Automatic skill changes are allowed only within an explicit scope. Every activated version must retain provenance, validation evidence, its predecessor, and a rollback path. Messaging gateways, voice, and broad assistant features are deferred.

Sources: [Hermes MIT license](https://github.com/NousResearch/hermes-agent/blob/main/LICENSE), [Hermes documentation](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/index.mdx), [configuration and context engine](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/configuration.md), and [prompt assembly](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/developer-guide/prompt-assembly.md).

### 3.2 Pi Agent

Pi demonstrates a small provider-neutral loop driven by typed streaming events, append-only sessions, resumable branches, safe compaction boundaries, steering, and extension hooks. DarwinRouter adopts a compact core loop, event-first integration, paired tool-call/result preservation, structured compaction records, and cross-provider conformance testing.

Presentation remains outside the runtime. CLI, HTTP, and the first-class Web UI
consume the same application service and typed event stream through adapters.

Sources: [Pi monorepo](https://github.com/badlogic/pi-mono), [agent loop](https://github.com/badlogic/pi-mono/blob/main/packages/agent/src/agent-loop.ts), [compaction](https://github.com/badlogic/pi-mono/blob/main/packages/coding-agent/docs/compaction.md), and [extension lifecycle](https://github.com/badlogic/pi-mono/blob/main/packages/coding-agent/docs/extensions.md).

### 3.3 Gas Town

Gas Town separates coordination from implementation work, gives work durable identities and dependencies, re-derives health from observable state, supervises worker lifecycles, and gates completion through validation. DarwinRouter adopts durable work units, leases, heartbeats, orphan recovery, stall detection, idempotent reassignment, acceptance gates, and OpenTelemetry-compatible observability.

The MVP adapts these ideas to an in-process runtime: inference and declared read-only work may run concurrently, but side-effecting operations require an exclusive resource lease and applicable approval. Stronger isolation follows after 1.0.

Sources: [Gas Town overview](https://github.com/gastownhall/gastown/blob/main/README.md), [architecture](https://github.com/gastownhall/gastown/blob/main/docs/design/architecture.md), and [lifecycle supervision](https://github.com/gastownhall/gastown/blob/main/docs/design/polecat-lifecycle-patrol.md).

## 4. Operating Modes

| Mode | Local execution | Cloud execution | Required behavior |
| --- | --- | --- | --- |
| `local_only` | Primary and fallback | Prohibited | Enforce egress denial; queue or reject when local capacity is unavailable. |
| `cloud_only` | Disabled | Primary and fallback | Operate without local inference hardware. |
| `hybrid` | Preferred when eligible | Permitted by policy | Optimize quality, privacy, latency, resource pressure, and cost. |

Mode changes apply to new tasks without daemon downtime. An active task retains the policy snapshot captured at admission unless an operator cancels and resubmits it.

## 5. System Architecture

```text
+------------- CLI / SDK / HTTP / Web UI ----------+
|                                                   |
|                 Application Service               |
|        admission, sessions, tasks, feedback       |
+-------------------------+-------------------------+
                          |
                 +--------v---------+
                 | Agent Runtime    |
                 | events + tools   |
                 +---+----------+---+
                     |          |
          +----------v--+    +--v----------------+
          | Router      |    | Worker Supervisor |
          | constraints |    | leases/heartbeats |
          | + fitness   |    | acceptance gates  |
          +------+------+
                 |
      +----------+-----------+
      |                      |
+-----v------+        +------v-----+
| Local      |        | Cloud      |
| Providers  |        | Providers  |
+-----+------+        +------+-----+
      |                      |
      +----------+-----------+
                 |
     +-----------v----------------------+
     | SQLite/WAL + Metrics + Traces    |
     | events, fitness, memory, skills  |
     +----------------------------------+
```

### 5.1 Package Boundaries

The Go module uses focused packages with narrow interfaces:

- `runtime`: typed loop, event delivery, cancellation, budgets, steering, and follow-up messages.
- `routing`: eligibility constraints, normalized ranking, exploration, fallback selection, and explanations.
- `providers`: provider-neutral request, stream, usage, cancellation, error, and model contracts.
- `tools`: schemas, registry, permissions, execution, effect classification, and normalized results.
- `sessions`: append-only history, branches, replay, checkpoints, idempotency, and compaction.
- `memory`: facts, retrieval, provenance, confidence, expiry, correction, export, and deletion.
- `skills`: discovery, selection, execution, drafting, versioning, validation, activation, and rollback.
- `workers`: bounded delegation, leases, heartbeats, recovery, and single-writer enforcement.
- `evaluation`: validators, evidence aggregation, user feedback, optional judging, and fitness updates.
- `resources`: CPU, RAM, swap, thermal pressure, unified memory/VRAM, and concurrency budgets.
- `policy`: egress control, secrets, redaction, approvals, and audit decisions.
- `telemetry`: migrations, repositories, metrics, traces, retention, and export.
- `workboard`: durable cards, dependencies, ordering, leases, acceptance state,
  and idempotent board operations shared by agents and operators.
- `webui`: embedded browser assets and a thin client over authenticated native
  APIs and runtime event streams; it owns no authoritative execution state.
- `api`, `cli`, and `daemon`: thin adapters over the application service.

No hand-written source file may exceed 1,000 lines. CI enforces the limit, excluding generated code and vendored dependencies. Packages must be split before the limit is reached rather than waived for convenience.

## 6. Public Interfaces

### 6.1 Go SDK

The versioned SDK exposes stable interfaces for:

- `Provider`: model discovery, health, streaming generation, cancellation, usage, and normalized failures.
- `Tool`: JSON-schema declaration, policy metadata, execution, and effect evidence.
- `ContextEngine`: context assembly, token estimation, compaction preparation, and compaction.
- `MemoryStore`: query, write, correct, expire, export, and delete facts.
- `SkillStore`: discover, load, draft, validate, activate, version, and roll back skills.
- `Evaluator`: accept evidence and return a typed outcome with confidence and provenance.
- `ResourceProfiler`: snapshot capacity and pressure and recommend safe execution budgets.
- `EventSink`: receive task-locally ordered, versioned runtime events; separate
  concurrent operations do not imply a global event order.

Interfaces accept `context.Context`; implementations must honor cancellation. Public records include schema versions and reject unknown incompatible major versions.

Current implementation note: the version-one Go SDK accepts a replaceable
`ResourceProfiler` and exposes a read-only `ResourcePlan` operation. Planning
uses configured hard limits and the same live in-process budget as execution to
recommend local execution, queueing, cloud offload, or rejection without
reserving capacity or invoking a provider. Its result is an instantaneous,
conservative recommendation; execution must remeasure and reserve atomically.
The SDK also accepts a process-local `EventSink` that receives detached,
redacted runtime events only after durable commit across root, worker, and
delegated-child task journals. Delivery is synchronous and live-only; explicit
event replay remains the recovery mechanism after restart or uncertain receipt.
Provider connections distinguish discovery, health, auxiliary, and selected
execution purposes. The selected execution adapter and any owned Codex process
must be constructed only after its task and turn starts are durably committed
and accepted; pre-route discovery and managed-residency maintenance remain
bounded control-plane preflight rather than task execution.

Configured event delivery serializes commit plus callback per task while
allowing unrelated task identities to remain concurrent. Submission recovery
delivers only runtime events newly committed while closing a provable
interrupted-model or interrupted-delegation history; terminal projection and
pre-start configuration retirement emit nothing. Delivery is live-only and a
failure never rolls back or authorizes recovery replay. A failed callback may
leave a checkpoint gap because each successful recovery terminalizes its task;
consumers must use explicit journal reads rather than expect a second append or
automatic redelivery. Current credential screening occurs before recovery
commit; a delegation batch that would require
changing its already-proven child-result provenance remains fenced rather than
persisting or emitting the synthesized parent events. Sink callbacks must not
re-enter the same service synchronously.

Schema 33 adds a database-wide committed-runtime-event ledger for durable SDK
catch-up. Every ordinary, interrupted-model, interrupted-delegation, and orphan
worker append receives one global insertion position in the same SQLite
transaction as its canonical task event and state projection. Reads freeze a
high-water mark, validate the complete ledger and task histories, and return a
canonical cursor anchored to the exact event IDs at both the consumed and
high-water positions. Consumers process pages at least once, deduplicate by
event ID, and persist a cursor only after the complete page succeeds. A live
`EventSink` callback is deliberately not convertible to a global checkpoint:
callbacks for unrelated tasks may complete out of order. Task-local gaps use
bounded session replay; database-wide recovery consumes sequential ledger
pages. The initial surface is trusted, read-only Go SDK access only—there is no
automatic replay, consumer-offset table, HTTP/CLI endpoint, or global SSE
stream—and event bodies remain sensitive application data.

Schema-33 event admission must also prove that the event can fit by itself in
the public maximum-size page using worst-case cursor/position overhead. An
unreadable event is rejected in the same transaction as its task append, so it
cannot strand every later global consumer. Migration never drops, truncates,
or silently skips an incompatible legacy row: an oversized pre-schema-33 event
fails the upgrade atomically and leaves the prior schema available for explicit
backup restoration or operator repair.

The SDK must expose explicit session-summary drafting, bounded inspection and
listing, operator review/history, and approved-summary continuation through the
same application service as the CLI/API. Current implementation and limits are
documented in [SDK session summaries](docs/sdk-session-summaries.md); this does
not fulfill automatic compaction or semantic validation requirements.

### 6.2 HTTP API

DarwinRouter exposes:

- An OpenAI-compatible streaming chat/completion endpoint.
- An authenticated OpenAI-shaped configured-model catalog that performs no
  provider discovery or inference and exposes no endpoint or credential data.
- A separate versioned Darwin-native configured-model metadata catalog across
  CLI, Go SDK and authenticated HTTP. It exposes redacted route declarations,
  not provider health, discovered availability, current capacity or execution authority.
- Native task submission, cancellation, status, and Server-Sent Events endpoints.
- Bounded metadata-only task discovery for SDK, CLI, interactive resume, and
  authenticated HTTP clients without loading conversation content.
- Route-explanation and model-health endpoints.
- Metadata-only route inspection through CLI, Go SDK, and authenticated HTTP,
  validated from the immutable initial `route.selected` boundary.
- Session, branch, and resume endpoints.
- Feedback and evaluation endpoints.
- Memory and skill inspection/management endpoints.
- Health, readiness, and metrics endpoints.
- Workboard endpoints for bounded card discovery, creation, revision, movement,
  dependency management, lease/heartbeat observation, and acceptance evidence.

Mutating native endpoints accept idempotency keys. Authentication is required unless the daemon is explicitly bound to a protected local transport.

### 6.3 CLI

The `darwin` CLI supports:

- Daemon start, stop, status, and diagnostics.
- Interactive, headless, and JSON-event task modes.
- Task cancellation, steering, feedback, and session resume.
- Provider, model, resource, route, and queue inspection.
- Configuration validation and redacted display.
- Memory and skill inspection, export, correction, deletion, activation, and rollback.
- Approval review for model disabling, pruning suggestions, policy changes, and destructive tools.

Configured memory commands must use the same scoped, credential-redacted
application service as SDK/HTTP management. Explicit raw database access must
remain distinguishable and cannot be combined with configured scope. Input
framing must reject ambiguous fact JSON; revisions remain required for correction
and deletion. Reads must not initialize storage or update last-use metadata.
See [operator memory management](docs/memory-management.md).

Interactive chat must deliver provisional assistant text while providers are
streaming, alongside ordered committed lifecycle metadata. Known credentials
must remain redacted across fragments, terminal controls must not cross chunk
boundaries, and rendered model lines must be distinguished from runtime status.
Only successful task completion advances conversation/feedback state; failed
partial output must not silently become the next task's history. Delivery
failure must cancel/join work without rewriting already committed events.

### 6.4 Web UI and Integrated Kanban

The daemon serves a responsive Web UI that provides a familiar conversational
surface without creating a second runtime. Operators can create and resume
chats, select configured models or automatic routing, stream provisional text
and committed lifecycle events, cancel or steer work, review tool approvals,
submit feedback, inspect route explanations, and see provider/resource health.
Conversation history is reconstructed from the same durable session records used
by the CLI, SDK, and HTTP API. A browser refresh or daemon restart must not
silently convert partial output into committed history.

The same Web UI includes native DarwinRouter Kanban boards for larger and
long-running work; they are not proxies for Linear or another external service.
Multiple boards are supported. At a minimum each board provides backlog, ready,
in-progress, blocked, review, done, and canceled states; configurable views may
group or hide states without changing their durable meaning. Cards carry a
stable ID, title, bounded Markdown description, priority, labels, dependency
DAG, parent/child relationships, assignee/worker identity, lifecycle timestamps,
WIP/attempt/time/token/cost budgets, current claim and lease, linked task/session/
attempt IDs, checkpoints, block reason, immutable-per-attempt acceptance
criteria, evidence, and a monotonic revision. The UI supports filtered board and
list views, card detail, dependencies and blockers, activity/checkpoint views,
accessible keyboard movement, and optimistic updates with server-side compare-
and-swap conflict handling.

DarwinRouter may discover eligible cards, decompose work within configured
depth/fan-out budgets, create and link child cards, atomically claim a ready card
whose dependencies are satisfied, heartbeat leases, append checkpoints and
progress evidence, request review, and transition accepted work through an
agent-facing tool contract. One active claim/attempt is permitted per card.
These operations are not hidden planner memory: all state is transactional,
inspectable, replayable, and attributable to an actor, model, reason, and
evidence. Agent writes must pass ordinary allow/deny/ask policy, inherit parent
denials, use idempotency keys, and hold the board's single-writer lease for the
affected card. A worker cannot weaken active criteria or policy, or mark its own
candidate accepted. Candidate completion enters review; done requires durable
configured acceptance, using deterministic evidence first and explicit operator
review for subjective work. An LLM audit remains advisory unless objective
evidence independently authorizes acceptance. WIP limits, attempt budgets,
bounded retries, and stall/attention states prevent runaway work. Lease expiry
makes work recoverable but does not prove execution stopped or authorize replay
of confirmed or uncertain side effects.

Agent decomposition policy is versioned and host-derived. Configuration version
1 defaults to a maximum hierarchy depth of 4, counting a top-level card as depth
1, and at most 8 direct children per parent. Both values are configurable from 1
through the hard domain bound of 64. Dependency fan-out is a separate
graph constraint and does not consume the direct-child allowance. The model
cannot supply limits, configuration identity, policy identity, or admission
identity in tool arguments. The application binds the effective configuration
digest and exact limits before dispatch, and the store re-evaluates depth and
direct-child count inside the serialized mutation before allocating a card or
emitting an event.

Every admitted model-authored hierarchy mutation records an immutable admission
and a redaction-safe board-event projection containing its configuration and
policy digests, effective limits, resulting depth, and direct-child count. Exact
idempotent replay remains bound to the original configuration digest. Reusing a
key after policy drift conflicts; the caller must inspect current state and issue
a fresh operation under the new policy. Delegated and Workboard execution
children receive no board-write tool authority. If a future child backend is
allowed to decompose, its host policy must be equal to or component-wise stricter
than both the current host policy and its parent admission.

Trusted deterministic validators are selected by immutable, versioned identity
and receive an owned copy of the exact frozen candidate and criterion binding.
The host constructs their evidence records; validator callbacks and model
review output cannot choose evidence source, actor, criterion, or authority.
Unknown required validators, callback panic, cancellation, or invalid evidence
references fail closed. Stock validator identities cannot be replaced by
extension code without a new identity.

Before any unattended card execution constructs a provider request or invokes a
tool, the scheduler must transactionally reserve a global and per-board WIP slot
and the card's remaining time, token, and cost allowance against the exact card
revision, execution profile, model/provider identity, task/session identity, and
configuration digest. The first durable runtime event and card claim consume
that reservation atomically. A reservation released with proof before runtime
start is uncharged; after runtime start, success, failure, and cancellation all
remain charged. Known provider usage and observed duration are recorded, while
missing, malformed, or ambiguous measurements conservatively consume the full
reserved allowance. A terminal runtime event alone does not release Workboard
WIP: candidate submission or another proof-bearing attempt finalization must do
so in the same transaction. Stale heartbeats, lease expiry, daemon restart, and
attention state never refund a reservation. Automatic acceptance-review calls
reserve and consume the same card budget before dispatch, including failed or
malformed reviews. These records are append-only, replay-validated, and are the
authoritative source for the integrated Web UI's used, reserved, remaining, and
attention-required budget presentation.

Long-running cards may span multiple runtime tasks and sessions. On restart the
supervisor re-derives board state from the append-only work log, task journals,
claims, leases, checkpoints, and runtime observations rather than trusting
private in-memory state. Dependency cycles are rejected. Accepted completion
atomically unlocks eligible successors; parent progress is derived rather than
cached as independent truth. Operators can pause, cancel, steer, reprioritize,
or revise future acceptance criteria at safe boundaries, but those changes do
not expand an active worker's permissions.

Candidate commitment and acceptance decision are separate durable boundaries.
After restart, the scheduler must page through already-committed Review
candidates and re-drive only the criterion decision from their stored evidence;
it must never reconstruct the task or redispatch the worker or advisory reviewer.
The scan is bounded and fair so subjective candidates awaiting user feedback do
not starve later objective candidates. A deterministic rejection returned to
Ready is not eligible for another worker attempt in the same reconciliation
pass.

The Web UI is bound to loopback by default and uses a same-origin browser
session/BFF boundary over the daemon's authenticated application service. Host
bootstrap secrets never enter JavaScript, URLs, local storage, or IndexedDB.
Cookie-backed sessions use HttpOnly and SameSite protections, Secure when TLS is
used, explicit expiry/logout, strict Host/Origin checks, and CSRF tokens on
mutations; existing bearer-token API semantics remain available to non-browser
clients and are not weakened for EventSource convenience. Remote access requires
explicit configuration and TLS at the deployment boundary. Request and output
bodies, rates, and concurrency are bounded. Content Security Policy uses self-
only assets with no object/frame execution. Prompts, tool output, Markdown,
links, attachments, route details, and model-produced HTML are untrusted and
rendered with raw HTML disabled, without script execution or implicit external
fetches. Browser storage must not contain credentials, capability/approval
tokens, or authoritative task state. Fully local mode vendors all UI assets and
applies the same zero-egress transport policy; no CDN, font, analytics, or
service-worker escape is permitted.

The accepted implementation boundary, operation map, streaming semantics,
workboard state ownership, and concrete browser controls are recorded in
[ADR 0001](docs/adr/0001-web-ui-workboard-boundary.md). Its versioned Go types,
JSON Schema, and reusable fixtures live under `webui/`. The first implementation
serves a versioned embedded shell through a terminal-approved, process-local
browser session boundary with a narrow public bootstrap and bounded CSRF grants;
it now includes bounded browser-safe chat/session projections, paginated
history, provisional post-redaction text, and durable reconnectable lifecycle
streaming. The implemented DAR-79 boundary adds direct in-process browser
facades for submit/resume, steering, task and queued-submission cancellation,
subjective feedback, and approval decisions. Browser mutations are protected by
same-origin session/CSRF policy and a request-bound durable operation journal;
bounded control, feedback, approval, recent-operation, and submission-status
reads support refresh reconciliation without exposing native bearer tokens or
raw runtime records. The implemented DAR-80 inspector adds bounded,
authenticated GET projections for the configured model catalog, per-task route
candidates and usage, normalized paired tool lifecycles, redacted audit
provenance, health, and host resources. The UI distinguishes a projection that
is unavailable from an optional measurement that is unknown, keeps routed
execution accounting separate from auxiliary classifier, summarizer,
orchestrator-audit, and optional-judge accounting, and never exposes raw
prompts, tool arguments, tool results, or provider responses. These views are
observational: they cannot change routing policy, configured models, approvals,
or task execution. DAR-81 implements the schema-35 workboard storage foundation
inside the primary SQLite/WAL database: normalized bounded records for boards,
canonical columns, cards, dependency edges, events, attempts, claims,
heartbeats and checkpoints, candidates, evidence, acceptance, recovery proofs, and scoped
idempotency receipts. It does not expose workboard mutations. The command APIs,
agent tools, and Kanban feature views are delivered through DAR-82, DAR-83, and
DAR-84. DAR-82 now provides authority-gated board domain/service
contracts and transactional repositories for board create/revise/list/read/
archive, redacted event pages, and card create/revise/move/reorder/dependency
mutations. It atomically maintains canonical columns, attributed immutable
events, optimistic revision fences, exact committed replay, graph/layout
revisions, aggregate transaction-byte limits, and bounded authenticated
pagination. Schema 36 adds normalized card identity to immutable events and
transactionally preserves schema-35 board events with a `NULL` card ID during
upgrade. Native JSON handlers and browser-session/CSRF BFF contracts now cover
live list/create/read/operations, browser mutation reconciliation, and
reconnectable workboard SSE through the daemon. The durable command layer now
includes claim/heartbeat/recovery, criteria revision, checkpoints, candidate
submission, evidence-based acceptance/rejection, pause/cancel requests,
block/unblock, lifecycle projections, and stale-claim attention. Operator-safe
actions are composed through browser/native adapters, while worker actions use
a fixed-authority internal dispatcher bound atomically to runtime task/session
identity. Independently derived stop proof gates automatic recovery and cancel
finalization; proof-ineligible attention claims remain visible without starving
later recoverable work. Schema 37 persists a non-secret workspace identity and
schema 38 preserves initiating and recovery-session attribution for exact
browser reconciliation, including legacy pending operations. Schema 39 adds a
durable cooperative pause lifecycle whose worker acknowledgement is accepted
only at an exact-fenced safe boundary. Schema 40 adds immutable, transactionally
derived recovery-to-replacement lineage. It binds the exact recovery and both
attempt/claim identities, clears the predecessor assignment when returning the
card to Ready, exposes lineage through bounded attempt projections, and never
copies predecessor task/session/output or grants replay authority. DAR-85 now
also has a schema-41 atomic runtime-start/claim boundary. A trusted host can
commit the runtime's actual redacted first event and all initial runtime and
Kanban ownership projections together, with an immutable marker that prevents
later adoption of independently committed halves. Exact retries validate the
complete progressed journal, event index, timing, worker identity, claim, and
marker. A capacity-only supervisor slot and host-frozen runtime identity are now
composed by the workboard worker runner. A trusted, one-use binding supplies the
exact already-open configured SQLite store and freezes task, session, optional
parent, and worker identities. An assigned card requires that exact assignee as
its worker; an unassigned card receives a fresh host-generated identity. Both
may execute as top-level tasks without a parent. On the runtime's first append,
the host atomically
commits the actual redacted `task.started` event and all initial runtime and
Kanban ownership projections, then starts the claim heartbeat before admission
returns. The runner emits no synthetic outer task or resource lease; binding
without a runtime start creates no task, attempt, or claim. The marker and
complete progressed journal continue to prevent adoption of independently
committed or corrupted halves.

The bounded per-board scheduling cycle is dependency-injected and composed by
the stock daemon when explicitly enabled. Its integrated runtime path accepts
only an explicit model and fails closed for automatic routing, managed
residency, worker delegation, provider fallback, and provider-overflow
compaction; the configured scheduler supplies a separate independent audit.
Schema 42 provides repository-verified transactional execution
admissions and settlements with exact runtime/configuration identity, bounded
WIP, conservative unknown-usage accounting, successful-overrun rejection, and
replay/tamper tests. Provider-side hard token ceilings, independent local
reviewer dispatch, stock-daemon composition, and crash/lease/acceptance
qualification are enforced by the enabled unattended path. DAR-83 owns the
integrated visual Kanban inside this same Web UI; its first read-only slice now
renders the seven canonical lanes with bounded card, dependency, attempt, and
checkpoint previews, bounded board/card filters, a canonical list alternative,
and reconnect-safe SSE invalidation/full refetch. Later slices add
revision-fenced board/card creation and revision, confirmed archive controls,
strict receipt validation, and browser-journal reconciliation that never
replays an ambiguous side effect. The current positioning slice adds native
keyboard controls for adjacent same-lane reordering and legal Backlog/Ready
movement. It fails closed under filters or partial pagination, freezes board,
layout, and card revisions, and validates exact receipts plus immutable events.
The dependency slice adds a selected-card editor that offers only explicit
same-board choices from a complete, unfiltered snapshot and freezes card and
graph revisions for one add/remove operation. Dependency receipts are bound to
the exact card, action, operation, and immutable event before authoritative
refresh. The current lifecycle-control slice adds explicit, confirmed pause and
cancellation requests for claimed in-progress or blocked cards, persistent
request badges, exact successor-card/event validation, and wording that does
not misrepresent a request as completed worker stop. Proof-gated cancellation
finalization remains open. The current acceptance slice adds an evidence-first
candidate review dialog inside the Kanban, renders criterion provenance and
keeps model-audit evidence advisory, then permits only evidence-eligible accept
or reject decisions with exact card/attempt/candidate/digest fences. Acceptance
fanout receipts validate the primary decision plus every bounded successor
event before authoritative refresh. Authoritative card refetches now carry a
bounded, validated keyboard-focus anchor across superseding same-board
refreshes. The replacement DOM restores the exact enabled control, then the
card toggle, then the stable Refresh control, without retaining stale nodes or
stealing focus after the operator moves elsewhere. Ordinary editors and the
candidate-review dialog are reparented outside an inert application background,
exclude overlapping modal state, and restore focus to a live opener or Refresh.
Running workers now consume durable cancellation requests after a heartbeat,
cancel and join the supervisor-owned callback, persist runtime cancellation,
and preserve the workboard claim for independent finalization. Pause
consumption, trusted same-daemon stop acknowledgement, and broader browser
qualification remain open. Same-board authoritative refreshes now preserve one
bounded selected/expanded-card anchor, reload fresh details when the card
reappears, and discard the anchor on board changes or confirmed deletion without
moving focus or weakening modal isolation.
DAR-84 now provides an explicitly enabled, local-root-only read slice through
the provider-neutral `workboard_list` and `workboard_read` tools. They use
bounded projections, closed schemas, durable read events, a global list scope,
and exact per-board reader scopes shared with writers; they are excluded from
child catalogs. An additional
`tools.workboard_write_enabled` gate, which requires reads, exposes approval-
backed board/card creation, board metadata revision/archive, rich card updates,
backlog/ready transitions, same-lane card reordering, dependency add/remove, and
pause/cancellation request
tools. Mutations use closed schemas, caller idempotency
keys, trusted model attribution, configured allow/deny/ask policy, and exact
argument-derived `workboard:<board_id>` writer leases; board creation uses the
global `workboards` writer scope. Only proven pre-commit conflicts are returned
as recoverable no-effect results. Invalid durable replay receipts and ambiguous
storage acknowledgements remain uncertain and are never automatically retried.
Mutation events use a deterministic task-bound model actor while the task
journal retains selected provider/model provenance. Child workers cannot
inherit these tools, including through an unadvertised direct tool call against
a borrowed root registry. Model-authored card placement is additionally bound
to the versioned effective decomposition policy and configuration digest; those
host values are absent from the closed tool schema. Interactive terminal chat
renders an exact ASCII-safe,
credential-screened preview for each supported workboard proposal before an
operator can approve its one-use authority. DAR-109 adds root-only criteria and
candidate-decision proposal tools using the runtime event journal plus the
digest-bound approval ledger as one two-party record. The durable store
revalidates the exact canonical arguments, task/turn/tool identity,
model/provider provenance, operator decision, revisions, and evidence/policy
digests before applying an operator-attributed mutation. The Web UI reconstructs
the same bounded proposal for inspection and fails closed when that proof cannot
be reproduced. A spent approval is never redispatched; lost acknowledgements
are reconciled read-only from the exact operation receipt. The model therefore
cannot rewrite its own acceptance gate or accept its own result merely because
an outer tool approval was consumed.

The DAR-79 journal lives in the primary SQLite/WAL database under schema 34 so
backup, restore, and migration use one state store. Operations are bound to the
authenticated browser-session subject and transition from `pending` to the
terminal `committed` or `rejected` state. Exact retries replay the recorded
result; reuse with a different request conflicts. Only definitive, sanitized
domain failures become `rejected`; an interruption whose effect is ambiguous
stays `pending` for explicit reconciliation. Pending rows are never silently
pruned. Terminal rows have bounded age/count retention and the journal has a
hard capacity limit. Browser sessions themselves remain process-local and are
revoked by daemon restart, so a newly authenticated session does not inherit a
prior session's authority even though its durable audit records survive.

Subjective browser feedback is an additive immutable revision chain, separate
from objective validator/tool evidence and advisory model-audit evidence.
Revision conflicts and corrupt history fail closed; revising a user judgment
does not overwrite either of the other evidence classes.

Chat and board mutations use versioned native endpoints with idempotency keys.
Streaming uses resumable Server-Sent Events with event IDs and bounded catch-up;
the client must reconcile from durable state after gaps rather than infer
success or restart work merely because the browser reconnects. Chat history uses
a bounded, paginated, redacted presentation projection distinct from metadata-
only task discovery and raw event/tool payloads. Board transitions, dependency
edits, claims, lease changes, approval decisions, and acceptance results emit
typed audit events and OpenTelemetry-compatible metrics without sensitive card
or chat content.

## 7. Configuration

Configuration precedence is:

1. CLI flags.
2. Environment variables.
3. Project configuration.
4. User configuration.
5. Built-in safe defaults.

Credentials are permitted only through environment variables or a configured secret-store adapter. Resolved configuration output must redact secrets and sensitive fields.

```yaml
version: 1
mode: hybrid

daemon:
  listen: "127.0.0.1:7788"

web_ui:
  enabled: true
  path_prefix: "/app"
  allowed_origins: ["http://127.0.0.1:7788"]
  browser_session_ttl: 8h

workboard:
  enabled: true  # Required in configuration v1 while API/Web UI routes are mounted.
  decomposition:
    version: 1
    max_depth: 4                 # Top-level card is depth 1; hard maximum is 64.
    max_children_per_parent: 8   # Direct children only; hard maximum is 64.
  scheduler:
    enabled: false  # Stock daemon rejects true until supervised execution is wired.
    interval: 5s
    max_active_claims: 3
    card_scan_limit: 10000
    worker_model: local-worker
    acceptance_judge:
      enabled: false
      reviewer_model: local-reviewer
      max_cost: 0.01
      max_input_tokens: 4096
      max_output_tokens: 4096
      timeout: 30s

hardware:
  auto_profile: true
  max_ram_usage_pct: 80
  max_vram_usage_pct: 85
  max_concurrent_local_models: auto
  local_pressure_policy: reject  # reject | wait
  local_queue_timeout: 30s       # Admission wait only, not execution timeout

workers:
  max_in_process: 3
  heartbeat_interval: 5s
  lease_timeout: 30s
  side_effect_policy: single_writer

providers:
  - id: local-primary
    kind: ollama
    endpoint: "http://127.0.0.1:11434"
  - id: cloud-primary
    kind: openai_compatible
    endpoint: "${CLOUD_LLM_BASE_URL}"
    api_key_env: CLOUD_LLM_API_KEY

models:
  - id: local-worker
    provider: local-primary
    model: "local-worker-model-id"
    locality: local
    capabilities: [chat, summarize, classify]
    context_tokens: 8192
    estimated_cost: 0
  - id: local-reviewer
    provider: local-primary
    model: "independent-local-reviewer-model-id"
    locality: local
    capabilities: [chat, audit]
    context_tokens: 8192
    estimated_cost: 0
  - id: cloud-reasoning
    provider: cloud-primary
    model: "cloud-model-id"
    locality: cloud
    capabilities: [chat, reasoning, code, tools]

routing:
  exploration_rate: 0.05
  minimum_samples: 20
  decay_half_life: 30d
  classifier:
    enabled: false
    model_id: ""
    max_cost: 0
    max_input_tokens: 4096
    max_output_tokens: 256
    timeout: 30s
  decay_overrides:
    - domain: coding
      profile: local
      half_life: 7d
  weights:
    quality: 0.35
    schema_compliance: 0.15
    reliability: 0.20
    latency: 0.10
    cost: 0.10
    recency: 0.05
    uncertainty: 0.05

skills:
  enabled: true
  auto_draft: true
  auto_activate_after_validation: true
  rollback_on_regression: true
  generation_budget:
    enabled: false
    window: 24h
    max_cost: 0
    max_attempts: 10
    max_in_flight: 1
    cooldown: 1h

memory:
  enabled: true
  local_only: true

evaluation:
  llm_judge_enabled: true
  evidence_precedence: [deterministic, tool_result, user_feedback, llm_judge]

security:
  local_only_egress: deny
  default_tool_policy: ask
  redact_env: [DARWIN_PROJECT_SECRET]

telemetry:
  database: "${DARWIN_DATA_DIR}/darwin.db"
  opentelemetry_enabled: false
```

Durations, percentages, weights, paths, provider references, capability names, and mode-specific contradictions must be validated before the daemon becomes ready.

When unattended scheduling is enabled, both scheduler model aliases are
mandatory and resolve directly; adaptive routing cannot choose either role. The
worker must be available in the configured deployment mode and have positive
context capacity plus an explicit cost estimate. The reviewer must additionally
be local, remain within its bounded `100ms`–`5m` timeout and `max_cost`, and have
a different provider/model identity from the worker. The global LLM-judge gate
must remain enabled. The stock daemon composes these fail-closed rules with the
qualified independent-review path, readiness health, and joined scheduler
shutdown; an enabled scheduler is never silently ignored.

## 8. Adaptive Routing

### 8.1 Task Classification

Tasks receive one or more domains, required capabilities, privacy classification, context estimate, tool requirements, latency objective, budget, and execution-risk classification. Deterministic rules run first. An opt-in auxiliary classifier may fill only omitted domain and capability fields when deterministic metadata is genuinely ambiguous. Explicit domain, capability, privacy, validation, delegated-worker, runtime-host, and fallback intent bypasses it. Merely enabling a local tool surface does not bypass a policy-compatible local classifier; the classifier itself receives no tools.

The classifier makes at most one provider-neutral call with no tools,
delegation, retry, memory, skill, or historical-session context. Only the fresh,
credential-redacted request is admitted under the normal egress, locality,
resource, token, time, and cost policies. Its closed JSON decision is validated,
persisted before candidate dispatch, and reused after restart; an uncertain
started attempt is never redispatched. A classifier decision may return no more
than 16 capability constraints. The accepted decision may add constraints
but cannot remove caller constraints. Classifier usage is accounted separately,
and classifier output is never evaluation evidence, fitness evidence, or proof
of task success. Malformed output, timeout, cancellation, provider failure, or
ambiguous persistence fails closed before routed-provider dispatch. A terminal
classifier call that cannot reach normal routing is closed through a minimal,
redacted, restart-safe task journal so its lifecycle and known usage remain
inspectable without claiming routed execution or fitness evidence.

### 8.2 Eligibility Filtering

Before ranking, DarwinRouter rejects candidates that violate any hard constraint:

- Deployment mode or privacy policy.
- Required capability or tool support.
- Context-window requirement.
- Provider or model health.
- Local resource capacity.
- Cost or latency budget.
- Credential availability.
- Egress or organizational policy.

An ineligible model cannot win through a high historical score. Explicit model
selection must not bypass local memory or concurrency admission: named-model,
automatic, and auxiliary execution share the service's resource budget. Unknown
local memory estimates fail closed. Reservations cover the entire admitted
execution and are released only after that execution returns, including failure
and cancellation cleanup. Independent-process coordination remains a separate
requirement from in-process reservations.

### 8.3 Fitness Ranking

Eligible models are ranked within their task domain and execution profile using normalized values for quality, schema compliance, reliability, latency, cost, recency, and uncertainty. Metrics are comparable only after normalization to documented units and ranges.

Fitness is maintained by model, provider, domain, and relevant execution profile. Recency decay limits stale evidence. Minimum-sample and uncertainty terms prevent overconfidence. Bounded exploration gives new or rarely used candidates a controlled opportunity without violating hard constraints.

### 8.4 Fallbacks

The router preselects an ordered fallback chain, preferring a different failure
domain before repeating one. Automatic execution may traverse at most 32 route
attempts. Every candidate is freshly checked for policy, health, resources and
remaining budget before dispatch; a candidate that becomes ineligible without
starting work may be skipped. Every executed predecessor must be a durably
failed, provider-declared retryable first turn with no output, steering or tool
proposal, and each successor records immediate retry lineage. Partial output,
validation failure, cancellation, persistence ambiguity, or any confirmed or
uncertain external side effect stops the chain. Explicit-model requests never
auto-fallback. Cross-provider conformance must exercise the production adapter
protocols, not merely relabel one provider fixture: a retryable Ollama failure
must be durably closed before an OpenAI-compatible fallback begins, and a
local-required request must make no discovery or inference request to that
cloud fallback.

Each task start records the selected route's operator-configured cost estimate.
Public native results expose the sum across admitted top-level fallback
attempts as `route_estimated_cost`; it is an admission estimate, not provider
billing, and excludes delegated workers and auxiliary audits. Token usage is
reported for a fallback chain only when every attempt has complete durable
usage evidence. Otherwise usage remains unavailable rather than presenting the
final route's tokens as a whole-request total.

### 8.5 Route Explanations

Every decision records:

- Policy and configuration snapshot identifiers.
- Eligible and rejected candidates with reason codes.
- Normalized score components and final ordering.
- Primary and fallback selections.
- Exploration decision, if any.
- Resource snapshot and health signals.

Explanations must not include secrets, raw prompts, or sensitive model output.

## 9. Evaluation and Evolution

### 9.1 Evidence Ladder

Outcome evidence is applied in this order:

1. Deterministic validators such as schema checks, compilation, tests, and explicit task predicates.
2. Tool outcomes and verified external receipts.
3. Explicit user feedback.
4. Optional LLM judging when objective evidence is unavailable.

Contradictory lower-priority evidence cannot override stronger evidence without an auditable policy decision. Model self-assessment alone is never success evidence.

Candidate-side evaluators cannot emit `user_feedback`. That evidence source is
reserved for an authenticated operator accept/reject action and is appended as
an immutable review record. A subjective-only candidate may therefore enter
Review with an empty, digest-bound evidence set; the first operator decision
creates evidence revision 1. The runtime must not invent placeholder evidence
or require an LLM judge merely to make a creative result reviewable.

### 9.1.1 Orchestrator audits and domain-sensitive feedback

The orchestrator must be able to audit model output and observed results automatically, using a bounded auxiliary review call where useful. Prefer an independent evaluator, but permit a separate same-model invocation when it is the configured coordinator. The audit receives task requirements, the candidate output, and available validation/tool evidence as untrusted data; it has no tool execution or permission-changing authority. It produces a validated structured assessment with findings, evidence references, confidence, and an abstain outcome. Persist the evaluator identity, rubric version, task domain, and audit cost separately from candidate performance.

- Deterministic checks identify empty/whitespace-only final answers when a textual answer is required, invalid required schemas, failed tests, and other mechanically verifiable failures. Intermediate empty assistant messages with tool calls are not empty-output failures.
- Nonblank text is not evidence of meaningful completion. The orchestrator should flag responses that merely repeat the request, promise future work, or omit required deliverables, citing the requirements and candidate output. These semantic findings remain advisory rather than fabricated deterministic failures; brevity alone is not a defect.
- Code tasks combine tests, compiler/linter results, and contract checks with orchestrator code review. A model review can identify suspected defects, but must not claim tests ran or passed without execution evidence. Passing tests alone does not establish complete correctness.
- Supply persisted tool and validation outcomes as separately referenced, bounded execution metadata with task/turn/attempt attribution, not only as conversational tool text. Preserve failure codes and explicit rejection outcomes; never silently drop failure evidence to fit the review budget. A recorded tool completion or syntax pass is not proof of successful tests.
- Creative and preference-heavy tasks give explicit user feedback greater influence than orchestrator taste judgments. Model-only subjective assessments remain low-confidence, bounded advisory signals; they must not disable models, activate skills, or outweigh later user feedback.
- Keep objective validity and subjective quality as separate dimensions. A user preference does not erase a failed test, and a test pass does not establish creative quality. Where no evidence supports an assessment, abstain rather than invent success.
- Later user feedback must be able to supersede prior subjective judge evidence through compensating, versioned records. Recompute the affected fitness contribution without double-counting the attempt; preserve original evidence and revision history. Corrections to objective evidence require explicit provenance.
- Audits inherit local-only/privacy constraints, use independent evaluator identity where possible, reserve explicit input plus output token ceilings before dispatch, enforce time/cost budgets, and do not recursively judge their own judgments. The input/output sum must fit the reviewer context and the owning work budget; reported provider usage is checked against the same reservation. A same-model accept or abstention creates no routing signal; a same-model rejection is capped as a low-confidence advisory warning. Failed, malformed, or missing audits never become positive evidence.

Acceptance tests must cover blank final answers versus tool-only intermediate turns, compiler/test evidence versus review opinion, creative-task user preference overriding a prior judge contribution, abstention, adversarial output attempting to influence the evaluator, revision replay/idempotency, and local-only audit egress denial.

### 9.2 Fitness Updates

Fitness updates are transactional, replay-safe, and linked to immutable evidence. Failed or canceled persistence cannot partially update routing state. Operator-approved corrections produce compensating records rather than rewriting history.

Apply recency decay independently to every current observation using its immutable source timestamp, then aggregate weighted domain/profile evidence. A correction retains the base observation time and contributes one current sample. Exact replay is idempotent; conflicting identities, correction forks, future timestamps, and invalid clocks fail closed. Stable source-time/identity ordering makes late arrival and backfill independent of database insertion order. Configuration provides a global half-life plus exact domain/profile overrides. Route explanations expose raw and effective samples, average decay contribution, and the effective observation window without sensitive content. See [fitness observation decay](docs/fitness-decay.md).

### 9.3 Pruning

Models crossing configurable failure or value thresholds become deprecation candidates. DarwinRouter explains the evidence and estimated resource savings, but disabling, uninstalling, or deleting a model requires operator approval.

### 9.4 Skill Learning

Repeated successful workflows may produce an automatic skill draft. A draft records source sessions, generalized steps, required tools, configuration, risks, and validation cases. Activation requires configured validation. Each revision retains prior versions and rolls back automatically when validation fails or post-activation outcomes materially regress.

Delayed controllers must bind activation to a durable operation ID and exact
candidate/revision. Retry after lost acknowledgement must recognize the original
receipt without repeating activation, including after a later rollback. The
Go host API supports operation-keyed activation and receipt inspection. Opt-in
Go-host learning additionally binds a named trusted validator into the learner
policy and saves a schema-26 activation intent before a later validation tick.
Receipt reconciliation resumes cursor progression without reactivation after
rollback. Go hosts can also explicitly start periodic deterministic regression
monitoring with automatic rollback via `StartSkillRegression`. The configured
daemon/SDK lifecycle can select registered trusted validators and jointly
supervise learning and durable regression checks. The stock binary ships the
narrow protected observed-tools provenance validator; broader semantic domain
validators remain required.
See [configured supervision](docs/configured-learning-supervision.md) and
[regression monitoring](docs/skill-regression-monitor.md).
See [activation operations](docs/skill-activation-operations.md).

Explicit regression operations must likewise retain deterministic passing-check
evidence and commit failed-check receipts atomically with rollback. Bind retries
to the exact activation revision and trusted validator identity without repeating
rollback or changing activation revisions merely to record a pass. Current Go
host APIs are described in [regression operations](docs/skill-regression-operations.md).
Opt-in Go-host durable scheduling now persists named policy-bound monitors,
cadence and exact pending operation identities in catalog schema 5. Failed-check
tombstones must fence late callbacks at receipt/rollback commit, while definite
check failures advance fairly without becoming successful validation evidence.
See [durable monitoring](docs/durable-skill-regression-monitor.md). Safe long-term
record retention remains open. Configuration selects named Go-host code, never
executable model output or arbitrary shell commands. Both saved policy bindings
must pass read-only preflight before either configured controller starts; shared
cancellation joins both on shutdown. New model-generated drafts carry their
verified source domain for progressive discovery without rewriting prior receipts.

Outcome-based regression requires trustworthy exposure evidence. The host now
records fresh skill-tier references (scope/name/version/digest) on TaskStarted
before dispatch, after context admission. Unknown legacy or redacted attribution
is not an unexposed sample. Do not infer use from model/user text or inherited
history, and do not equate context inclusion with semantic execution or causality.
Read-only task outcome inspection through CLI, SDK and HTTP captures one coherent
journal/current-evaluation snapshot, retaining negative and missing evidence and
keeping mechanical validity separate from quality. Bounded explicit task-set
comparison now reads all observations in one SQLite snapshot and reports advisory
Wilson-interval separation, using independent quality evidence and conservative
exclusions. Indexed automatic latest-window selection now fixes privacy and
selects recorded exposures before examining outcomes, with snapshot-wide session
exclusions. Trusted Go hosts can explicitly opt into one committed outcome-policy
adjudication per first candidate activation, with a receipt and rollback
atomically bound to its validated predecessor. The configured daemon and SDK can
now supervise that path. They inspect the exact catalog-owned candidate and a
fresh bounded comparison read-only; `waiting` creates no intent. Once both
cohorts are decision-ready, the prepared path atomically saves the intent and
exact selected report before final adjudication. Exact recovery uses that fixed
report and rechecks its source tasks, policy and activation without selecting a
replacement window. The named scan cursor, due time, pending check, exact
activation/predecessor pair, and stable operation binding are persisted before
evidence selection. Restart reconciles committed receipts without rediscovering
mutable activation state. The daemon starts the
supervisor before task dispatch, includes it in readiness, and joins it on
shutdown. Reports remain observational/advisory and never fabricate deterministic
evidence. Subjective creative/unknown policy requires operator-owned
`user_feedback`; judge-only evidence is never supervisor authority. SQLite source
checks are not atomic with catalog replacement. Causal/confounder controls and
repeated-look correction remain open.
See [skill outcome attribution](docs/skill-outcome-attribution.md).
See [outcome comparison and limitations](docs/skill-outcome-comparison.md).
See [automatic window selection](docs/skill-comparison-selection.md).
See [opt-in outcome-policy rollback](docs/skill-outcome-rollback.md).
See [configured outcome supervision](docs/configured-outcome-supervision.md).

Before dispatch, a learning selection must bind its grouping-rule identity, destination skill, configured model, policy version and exact source/evaluation digests. Generation verifies those bindings against the same coherent source snapshot used for its prompt. Changed evidence or policy invalidates the selection rather than silently substituting inputs. The durable selection ID is the single-use generation attempt ID; uncertain or failed attempts do not authorize automatic redispatch. Source selection does not establish semantic repetition or substitute for activation validation. Background learning additionally requires durable scan progress, explicit grouping rules and aggregate budget/cooldown controls.

The signed-in Sol coordinator may also perform bounded skill drafting through
the Codex app-server provider. This remains cloud inference: all source and scope
privacy constraints apply. Commit the generation reservation before CLI launch,
include the closed output schema in context admission, and expose no task tools.
Generated provenance is never accepted; the host derives it from recorded work.
See [Codex skill drafting](docs/codex-skill-generation.md).

The initial deterministic grouping rule, `observed_tools_v1`, recognizes repeated successful tool execution sequences within the same domain and execution profile. Derive the sequence from actual durable dispatch/completion events, preserving order and repetitions; never infer execution from supplied conversation history. Failed or uncertain tool trajectories do not qualify. Require at least two distinct sessions. Identical tool names are a drafting heuristic, not proof of equivalent arguments, tool implementations, or semantics. Text-only workflows need a separate validated grouping rule. Recheck the actual execution sequence alongside source extraction before planning and generation; a conversation snapshot digest alone does not cover all execution evidence.

Discovery must persist each observed page atomically with its scan checkpoint. Retries of the same revision and parameters return the original saved page, even when feedback changes afterward. Each epoch freezes membership using a durable insertion-sequence fence, not merely a maximum task ID; new tasks cannot indefinitely extend an active epoch. The next epoch starts from the beginning to revisit late feedback and lower-sorting arrivals. Page boundaries freeze neither source evidence across the epoch nor permission to generate. Corrupt history must stop advancement visibly rather than silently skip work.

Automatic generation must require an enabled aggregate budget before dispatch. Reserve estimated cost, rolling attempt capacity and an in-flight slot atomically with the single-use generation claim. Failed attempts remain charged; unresolved started attempts retain their in-flight slot regardless of age. Apply a per-skill-name cooldown in the configured scope. Cost estimates must use consistent units and are not proof of actual provider charges. Manual generation retains an explicit budget opt-in for compatibility; the background scheduler must not inherit an unlimited default. Budget-policy changes remain trusted operator configuration.

The daemon's learner is disabled by default and advances bounded durable phases.
Pin the exact selection and destination bucket in its cursor before inference;
restarts must not recompute a different attempt ID from changed feedback. Use
stable workflow-derived skill identities independent of scan epochs. Publish
generated proposals as inactive versions until separate validation authorizes
activation. Budget/cooldown pauses are expected policy states; unresolved claims,
stale sources and incompatible policy require visible operator attention rather
than silent reset. Provide read-only persisted state inspection even when
scheduling is disabled, metadata-only health and cancellation/join on shutdown.

Generated-version inspection begins with an exact, read-only publication-binding
lookup. Given a permitted skill key and immutable version ID, the store returns
the unique generation-attempt ID and canonical attempt digest only after the
catalog receipt, catalog metadata and stored version body pass structural and
integrity validation. Manual, legacy and unpublished versions have no such
binding and fail closed. The lookup creates, repairs, activates and rewrites
nothing, and its canonical digest implementation is shared with publication so
the read and write paths cannot drift.

This receipt is a consistency seam, not a production validation result or
tamper-proof attestation. It does not load telemetry, authenticate source events,
reconstruct workflow selection, invoke a validator or authorize activation;
candidate-authored validation cases remain untrusted. Coherent observed-tool
provenance validation, protected daemon/SDK validator wiring, and activation-bound
outcome supervision are now present. Durable scheduling records, a schema-47
structural lifecycle journal, and bounded no-action retention tombstones are
also implemented; causal attribution, repeated-look correction, and broader
production qualification remain open.

The stock `darwin_observed_tools_activation_v1` validator closes the next
provenance layer without claiming semantic correctness. It binds the callback's complete immutable version
to the publication receipt and canonical generation-attempt digest, then reads the
drafted attempt, content-addressed workflow selection, current accepted source
evaluations and durable tool lifecycle events in one read-only SQLite transaction.
It re-derives the exact `observed_tools_v1` group, ordered tool sequence, source
sessions, evaluation digests and privacy requirement. Readable stale evaluation,
judge-only, privacy, and required-tool mismatches return deterministic failed
evidence suitable for rollback. Failed or uncertain tools, malformed state,
concurrent database commits, cancellation, and publication/version revision fail
as operational validation errors and never authorize rollback.
The catalog and database are re-opened read-only; validation performs no provider,
tool, activation or persistence call, and candidate `validation_cases` remain inert
bytes.

Passing evidence is deterministic only for this bounded provenance/freshness
claim. The selection ID content-addresses its saved policy identity, but this
validator does not independently reconstruct the historical full configuration or
generation prompt digest, authenticate a tamper-capable database operator, prove
tool arguments/implementation versions or establish workflow usefulness. Required
tools must have been observed, but a draft may conservatively omit observed tools.
The daemon and versioned SDK now construct this identity as a protected,
settings-bound, lazy read-only callback before configured-learning preflight.
Configuration remains explicit opt-in; unknown IDs and host attempts to claim the
protected identity fail before listeners, database initialization, or supervisors.
Qualification covers generation, publication, activation, progressive reuse,
restart idempotence and deterministic stale-evidence rollback. Outcome
supervision now requires objective evidence for auditable domains and
authenticated user feedback for creative/unknown work; durable scheduling and
lifecycle-event qualification remain open.

## 10. Sessions, Context, and Memory

### 10.1 Durable Sessions

State transitions are appended durably before acknowledgement. Events carry task, session, turn, attempt, worker, route, and causation identifiers. Replay must reproduce externally visible state without repeating uncertain effects.

Sessions support cancellation, retries, resumable branches, steering messages, follow-ups, checkpoints, and idempotency keys.

Operators can discover durable task IDs through newest-first, insertion-fenced
metadata pages. Listing exposes only task/session identity, state, sequence and
start time, performs no inference or repair, and never substitutes for the exact
continuation-eligibility check on a selected task.

Active-task steering is durable user guidance, not a policy or permission change.
The initial HTTP surface accepts up to32 messages per task, each up to64KiB UTF-8,
with task-scoped idempotency keys. Acceptance means queued; a separate committed
`steering.applied` event means added to conversation context, not proven execution
or compliance. Apply only outside model streams and complete tool-call/result
batches. Completion must serialize with queue acceptance so accepted guidance
cannot disappear behind a successful terminal transition. Guidance never resets
iteration, output, context, privacy or tool limits, and pending guidance must not
be discarded by automatic fallback. `runtime.max_turns` bounds all task turns
(default8, allowed1–32), also capped by tool limits when tools are enabled.
Failed/canceled tasks may retain inspectable pending guidance. Line-oriented
`darwin chat` supports `/steer` during work, with committed application evidence.
The experimental Codex coordinator admits this guidance at completed model/tool
boundaries through checked native steering or a new turn in the same thread;
it does not interrupt an active model stream or replay tool execution. See
[Codex steering](docs/codex-steering.md) for limits and qualification.
General interrupted-task recovery and full-screen editing remain delivery work.

### 10.2 Context Assembly

Prompt input is assembled in three tiers:

1. Stable: runtime identity, invariant tool guidance, and stable policy.
2. Project context: repository instructions and explicitly loaded skills.
3. Volatile: relevant memory, recent events, resource state, timestamps, and task metadata.

### 10.3 Compaction

Compaction occurs only at safe boundaries and never separates a tool call from its result. It retains recent messages and creates a structured summary of decisions, requirements, failures, open work, referenced artifacts, and cumulative file/tool activity. The compaction record includes the first retained event and token estimates before and after compaction.

Current implementation also supports explicit summary drafting through the
signed-in Sol Codex provider: schema-inclusive context admission, durable start
before launch, decoded-secret redaction and host-derived provenance. Drafts do
not activate themselves. Explicit native compacted-history import supports
operator-supplied summaries and approved stored drafts with canonical provenance
and transactional review checks. Storage can deterministically discover the
newest currently approved draft for an exact immutable source task, skipping
revoked heads and failing closed on malformed or excessive review history. An
opt-in runtime policy can use that primitive after full-history automatic
routing finds no route, or when the built-in conservative floor proves an
explicitly selected model cannot fit its fully assembled initial request. It
rebuilds the continuation from the approved draft and reruns ordinary admission
exactly once before task creation or inference dispatch.
The default is off, and the policy never generates or approves a summary.
When an admitted provider instead reports `context_overflow`, the opt-in policy
may start one separately linked task using the same model and newest currently
approved summary. Recovery requires durable replay proof that the failed first
turn emitted no output, invoked no tool and created no confirmed or uncertain
effect. It preserves ordered retry lineage and aggregate route cost, observes
the terminal-tree attempt cap, and reruns all admission plus the transactional
approval check. It never replays the failed provider call. Partial streams,
cancellation, ambiguous model identity and absent or stale approval remain
terminal. Delegated work is eligible only for the separate plan-backed
mid-task path described below after every child has completed and its
acceptance, lease, policy and result evidence can be rederived exactly.
For a built-in history-first continuation whose complete initial request still
fits, the same off-by-default policy may freeze that currently approved summary
as a one-shot later-turn alternative. The first turn receives full history. If
completed-turn/tool growth or queued steering would overflow a subsequent
request, `context.compacted` is committed before the frozen prefix is replaced
and before provider redispatch. Durable admission revalidates the exact current
review, canonical source replacement, safe boundary, one-shot state and journal
budget; replay preserves all live suffix messages and tool pairs. The journal
budget reserves both terminal bytes and the final event slot, and definitive
exhaustion terminalizes without another provider dispatch. Revocation,
redaction drift, estimator/persistence failure, or an insufficient compact form
fails closed. Version-two plans extend this path to described custom context
engines, already-compacted multi-epoch continuations, completed bounded
delegation, and stateful Codex app-server rollover. Each surface adds its own
immutable evidence: engine and prompt-tier identities; complete ordered
lineage; exact parent/child policy, lease, context and accepted-result bindings;
or completed native-turn and provider-generation retirement checks. No surface
may infer success from a model self-assessment, an expired lease, or in-memory
provider state that cannot be reconstructed after restart.
Trusted Go hosts can opt into a named deterministic semantic validator after
drafting. Version-two review evidence binds the exact source sequence and
digest, complete draft digest, validator identity and previous review head;
lost-ack retries use a caller-supplied operation ID and do not re-invoke the
validator. Only approval authorizes continuation, while rejection and
abstention remain inactive and append-only. The stock runtime does not infer a
validator or treat an LLM self-review as deterministic approval. The SDK ships
an explicitly selected deterministic integrity linter which can reject bounded
mechanical defects but never returns approval; clean or ambiguous drafts
abstain for operator/domain review. Configured unattended semantic validation
and typed claim-level summary evidence remain required work. Release
qualification must distinguish deterministic local/SIGKILL recovery evidence
from live-provider, filesystem, power-loss and hardware durability claims; see
[native summary drafting](docs/codex-session-summaries.md),
[compacted continuation](docs/codex-compacted-continuation.md), and
[crash recovery](docs/crash-recovery-matrix.md).

Extended compaction must use a versioned, immutable plan before any additional
surface is enabled. The plan binds the exact approved checkpoint and source
range, summary attempt/review/draft evidence, context-engine implementation
identity, effective configuration and policy, stable/project/volatile tier
digests, exact original and replacement prefixes, and the message boundary at
which future live suffix history begins. Canonical structural validation is not
authorization: durable admission independently derives and cross-checks every
identity, then activation records the actual live-suffix count and digest. A
legacy checkpoint may continue to replay under its original contract but cannot
be interpreted as this stronger plan.

### 10.4 Memory

Memory stores durable facts rather than procedures. Records include provenance, confidence, creation time, last-use time, optional expiry, and privacy classification. Users can inspect, export, correct, or delete records. Local-only policy prevents external memory providers and keeps stored memory on the host.

The configured CLI, Go SDK and authenticated HTTP API provide a consistent scoped factual-memory export.
Built-in storage uses one SQLite read snapshot, includes current expired/private
facts, and leaves revisions and last-use unchanged. Versioned JSON contains
scope, observation time and ordered facts, with configured-secret redaction.
At most 1,000 facts and an 8 MiB encoded envelope are supported; overflow or
invalid records fails without partial output or silent pagination. Optional
custom exporters must guarantee equivalent completeness/isolation. This is not
a database backup or restore operation. POST `/v1/memory/export` accepts only
`{"version":1}`, uses the shared bounded memory-operation pool, and buffers the
validated response before writing. Native HTTP writes have a fifteen-second
deadline and explicit content length; failed transfers must not be accepted as
complete exports. Larger exports remain unimplemented. See [memory management](docs/memory-management.md).

## 11. Tools and Worker Safety

### 11.1 Tool Contracts

Tools declare schemas, capabilities, resource scopes, effect class, timeout behavior, and permission requirements. Arguments are validated before execution. Results distinguish success, rejected execution, deterministic failure, confirmed effect, no effect, and uncertain effect.

Declare tool operation behavior as `read_only`, `idempotent_write`, or `non_idempotent_write`, separately from observed `none`/`confirmed`/`uncertain` effects. Default legacy write declarations conservatively to non-idempotent; reject contradictions. Snapshot the declaration into durable dispatch/completion and approval bindings, preserve it in replay, recovery and audit, and expose it through SDK and approval inspection. Both write classes retain per-call approval and single-writer requirements. A declaration is not proof of implementation correctness and must never authorize automatic retry of confirmed or uncertain effects. Historical records without a declaration remain unclassified. See [tool behavior](docs/tool-behavior.md).

Known effect-free failures may explicitly permit a new model turn, without automatically replaying the failed call or resetting budgets. Persist the failed completion before delivering it to the model; preserve its failure identity across provider handoff, replay, compaction and audit. A repaired final task and positive user feedback do not erase failed procedural steps or qualify them as successful skill-learning evidence. Generic execution errors, panics, uncertain effects and nonrecoverable failures remain terminal. Every newly proposed action must pass normal policy and approval gates. See [recoverable tool failures](docs/recoverable-tool-failures.md) for the current read/delegation scope and provider qualification limits.

Current implementation increment: opt-in local `create_file` creates new UTF-8 files only after exact per-call review in terminal chat or a trusted SDK host. It uses pinned directories, atomic no-replace publication and durable approval consumption under the shared filesystem writer lease. Opt-in `replace_file` adds complete existing-file replacement up to 64 KiB per old/new content, exact preimage checks, reviewed content and retained private recovery copies. Replacement preserves basic permission bits, not extended metadata; its rename is atomic visibility, not external-writer compare-and-swap. Children retain read-only capabilities. General patch editing, delegated writes, unattended/headless approvals and stronger isolation remain later work. See [reviewed file creation](docs/reviewed-file-creation.md) and [reviewed replacement](docs/reviewed-file-replacement.md) for configuration and cooperative-filesystem limits.

For operator-approved cloud-coordinator/local-worker inspection, separate the
parent and child catalogs: the cloud coordinator receives delegation tools but
never direct filesystem authority. The child receives a count-only
`read_file` schema requiring `count_regular_files`; its result contains bounded
direct and recursive regular-file totals and skipped-entry diagnostics without
file names or contents. Symlinks, root escapes, writes and shell execution are
excluded. Both file-tools and delegated-read settings plus an absolute root are
required, and saved changes become authority only after daemon restart.

### 11.2 Permission Model

Rules support `allow`, `deny`, and `ask`, scoped by tool and resource. Child workers inherit every parent denial and may add stricter rules. Later rules cannot weaken a non-overridable organizational or local-only policy.

### 11.3 In-Process Workers

The MVP runs one orchestrator and a configurable bounded worker pool. Inference and declared read-only tools may execute concurrently. Side-effecting tools require a single-writer lease for each affected resource scope.

Pre-child orphan recovery must also cover an owned reader after `task.started`
but before `worker.started`, and after worker start with optional heartbeats.
Require explicit read-only parent dispatch, strict execution-free worker history,
retained original process-death proof and transactional absence of all linked
child records. Append worker failure, release the exact reader and record
`orphan_worker_without_child_unlocked` atomically; never synthesize a child or
success. Exact retries re-derive the failure from canonical bounded history.

Application tool dispatch must acquire shared leases for allowed read-only tools and retain ownership until handlers actually return. Expired but unreleased readers must still exclude writers; cancellation and expiry are not proof of termination. Built-in file tools conservatively share a single `workspace` scope across all roots, covering nesting and aliases; legacy `create_*` scopes remain conflicting during upgrades. SDK hosts must canonicalize overlapping custom resources. Busy admission must not silently replay a tool. See [reader/writer execution](docs/reader-writer-execution.md) for the implemented boundary and outstanding crash-reconciliation limits.

Workers use durable work records, leases, heartbeats, and acceptance states. Supervisors re-derive liveness from durable events, leases, and observed goroutine/provider state. Lease expiry, orphaned work, or stalls trigger safe recovery or operator attention; they do not imply that an uncertain effect can be replayed.

New leases must carry durable local execution-image ownership, not PID or heartbeat inference alone. Schema 22 binds them to a private lifetime-held OS lock and requires matching ownership for active lease mutations. New guards use a durable private user-configuration directory, overridable before first acquisition with `DARWIN_PROCESS_OWNER_DIR`; existing references retain their original location. Legacy ownership remains unknown. An unlocked probe alone is insufficient reclamation authority and does not attest remote or descendant termination. See [process-lifetime ownership](docs/process-lifetime-ownership.md) for platform, retention and remaining safe-recovery requirements.

Schema 23 implements a bounded daemon sweep for terminal readers only: hold and revalidate the unlocked owner probe through a transaction, require complete terminal replay without pending tools, uncertain effects or interrupted turns, then atomically release the exact reader and record a private immutable receipt. Do not rewrite task history, infer acceptance, change fitness, resume work or replay tools. Writers and running/unknown holders remain unresolved. See [terminal reader recovery](docs/terminal-reader-recovery.md).

A separate schema-23 orphan-worker sweep handles running supervisor journals with one terminal, effect-resolved child or an eligible interrupted child, and a verified parent delegation binding. Retain original process ownership proof, then atomically append worker failure (`worker_owner_interrupted`), update the head, release the exact reader and record an event-bound receipt. A running model-only child receives its own deterministic `interrupted_model` failure in that same transaction. A child with fully paired, explicitly read-only current tools and no-effect results receives `interrupted_read_only_model`; preserve tool results, partial deltas and interrupted-turn evidence. These two paths reject independently held child resource leases. Preliminary acceptance must never become recovered success. Existing parent reconciliation may subsequently record failure, but no worker/child rerun or automatic continuation is authorized. See [orphan worker recovery](docs/orphan-worker-recovery.md).

A narrowly qualified pending-tool path resolves 1–32 already-dispatched, explicitly `read_only` child calls with deterministic failed `tool.completed` records (`tool_failed`, effect `none`, fixed `read_only_tool_interrupted` error), then appends `interrupted_read_only_tool` child failure and worker failure atomically. It never invents actual tool output, successful execution or dispatch. All past completed tool results must be read-only/no-effect; undispatched proposals, delegation tools, writes, legacy behavior, acceptance/evaluation/error records and unresolved effects remain excluded. Up to 64 unreleased child readers are permitted only when their complete process identity/reference matches the worker's retained unlocked execution-image guard. Their exact snapshots must remain unchanged through commit; the worker transaction does not release them. Separate terminal-reader recovery must independently verify and reclaim each reader. Receipt retry re-derives the entire synthetic suffix from canonical bounded original history. This trusts the declared read-only contract, does not prove remote cancellation, and grants no retry or reassignment authority. Actual application SIGKILL fixtures cover interruption after the read handler returned but before result persistence or reader release, not interruption inside arbitrary callbacks. General pending-tool, writer and uncertain-effect recovery remains unfinished.

Joined worker finalization must commit the terminal event, task projection and exact reader-lease release atomically before output delivery. Preliminary validator acceptance or `worker.completed` is not a substitute for that terminal commit. Production storage implements this boundary, while legacy journals must at least surface cleanup failure rather than returning success. An enclosing tool reader is a separate lease and is not released by worker finalization or parent journal recovery. See [worker finalization and crash boundaries](docs/worker-finalization.md) for qualification and remaining reconciliation requirements.

Operator inspection must distinguish an absent/legacy observation from an observed empty scope, include expired unreleased readers and compatibility-alias holders, and never expose lease capabilities. Approval execution inspection provides a bounded versioned reader/writer count summary from the same transaction as the approval and journal. Counts are diagnostic only: they cannot prove process termination or authorize dispatch, retry, release or reassignment. General holder discovery and safe crashed-holder reconciliation remain required beyond this summary.

### 11.4 Acceptance Gate

A worker reporting completion produces a candidate result. The orchestrator releases it only after required validators accept it and the acceptance record is durable. Rejected candidates retain diagnostics for a bounded safe retry or fallback.

## 12. Hardware and Resource Management

The resource profiler samples:

- System RAM and swap.
- Apple unified memory or NVIDIA/AMD VRAM when observable.
- CPU availability and load.
- Thermal pressure.
- Loaded local models and estimated memory requirements.
- Current provider and worker concurrency.

Linux thermal sampling uses kernel-exported passive/hot/critical thresholds,
not fixed temperature guesses. Missing or incomplete measurements remain
unknown; pressure must flow into existing local admission without modifying OS
cooling policy. The current bounded implementation and physical-qualification
limits are recorded in [Linux thermal profiling](docs/linux-thermal-profiling.md).

Default allocation policy (GiB means 2^30 bytes):

- Below 16 GiB usable unified memory/VRAM: one local execution; unload before incompatible model switches.
- From 16 GiB through 64 GiB: one or two executions subject to measured pressure and declared footprints.
- Above 64 GiB: bounded parallel executions or ensembles when policy enables them.

For automatic concurrency, usable headroom is the configured percentage ceiling
minus observed host use and outstanding reservations. Where a model has a
discrete-GPU binding, apply the RAM tier globally and the bound device's VRAM
tier to that device's active executions. Independent GPU capacities must not be
summed into a fungible pool. Legacy custom aggregate observations use the smaller
RAM/VRAM tier and cannot overlap device-specific reservations. Cap admission by the
configured worker ceiling and observed CPU thread count; missing CPU measurements
permit only one slot. Recompute before each admission without preempting existing
work solely because it now exceeds a lower tier. A numeric concurrency setting
remains an explicit cap, but never bypasses memory or pressure checks.

Residency management must distinguish a released task reservation from actual
provider eviction. For the initial Ollama backend, explicit provider
`manage_residency: true` authorizes inspection and unloading of configured idle
models on a dedicated loopback endpoint. It defaults off on potentially shared
servers. Hold active-use references through tool pauses; never evict an active
or unknown model. Require an unload acknowledgement and fresh inventory absence,
then re-profile hardware before admitting the replacement. Ambiguous unload
outcomes must not cause blind repeat mutations. A single Service coordinates the
initial implementation; cross-process/shared-server fencing and equivalent
lifecycle adapters for other backends remain required qualification work before
those deployment scenarios are supported.

Local discrete-GPU models declare `gpu_device` as a source-qualified NVIDIA UUID
or AMD card identifier plus positive RAM/VRAM footprint estimates. Admission
requires fresh matching observations and rejects unavailable or ambiguous device
state. In the initial implementation, placement is operator-declared: backend
affinity must be configured externally, and diagnostics must clearly distinguish
accounting from verified placement. Automatic affinity verification, stable AMD
identity resolution, multi-device sharding and partition-aware allocation remain
delivery requirements. Unbound models must not inherit a guessed GPU. Auxiliary
reviews and summaries obey the same per-device and shared host reservations.

Threshold crossings suspend new local admissions. Hybrid mode may use an eligible cloud route; local-only mode queues or rejects according to queue policy. Active work is canceled only for safety-critical pressure.

The initial pressure policy defaults to `reject`. Operators may choose `wait`
with a bounded admission allowance (100 ms through five minutes). Only verified
resource-capacity denials before task dispatch may retry; invalid configuration,
unavailable measurements, provider execution, tool effects and ambiguous durable
state are not pressure retries. Automatic hybrid routing tries eligible cloud
alternatives before waiting, without overriding local-only/privacy constraints.
Admitted work retains its original execution deadline. Waiters are bounded by the
service's execution slots; durable submissions retain their existing request and
claim identity while waiting. Stronger fairness and cross-process admission
coordination remain separate delivery requirements.

## 13. Storage and Observability

Provide read-only task lease/recovery metadata through CLI `task leases`, SDK
`InspectTaskLeases`, and authenticated `GET /v1/tasks/{id}/leases`. Validate one
bounded coherent snapshot, distinguish unavailable legacy feature groups from
measured zero, omit execution capabilities and content, and never interpret
expiry/counts as process-death or retry authority. The implemented diagnostic
covers task-owned aggregate counts. Bounded per-scope holder discovery is also
implemented through CLI `resources leases`, SDK `InspectScopeLeases`, and
authenticated `GET /v1/resources/leases?scope=...`, using the admission overlap
policy. It reveals requested scope and holder task IDs, not private lease
capabilities. Schema 24 adds durable attention for expired unreleased leases,
maintained by bounded daemon sweeps and inspectable through CLI `resources
attention`, SDK `ListLeaseAttention`, and authenticated `GET
/v1/resources/attention`. Renewal/release resolves observations;
expiry recurrence reopens the same record. These are diagnostic snapshots, not
execution or recovery authority. Daemon sweeps isolate each candidate transaction
so malformed metadata does not starve later leases; errors remain visible and
failed candidates are revisited after cursor wrap, without repair or release.
Schema 25 appends changed observations atomically
with the current projection, with byte-preserving migration baselines for existing
records. History is inspectable through CLI `resources attention-history`, SDK
`ListLeaseAttentionHistory`, and authenticated HTTP
`GET /v1/resources/attention/{id}/history`. Operator acknowledgment,
notifications, retention and broader stall reasons remain
required. See [lease attention](docs/lease-attention.md).
See [task lease inspection](docs/task-lease-inspection.md) and
[scope holder inspection](docs/scope-holder-inspection.md).

SQLite operates in WAL mode with serialized, versioned migrations. Event appends and corresponding state projections are transactional. Startup verifies database integrity and migration compatibility before readiness.

Schema28 adds a task/kind/sequence index to accelerate journal evidence reads
without changing eligibility, recent-sample ordering or evidence validation.
Read-only legacy schemas remain supported at their feature-specific minimums;
older writers must stop before migration. See [index migration](docs/event-kind-index.md)
and [bounded performance evidence](docs/benchmarks.md). Large-history migration
cost, concurrent workloads and the complete latency SLAs remain to be qualified.

Core stored entities include configurations, policy snapshots, tasks, sessions, events, attempts, routes, provider health, model fitness, evaluation evidence, work leases, memory, skill versions, approvals, and audit records.

Metrics and traces follow OpenTelemetry conventions and cover task latency, route decisions, provider calls, tool calls, worker leases, retries, fallbacks, compactions, fitness updates, skill changes, queue pressure, and resource pressure. Export is optional and disabled by default; local metrics remain available.

An explicit one-shot CLI/SDK OTLP/HTTP JSON export now delivers the existing
content-free lifecycle gauges through the configured deployment-mode network
policy. It reads existing storage only and introduces no implicit background
network access. See [metric export](docs/metrics-export.md) for delivery semantics,
credential handling and bounds. Opt-in daemon scheduling and an owned SDK
exporter now send fresh snapshots sequentially and report supplemental health,
without blocking task readiness on collector failure. The legacy
`opentelemetry_enabled` switch is supported as an alias for enabling the
configured periodic metrics exporter; it requires the same explicit destination
and policy checks and does not disable task or learning execution. Explicit
CLI/SDK trace export now reconstructs bounded recent task roots with
successfully paired provider-turn, tool-call and worker children plus fixed
route/exploration, evaluation, fallback, compaction, skill-context, steering
and error observations. Schema 4 additionally emits fixed thermal/swap pressure
observations from the canonical task-start resource snapshot without exporting
host measurements or source strings. Schema 5 adds terminal, content-free
reader/writer lease-state observations without exporting counts, capabilities,
owners, scopes, expiry instants, or process references. Schema 6 adds validated
base/revised fitness-mutation observations without exporting routing keys,
scores, samples, evidence, or evaluator identities. Schema 7 includes running
roots ending at the coherent observation instant, with only durably completed
child operations; it does not infer process health or fabricate completion for
in-flight operations. Schema 8 adds independent content-free roots for
terminal model-generated drafts and committed catalog activation/rollback
transitions at their actual durable timestamps. These roots reveal no skill,
version, model/provider, validator, evidence, source, or operation identity and
are not falsely attached to source tasks. A linked top-level submission also emits one fixed
queue-residency bucket at task start without exposing submission identity or
exact arrival time; retries and delegated children are excluded. Paired tool
completions expose only their authoritative none/confirmed/uncertain effect
class, never tool identity or result content. Public snapshots have no content or durable identity,
and fixed route-constraint observations expose only the nine canonical
exclusion reason classes—not candidate identities. Unknown reasons fail closed.
Each serialization uses fresh random OTLP trace/span IDs. An independently
configured daemon/SDK trace supervisor now
sends fresh bounded snapshots sequentially, fences configuration rotation and
reports supplemental health; scheduling and delivery are non-durable. Broader
span families, queue arrival/service rates, stable correlation and the complete instrumentation list above
remain unfinished. See [trace export](docs/traces-export.md). The exported
closed-vocabulary gauges now also
count every canonical durable runtime-event kind, providing content-free task,
provider-turn, tool, worker, route, evaluation, error and steering activity.
Derived gauges count fallback-linked tasks, compactions, skill-context use,
exploration and capacity/budget/privacy/health route exclusions. Snapshot schema
version 7 also reports fixed accept/reject/abstain advisory audit outcomes
without evaluator, candidate, evidence or finding identity. These do not become
objective success or direct fitness evidence. Version 6 derives fixed provider-turn and tool-call duration histograms
from paired durable events. Observed, missing-start, missing-end and invalid-time
samples reconcile with canonical start/completion totals without exporting any
provider, model, tool, task, turn, attempt or call identity. These are retained
lifecycle wall times, not provider-reported server latency. Version 5 classifies
the bounded durable queued population into fixed age
buckets, reconciled exactly with queued submission state, so sustained waiting
is observable without exporting submission IDs or exact arrival timestamps.
Direct thermal
and other host-resource measurements are now attached to application-backed
snapshot schema version 4 with explicit per-measurement availability. Exported
values are limited to fixed CPU-thread, RAM, swap, aggregate VRAM,
thermal-pressure and unified-memory gauges; device inventory, profiler
provenance and host identity remain private. Cloud-only, disabled or failed
profilers report unavailable rather than fabricated zero, while storage-only
inspection omits the live block. Queue arrival/service rates, operation-specific
cardinality, per-device capacity, model
residency and reservation telemetry remain separate work.

Schema29 now records task start-to-terminal event wall time transactionally,
including recovery terminals, and exports cumulative fixed-bucket histograms
with explicit missing/invalid timing counts. This is not inference latency or
quality evidence. Historical tasks are not retrospectively sampled; see
[task-duration metrics](docs/task-duration-metrics.md) for coverage and reset limits.

Schema33 introduces the canonical global runtime-event ledger described in the
SDK section. Migration deterministically backfills legacy canonical event rows
in SQLite insertion order with bounded memory, rejects incomplete or corrupt
history transactionally, and resumes autoincrement positions after the migrated
high-water mark. The ledger is an ordering and catch-up mechanism, not an
acceptance decision, delivery acknowledgement, or permission to retry model or
tool effects.

## 14. Security and Privacy

- Fully local mode denies unauthorized outbound connections at the DarwinRouter transport boundary.
- Recognized loopback provider destinations, including case-insensitive `localhost`, remain pinned to loopback in every deployment mode; enabling cloud routes must not delegate local endpoint resolution to DNS.
- Provider credentials are read from environment variables or secret-store adapters and are never serialized into configuration snapshots.
- Logs, events, errors, traces, and route explanations pass through structured redaction before persistence or export.
- Tool schemas and model output are untrusted input.
- Approval is required for destructive tools, model disabling/removal, and policy changes.
- Automatic retries require proof of no effect or a verified idempotency contract.
- Daemon network binding is local by default; remote binding requires authentication and explicit configuration.
- Memory and skill records honor task privacy classifications and deletion requests.

## 15. Reliability and Failure Handling

DarwinRouter must remain recoverable across crashes at every durable boundary: before and after task admission, provider dispatch, stream capture, tool execution, evaluation, fitness update, compaction, skill activation, and response acknowledgement.

Provider errors use normalized typed classifications. Expected operational failures return events and durable outcomes rather than panicking the daemon. OOM, context overflow, timeout, cancellation, quota exhaustion, invalid schemas, partial streams, and lost acknowledgements have explicit resolution paths.

Provider-reported context overflow is a non-retryable execution outcome. HTTP
413 and an exact recognized structured provider code normalize to the closed
`context_overflow` classification; arbitrary response prose is never parsed for
retry authority. The task records that terminal code and automatic fallback
does not resend the same oversized context to another route. The separately
linked, approved-summary recovery described in Section 10.3 is the sole current
exception: it uses a smaller, frozen context on the same model only after exact
effect-free failure verification, and remains disabled by default.

Local preflight uses the same durable overflow code when a trusted estimator's
finite count exceeds the configured model window. Estimator errors and panics
instead become `context_estimation_failed`; they must not be conflated with a
known overflow or with generation, output, and iteration budget exhaustion.

## 16. Testing and Acceptance

- Unit-test scoring, normalization, policies, schemas, migrations, redaction, state transitions, and safe retry rules.
- Use golden provider fixtures for stream ordering, tool pairing, Unicode, malformed output, context overflow, cancellation, and cross-provider handoff.
- Run Go race detection on event delivery, storage, worker leases, routing updates, and shutdown.
- Property-test event replay, configuration precedence, route-ranking invariants, and idempotency.
- Inject failures before and after every durable boundary.
- Simulate low-, mid-, and high-resource machines.
- Prove fully local mode produces zero unauthorized external requests.
- Verify adversarial output cannot bypass tool schemas, permissions, resource scopes, or redaction.
- Require attributable evidence under the domain-sensitive evaluation policy before a result updates fitness; automatic skill activation still requires deterministic validation.
- Restart from a clean process and reproduce task, route, worker, evaluation, memory, and skill state.
- Benchmark deterministic routing below 150 ms and auxiliary classification below 500 ms, excluding provider inference.
- Qualify chat and Kanban flows with browser end-to-end tests covering stream
  resume, refresh/restart recovery, stale revisions, dependency cycles, lease
  expiry, approval boundaries, content injection, CSRF/origin enforcement,
  keyboard accessibility, and local-only zero-egress behavior.

### 16.1 Release Artifact Acceptance

Release preparation must bind artifacts to an explicit reviewed source commit
and semantic version. Build macOS/Linux amd64/arm64 archives from one isolated
snapshot of committed source, with fixed build/archive metadata. Include a
canonical target-specific SPDX 2.3 SBOM in each archive and a versioned manifest
and checksums covering each complete archive and the manifest. The SBOM must
identify the exact binary SHA-256, module-level Go dependency/toolchain closure,
and hashes of the first-party Web UI sources embedded in that binary. Unknown or
unreviewed dependency license conclusions remain `NOASSERTION`.
Qualify reproducibility with two complete builds and verify executable target
identities and the native CLI version. Cross-compilation is not native runtime
qualification on the other targets.

SBOM generation is inventory evidence only. It does not perform vulnerability
analysis, establish independent build provenance, make a legal conclusion, or
replace candidate-bound license evidence, third-party notices, and human review.

Sign the checksum set with a separately provisioned release identity; never
reuse repository SSH credentials. Verification must require an independently
trusted public key, reject malformed manifests, missing/extra files and unsafe
paths, and perform no extraction or execution. Publication must not overwrite
existing artifacts or occur implicitly during build/test. A real release still
requires full runtime acceptance, approved license/dependency notices, supported
platform qualification, installation/migration guidance and reviewed release
notes. Local test signatures do not establish a production signing identity or
platform notarization.

## 17. Delivery Roadmap

### Phase 1: Foundation

Go module, quality gates, versioned configuration, SQLite/WAL migrations, canonical runtime events, and append-only persistence.

### Phase 2: Agent Runtime

Provider contracts, OpenAI-compatible and Ollama adapters, minimal agent/tool loop, steering, cancellation, recovery, conformance tests, permissions, effect evidence, and bounded workers.

### Phase 3: Adaptive Routing

Hardware profiling, eligibility constraints, normalized fitness, bounded exploration, failure-domain-aware fallbacks, resource throttling, and route explanations.

### Phase 4: Learning

Evaluation ladder, transactional fitness updates, safe compaction, memory, progressive skills, automatic skill drafting, validation, versioning, and rollback.

### Phase 5: Product Surfaces and Hardening

Daemon, CLI, Go SDK, OpenAI-compatible API, Darwin-native API, authenticated Web
UI chat, durable integrated Kanban, agent-facing board operations, local-only
egress enforcement, redaction, recovery qualification, performance benchmarks,
and release packaging.

### Post-MVP

Subprocess, Git-worktree, container, SSH, and remote worker backends; multi-stage
agent pipelines; and messaging and scheduling adapters. Advanced analytics and
cross-installation board federation remain post-MVP; the core Web UI and Kanban
are 1.0 requirements.

## 18. Risks and Mitigations

| Risk | Mitigation |
| --- | --- |
| Biased or sparse fitness data | Domain-specific scores, uncertainty, minimum samples, bounded exploration, and inspectable evidence. |
| Unsafe in-process side effects | Permission policy, effect classes, exclusive resource leases, acceptance gates, and no uncertain-effect replay. |
| Self-modifying skills regress behavior | Validation, provenance, immutable versions, bounded activation scope, monitoring, and rollback. |
| Local hardware instability | Continuous pressure profiling, admission control, model unloading, graceful queuing, and optional cloud offload. |
| Context summaries lose critical state | Safe boundaries, structured summaries, recent-event retention, and durable original history. |
| Provider-specific behavior leaks into core | Narrow contracts, adapters, normalized events/errors, and conformance suites. |
| Telemetry exposes sensitive data | Structured redaction, local defaults, export opt-in, retention controls, and privacy classifications. |
| Web content executes model-controlled code | Strict sanitization, CSP, no implicit external fetches, and browser/API security tests. |
| Agent board updates bypass acceptance or duplicate work | Durable dependencies, revisions, idempotency, scoped leases, policy checks, and separate acceptance state. |

## 19. Default Decisions

- Implementation language: Go.
- Runtime shape: full agent runtime with adaptive routing as the defining capability.
- MVP isolation: bounded in-process workers.
- Parallelism: concurrent inference/read-only work and single-writer side effects.
- Persistence: SQLite/WAL behind storage interfaces.
- User surfaces: daemon, CLI, Go SDK, HTTP API, and authenticated Web UI with
  integrated chat and Kanban work planning.
- Learning: automatic telemetry, fitness updates, and validated skill evolution.
- Approval: required for model pruning, policy changes, and destructive actions.
- Privacy: local storage and export-off defaults.
