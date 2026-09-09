# MVP qualification

DAR-45 has one deterministic release-candidate gate:

```sh
make qualify-mvp
```

The command uses loopback provider fixtures and disposable SQLite databases. It
does not contact OpenAI or Ollama, consume signed-in account usage, inspect user
conversations, or modify a configured DarwinRouter store.

## Acceptance matrix

| Requirement | Qualification evidence |
| --- | --- |
| Fully local | A local-only task runs through the Ollama adapter, streams a redacted answer, and reaches a durable terminal event; a cloud-designated route is denied. |
| Fully cloud | A cloud-only task runs through the OpenAI-compatible streaming adapter, authenticates through the secret resolver, and reaches a durable terminal event without persisting the credential. |
| Hybrid | A Sol-shaped cloud coordinator delegates bounded work to an isolated Ollama-shaped local worker, receives an untrusted result, reviews it, and preserves parent/work/execution lineage. Privacy-constrained variants deny the cloud coordinator. |
| Orchestrator audit | Production provider results are reviewed through the bounded audit path. The gate covers objectively auditable code output and a substantive missing deliverable; citable deterministic evidence outranks a contradictory audit, explicit creative-task feedback remains authoritative, and local-only history cannot reach a cloud reviewer. Independent pass, fail and abstain dispositions are durable, while malformed and canceled/timed-out reviews leave typed failed attempts without advisory evidence. An automatic primary failure falls back to a distinct successful task, and only that final result is audited. A versioned SDK evaluator can replace provider-backed review without constructing the reviewer provider: its exact evidence and provenance are admitted durably before one bounded call, malformed or panicking extensions fail safely, and concurrent or restarted replay never invokes it twice. |
| Fallback | A retryable primary failure produces a distinct, linked task on the fallback; partial output, empty output, explicit selection, and exhausted cost budget do not trigger unsafe automatic retries. Hybrid privacy prevents a fallback from crossing locality when local execution is required. |
| User feedback | Explicit feedback updates the exact model/provider/domain/profile fitness projection once; conflicting repeats are rejected. |
| Resource planning | The Go SDK obtains a versioned, non-mutating capacity recommendation from the same profiler, configured RAM/VRAM limits, adaptive concurrency rule, and live in-process budget used by execution. Randomized and boundary fixtures cross-check every advertised RAM, aggregate-VRAM, and device-VRAM slot against actual reservations under the race detector. Mode, local-required privacy, thermal and explicit swap pressure map to local execution, queueing, offload, or rejection without provider construction or durable writes. |
| Event sink | A configured SDK sink observes detached, redacted runtime events after durable commit from plain and streaming root runs, worker lifecycle, and delegated child execution. Per-task ordering, callback isolation, typed-nil admission, failure/panic containment, cancellation cleanup, client reuse, concurrent delivery, explicit replay, and top-level route-chain-scoped per-call stream semantics are race-tested. |
| Provider construction | Provider connections identify discovery, health, auxiliary, and execution purposes. Pre-task discovery remains an admitted control-plane probe, while each selected execution adapter and owned Codex launch occurs only after that attempt's durable, delivered `task.started`, optional route, and `turn.started` boundaries. Start persistence or sink failure prevents construction; construction failure/cancellation is sanitized and terminalized on the same task while journal ownership and persistence remain available, with expired submitted attempts left to reconciliation. Automatic fallback and delegated-child attempts prove the boundary against their own journals. |
| Session inspection | SDK, CLI, and authenticated HTTP clients can page a metadata-only task graph for one exact session. The cursor is canonical, session-bound, and insertion-fenced; canonical start/head envelopes, page/item session identity, same-session parent links, and bounded safe fallback retry chains are validated before any page is returned. Retry links may cross sessions because separate automatic attempts own separate sessions. Corruption, cancellation, credential collision, malformed requests, service panic, and capacity exhaustion fail closed without inference, mutation, partial data, or content disclosure. |
| Skills | Only the relevant active skill is progressively loaded. Draft and unrelated skills stay out of context, while scope, privacy, redaction, and the kill switch fail closed. Source-derived privacy survives publication and restart; legacy unclassified skills are local-only, and local-only learned workflows are rejected before cloud provider construction while public-only workflows remain usable. Automatic evolution derives a draft from durable successful-workflow evidence, rejects failed or nondeterministic validation, prevents duplicate generation and activation claims under races and restarts, survives a clean service restart, and rolls back a deterministic regression to the prior validated version. |
| Recovery | A completed task survives owner-process `SIGKILL` and is reconciled without another provider call. A separate restart fixture preserves a failed read-only step and accepted user feedback while excluding the repaired trajectory from successful-workflow learning. |

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
