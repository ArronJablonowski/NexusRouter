# Product Requirements Document: DarwinRouter

**Version:** 1.0.0

**Author:** Arron Jablonowski

**Date:** September 4, 2026
**Status:** Draft / Specification

---

## 1. Executive Summary

DarwinRouter is a Go-based, local-first agent runtime whose defining capability is adaptive model routing. It evaluates tasks, chooses among heterogeneous local and cloud models, executes provider-neutral agent and tool loops, measures outcomes, and improves future routing from durable evidence.

The product combines a compact event-driven runtime, persistent sessions and memory, progressively loaded procedural skills, bounded worker delegation, hardware-aware scheduling, auditable policy enforcement, and restart-safe telemetry. It runs as a persistent daemon controlled through a CLI, a versioned Go SDK, an OpenAI-compatible HTTP surface, and Darwin-native task-management APIs.

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

### 2.3 Non-Goals for 1.0

- Messaging-platform gateways, voice interfaces, and personal-assistant features.
- General-purpose cron or workflow automation.
- Automatic model deletion, disabling, or policy changes without operator approval.
- Parallel side-effecting agents without isolated execution.
- Built-in subprocess, Git-worktree, container, SSH, or remote worker backends; these are post-MVP adapters.
- A production web dashboard.

## 3. Research Basis

DarwinRouter adapts proven ideas from three open-source agent systems while retaining routing as its core product identity.

### 3.1 Hermes Agent

Hermes distinguishes durable factual memory from reusable procedural skills, loads skills progressively, separates stable prompt material from volatile session state, and permits auxiliary models for bounded tasks. DarwinRouter adopts those separations, automatic skill drafting, tiered prompt assembly, replaceable context and memory engines, and strict credential hygiene.

Automatic skill changes are allowed only within an explicit scope. Every activated version must retain provenance, validation evidence, its predecessor, and a rollback path. Messaging gateways, voice, and broad assistant features are deferred.

Sources: [Hermes documentation](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/index.mdx), [configuration and context engine](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/configuration.md), and [prompt assembly](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/developer-guide/prompt-assembly.md).

### 3.2 Pi Agent

Pi demonstrates a small provider-neutral loop driven by typed streaming events, append-only sessions, resumable branches, safe compaction boundaries, steering, and extension hooks. DarwinRouter adopts a compact core loop, event-first integration, paired tool-call/result preservation, structured compaction records, and cross-provider conformance testing.

Terminal presentation remains outside the runtime. CLI, HTTP, and future UIs consume the same event stream through adapters.

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
+---------------- CLI / SDK / HTTP ----------------+
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
- `EventSink`: receive ordered, versioned runtime events.

Interfaces accept `context.Context`; implementations must honor cancellation. Public records include schema versions and reject unknown incompatible major versions.

### 6.2 HTTP API

DarwinRouter exposes:

- An OpenAI-compatible streaming chat/completion endpoint.
- Native task submission, cancellation, status, and Server-Sent Events endpoints.
- Route-explanation and model-health endpoints.
- Session, branch, and resume endpoints.
- Feedback and evaluation endpoints.
- Memory and skill inspection/management endpoints.
- Health, readiness, and metrics endpoints.

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

hardware:
  auto_profile: true
  max_ram_usage_pct: 80
  max_vram_usage_pct: 85
  max_concurrent_local_models: auto

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
  - id: local-fast
    provider: local-primary
    model: "local-model-id"
    locality: local
    capabilities: [chat, summarize, classify]
  - id: cloud-reasoning
    provider: cloud-primary
    model: "cloud-model-id"
    locality: cloud
    capabilities: [chat, reasoning, code, tools]

routing:
  exploration_rate: 0.05
  minimum_samples: 20
  decay_half_life: 30d
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

memory:
  enabled: true
  local_only: true

evaluation:
  llm_judge_enabled: true
  evidence_precedence: [deterministic, tool_result, user_feedback, llm_judge]

security:
  local_only_egress: deny
  default_tool_policy: ask

telemetry:
  database: "${DARWIN_DATA_DIR}/darwin.db"
  opentelemetry_enabled: false
```

Durations, percentages, weights, paths, provider references, capability names, and mode-specific contradictions must be validated before the daemon becomes ready.

## 8. Adaptive Routing

### 8.1 Task Classification

Tasks receive one or more domains, required capabilities, privacy classification, context estimate, tool requirements, latency objective, budget, and execution-risk classification. Deterministic rules run first. An auxiliary classifier may fill ambiguous fields when policy and latency budgets permit.

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

An ineligible model cannot win through a high historical score.

### 8.3 Fitness Ranking

Eligible models are ranked within their task domain and execution profile using normalized values for quality, schema compliance, reliability, latency, cost, recency, and uncertainty. Metrics are comparable only after normalization to documented units and ranges.

Fitness is maintained by model, provider, domain, and relevant execution profile. Recency decay limits stale evidence. Minimum-sample and uncertainty terms prevent overconfidence. Bounded exploration gives new or rarely used candidates a controlled opportunity without violating hard constraints.

### 8.4 Fallbacks

The router preselects a fallback from a different failure domain when possible. A retry is allowed only when policy, remaining budget, and effect evidence make it safe. A tool or model attempt with confirmed or uncertain external side effects is never automatically replayed.

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

### 9.1.1 Orchestrator audits and domain-sensitive feedback

The orchestrator must be able to audit another model's output and observed results automatically, using a bounded auxiliary review call where useful. The audit receives task requirements, the candidate output, and available validation/tool evidence as untrusted data; it has no tool execution or permission-changing authority. It produces a validated structured assessment with findings, evidence references, confidence, and an abstain outcome. Persist the evaluator identity, rubric version, task domain, and audit cost separately from candidate performance.

- Deterministic checks identify empty/whitespace-only final answers when a textual answer is required, invalid required schemas, failed tests, and other mechanically verifiable failures. Intermediate empty assistant messages with tool calls are not empty-output failures.
- Nonblank text is not evidence of meaningful completion. The orchestrator should flag responses that merely repeat the request, promise future work, or omit required deliverables, citing the requirements and candidate output. These semantic findings remain advisory rather than fabricated deterministic failures; brevity alone is not a defect.
- Code tasks combine tests, compiler/linter results, and contract checks with orchestrator code review. A model review can identify suspected defects, but must not claim tests ran or passed without execution evidence. Passing tests alone does not establish complete correctness.
- Creative and preference-heavy tasks give explicit user feedback greater influence than orchestrator taste judgments. Model-only subjective assessments remain low-confidence, bounded advisory signals; they must not disable models, activate skills, or outweigh later user feedback.
- Keep objective validity and subjective quality as separate dimensions. A user preference does not erase a failed test, and a test pass does not establish creative quality. Where no evidence supports an assessment, abstain rather than invent success.
- Later user feedback must be able to supersede prior subjective judge evidence through compensating, versioned records. Recompute the affected fitness contribution without double-counting the attempt; preserve original evidence and revision history. Corrections to objective evidence require explicit provenance.
- Audits inherit local-only/privacy constraints, use independent evaluator identity where possible, enforce time/cost budgets, and do not recursively judge their own judgments. Failed, malformed, or missing audits never become positive evidence.

Acceptance tests must cover blank final answers versus tool-only intermediate turns, compiler/test evidence versus review opinion, creative-task user preference overriding a prior judge contribution, abstention, adversarial output attempting to influence the evaluator, revision replay/idempotency, and local-only audit egress denial.

### 9.2 Fitness Updates

Fitness updates are transactional, replay-safe, and linked to immutable evidence. Failed or canceled persistence cannot partially update routing state. Operator-approved corrections produce compensating records rather than rewriting history.

### 9.3 Pruning

Models crossing configurable failure or value thresholds become deprecation candidates. DarwinRouter explains the evidence and estimated resource savings, but disabling, uninstalling, or deleting a model requires operator approval.

### 9.4 Skill Learning

Repeated successful workflows may produce an automatic skill draft. A draft records source sessions, generalized steps, required tools, configuration, risks, and validation cases. Activation requires configured validation. Each revision retains prior versions and rolls back automatically when validation fails or post-activation outcomes materially regress.

## 10. Sessions, Context, and Memory

### 10.1 Durable Sessions

State transitions are appended durably before acknowledgement. Events carry task, session, turn, attempt, worker, route, and causation identifiers. Replay must reproduce externally visible state without repeating uncertain effects.

Sessions support cancellation, retries, resumable branches, steering messages, follow-ups, checkpoints, and idempotency keys.

### 10.2 Context Assembly

Prompt input is assembled in three tiers:

1. Stable: runtime identity, invariant tool guidance, and stable policy.
2. Project context: repository instructions and explicitly loaded skills.
3. Volatile: relevant memory, recent events, resource state, timestamps, and task metadata.

### 10.3 Compaction

Compaction occurs only at safe boundaries and never separates a tool call from its result. It retains recent messages and creates a structured summary of decisions, requirements, failures, open work, referenced artifacts, and cumulative file/tool activity. The compaction record includes the first retained event and token estimates before and after compaction.

### 10.4 Memory

Memory stores durable facts rather than procedures. Records include provenance, confidence, creation time, last-use time, optional expiry, and privacy classification. Users can inspect, export, correct, or delete records. Local-only policy prevents external memory providers and keeps stored memory on the host.

## 11. Tools and Worker Safety

### 11.1 Tool Contracts

Tools declare schemas, capabilities, resource scopes, effect class, timeout behavior, and permission requirements. Arguments are validated before execution. Results distinguish success, rejected execution, deterministic failure, confirmed effect, no effect, and uncertain effect.

### 11.2 Permission Model

Rules support `allow`, `deny`, and `ask`, scoped by tool and resource. Child workers inherit every parent denial and may add stricter rules. Later rules cannot weaken a non-overridable organizational or local-only policy.

### 11.3 In-Process Workers

The MVP runs one orchestrator and a configurable bounded worker pool. Inference and declared read-only tools may execute concurrently. Side-effecting tools require a single-writer lease for each affected resource scope.

Workers use durable work records, leases, heartbeats, and acceptance states. Supervisors re-derive liveness from durable events, leases, and observed goroutine/provider state. Lease expiry, orphaned work, or stalls trigger safe recovery or operator attention; they do not imply that an uncertain effect can be replayed.

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

Default allocation policy:

- Below 16 GB available unified memory/VRAM: one local model, unload before incompatible model switches.
- From 16 GB through 64 GB: one or two models subject to measured pressure and declared footprints.
- Above 64 GB: bounded parallel models or ensembles when policy enables them.

Threshold crossings suspend new local admissions. Hybrid mode may use an eligible cloud route; local-only mode queues or rejects according to queue policy. Active work is canceled only for safety-critical pressure.

## 13. Storage and Observability

SQLite operates in WAL mode with serialized, versioned migrations. Event appends and corresponding state projections are transactional. Startup verifies database integrity and migration compatibility before readiness.

Core stored entities include configurations, policy snapshots, tasks, sessions, events, attempts, routes, provider health, model fitness, evaluation evidence, work leases, memory, skill versions, approvals, and audit records.

Metrics and traces follow OpenTelemetry conventions and cover task latency, route decisions, provider calls, tool calls, worker leases, retries, fallbacks, compactions, fitness updates, skill changes, queue pressure, and resource pressure. Export is optional and disabled by default; local metrics remain available.

## 14. Security and Privacy

- Fully local mode denies unauthorized outbound connections at the DarwinRouter transport boundary.
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

Daemon, CLI, Go SDK, OpenAI-compatible API, Darwin-native API, local-only egress enforcement, redaction, recovery qualification, performance benchmarks, and release packaging.

### Post-MVP

Subprocess, Git-worktree, container, SSH, and remote worker backends; multi-stage agent pipelines; messaging and scheduling adapters; and an operational dashboard.

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

## 19. Default Decisions

- Implementation language: Go.
- Runtime shape: full agent runtime with adaptive routing as the defining capability.
- MVP isolation: bounded in-process workers.
- Parallelism: concurrent inference/read-only work and single-writer side effects.
- Persistence: SQLite/WAL behind storage interfaces.
- User surfaces: daemon, CLI, Go SDK, and HTTP API.
- Learning: automatic telemetry, fitness updates, and validated skill evolution.
- Approval: required for model pruning, policy changes, and destructive actions.
- Privacy: local storage and export-off defaults.
