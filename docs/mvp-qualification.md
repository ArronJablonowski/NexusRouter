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
| Orchestrator audit | Production provider results are reviewed through the bounded audit path. The gate covers objectively auditable code output and a substantive missing deliverable; citable deterministic evidence outranks a contradictory audit, explicit creative-task feedback remains authoritative, and local-only history cannot reach a cloud reviewer. Independent pass, fail and abstain dispositions are durable, while malformed and canceled/timed-out reviews leave typed failed attempts without advisory evidence. An automatic primary failure falls back to a distinct successful task, and only that final result is audited. |
| Fallback | A retryable primary failure produces a distinct, linked task on the fallback; partial output, empty output, explicit selection, and exhausted cost budget do not trigger unsafe automatic retries. Hybrid privacy prevents a fallback from crossing locality when local execution is required. |
| User feedback | Explicit feedback updates the exact model/provider/domain/profile fitness projection once; conflicting repeats are rejected. |
| Skills | Only the relevant active skill is progressively loaded. Draft and unrelated skills stay out of context, while scope, privacy, redaction, and the kill switch fail closed. Source-derived privacy survives publication and restart; legacy unclassified skills are local-only, and local-only learned workflows are rejected before cloud provider construction while public-only workflows remain usable. Automatic evolution derives a draft from durable successful-workflow evidence, rejects failed or nondeterministic validation, prevents duplicate generation and activation claims under races and restarts, survives a clean service restart, and rolls back a deterministic regression to the prior validated version. |
| Recovery | A completed task survives owner-process `SIGKILL` and is reconciled without another provider call. A separate restart fixture preserves a failed read-only step and accepted user feedback while excluding the repaired trajectory from successful-workflow learning. |

These fixtures use production application, provider, routing, session, skill,
evaluation, and SQLite/WAL paths. They are deterministic evidence, not a claim
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
