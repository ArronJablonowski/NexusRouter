# MVP qualification

DAR-45 has one deterministic release-candidate gate:

```sh
make qualify-mvp
```

The command uses loopback provider fixtures and disposable SQLite databases. It
does not contact OpenAI or Ollama, consume signed-in account usage, inspect user
conversations, or modify a configured NexusRouter store.

## Acceptance matrix

| Requirement | Qualification evidence |
| --- | --- |
| Fully local | A local-only task runs through the Ollama adapter, streams a redacted answer, and reaches a durable terminal event; a cloud-designated route is denied. |
| Fully cloud | A cloud-only task runs through the OpenAI-compatible streaming adapter, authenticates through the secret resolver, and reaches a durable terminal event without persisting the credential. |
| Hybrid | A Sol-shaped cloud coordinator delegates bounded work to an isolated Ollama-shaped local worker, receives an untrusted result, reviews it, and preserves parent/work/execution lineage. Privacy-constrained variants deny the cloud coordinator. |
| Orchestrator audit | Production provider results are reviewed through the bounded audit path. The gate covers objectively auditable code output and a substantive missing deliverable; citable deterministic evidence outranks a contradictory audit, explicit creative-task feedback remains authoritative, and local-only history cannot reach a cloud reviewer. Independent pass, fail and abstain dispositions are durable, while malformed and canceled/timed-out reviews leave typed failed attempts without advisory evidence. An automatic primary failure falls back to a distinct successful task, and only that final result is audited. A versioned SDK evaluator can replace provider-backed review without constructing the reviewer provider: its exact evidence and provenance are admitted durably before one bounded call, malformed or panicking extensions fail safely, and concurrent or restarted replay never invokes it twice. |
| Fallback | A retryable primary failure produces a distinct, linked task on the fallback; partial output, empty output, explicit selection, and exhausted cost budget do not trigger unsafe automatic retries. Hybrid privacy prevents a fallback from crossing locality when local execution is required. |
| User feedback | Explicit feedback updates the exact model/provider/domain/profile fitness projection once; conflicting repeats are rejected. |
| Task intent | Missing task domains are derived only from high-confidence structured evidence: an explicit validator or one unambiguous capability family. Free-form prompt text is never guessed. Canonical domain/profile defaults are applied before skill discovery, routing, evaluation, persistence, and submission hashing; unsafe labels fail before storage or provider construction. A versioned submission contract prevents an upgraded binary from reinterpreting already-queued requests. |
| Resource planning | The Go SDK obtains a versioned, non-mutating capacity recommendation from the same profiler, configured RAM/VRAM limits, adaptive concurrency rule, and live in-process budget used by execution. Randomized and boundary fixtures cross-check every advertised RAM, aggregate-VRAM, and device-VRAM slot against actual reservations under the race detector. Mode, local-required privacy, thermal and explicit swap pressure map to local execution, queueing, offload, or rejection without provider construction or durable writes. |
| Event sink | A configured SDK sink observes detached, redacted runtime events after durable commit from plain and streaming root runs, worker lifecycle, delegated child execution, and newly committed interrupted-model/delegation recovery terminals. Per-task commit/delivery ordering, rotated-secret fail-closed screening, obsolete-configuration recovery, lifecycle-derived callback contexts, callback isolation, typed-nil admission, failure/panic containment, checkpoint-gap catch-up, no automatic redelivery, client reuse, concurrent delivery, explicit replay, and top-level route-chain-scoped per-call stream semantics are race-tested. Reconciliation paths that append no runtime event emit nothing. |
| Global event catch-up | The Go SDK pages every schema-33-admitted committed runtime event in durable SQLite insertion order through a frozen high-water cursor. Schema-33 migration, ordinary and recovery batch atomicity, exact retries, restart polling, task-history completeness, cursor-anchor mismatch, cursor-anchor and same-task position remapping, corruption, cancellation, append/page-size bounds, and no-partial-result behavior are race-tested. Consumption is at least once and deliberately cannot derive a global checkpoint from concurrent live sink callbacks. |
| Provider construction | Provider connections identify discovery, health, auxiliary, and execution purposes. Pre-task discovery remains an admitted control-plane probe, while each selected execution adapter and owned Codex launch occurs only after that attempt's durable, delivered `task.started`, optional route, and `turn.started` boundaries. Start persistence or sink failure prevents construction; construction failure/cancellation is sanitized and terminalized on the same task while journal ownership and persistence remain available, with expired submitted attempts left to reconciliation. Automatic fallback and delegated-child attempts prove the boundary against their own journals. |
| Panic containment | Direct provider and tool extension panics are sanitized at narrow runtime boundaries. Callback persistence, cancellation, and lease loss retain precedence; incomplete tool proposals cannot dispatch; uncertain tool effects are durably paired before failure or cancellation and are never retried. A last-resort per-claim dispatcher guard joins its heartbeat, leaves ambiguous work fenced for ordinary lease reconciliation, retains worker capacity, and never persists the panic value. |
| Session inspection | SDK, CLI, and authenticated HTTP clients can page a metadata-only task graph for one exact session. The cursor is canonical, session-bound, and insertion-fenced; canonical start/head envelopes, page/item session identity, same-session parent links, and bounded safe fallback retry chains are validated before any page is returned. Retry links may cross sessions because separate automatic attempts own separate sessions. Corruption, cancellation, credential collision, malformed requests, service panic, and capacity exhaustion fail closed without inference, mutation, partial data, or content disclosure. |
| Skills | Only the relevant active skill is progressively loaded. Draft and unrelated skills stay out of context, while scope, privacy, redaction, and the kill switch fail closed. Source-derived privacy survives publication and restart; legacy unclassified skills are local-only, and local-only learned workflows are rejected before cloud provider construction while public-only workflows remain usable. Automatic evolution derives a draft from durable successful-workflow evidence, rejects failed or nondeterministic validation, prevents duplicate generation and activation claims under races and restarts, survives a clean service restart, and rolls back a deterministic regression to the prior validated version. |
| Recovery | A completed task survives owner-process `SIGKILL` and is reconciled without another provider call. A separate restart fixture preserves a failed read-only step and accepted user feedback while excluding the repaired trajectory from successful-workflow learning. An actual `nexus serve` process is also killed during a partial local provider stream and restarted against the same SQLite store: the interrupted source is recovered without redispatch, exact completed-history branch and recovered-history resume fences are obtained over authenticated HTTP, denied authentication and browser-origin requests cause no execution, repeated idempotency keys execute each child once, parent/session lineage is preserved, and both source histories remain byte-for-byte unchanged. Queued and expired pre-start work from an obsolete configuration or submission-contract generation is terminalized as `configuration_changed` without execution; a dedicated insertion-fenced cursor skips unrelated history, advances past corrupt candidates with degraded health, and leaves started or otherwise uncertain work fenced. |
| Web UI and Kanban | `make qualify-webui` requires a real Chrome/Chromium binary and exercises authenticated chat/Kanban presentation, hostile-content inertness, the computed accessibility tree, local/cloud/hybrid application paths, local-only egress denial, durable scheduling, daemon restart and uncertain-effect non-redispatch. The supported-browser matrix, explicit limits and manual compatibility checklist are recorded in [Web UI and Kanban qualification](webui-qualification.md). |

These fixtures use production application, provider, routing, resource, session,
skill, evaluation, and SQLite/WAL paths. They are deterministic evidence, not a claim
about external provider availability or model quality.

## Live evidence and limits

The earlier supervised `gpt-5.6-sol` coordinator to Ollama worker round trip is
recorded in [Initial supervised hybrid test](initial-hybrid-test.md). It is not
automatically repeated because it consumes signed-in account usage. That live
checkpoint and the deterministic gate are complementary: the former verifies a
real integration once, while the latter is safe to repeat after every change.

This qualification establishes the current in-process MVP acceptance slice. It
does not qualify every PRD roadmap item, arbitrary external side effects, power
loss, sustained provider availability, model correctness, remote workers, or
post-MVP isolation backends.
