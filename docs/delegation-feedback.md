# Delegated failure feedback

When a dispatched delegation fails, the coordinator receives a bounded
diagnostic envelope instead of treating the rejected candidate as accepted
output. The existing `error` discriminator is retained. If durable evidence can
be verified, additive version-1 fields identify the work, execution and terminal
records. A successful `untrusted_output` envelope may additionally contain a
sanitized `audit` object when `evaluation.judge` and
`evaluation.auto_review_model` are enabled.
It contains only `status`, optional `verdict` and `confidence`, and opaque
`cited_evidence` identifiers; it never copies the review prompt, raw review,
credentials, operation identity, or unrelated history.

Illustrative invalid-output rejection:

```json
{
  "version": 1,
  "error": "delegate_unavailable_or_rejected",
  "reason": "invalid_output",
  "work_task_id": "work-example",
  "execution_task_id": "execution-example",
  "evidence": [
    {"task_id": "work-example", "sequence": 3, "kind": "task.failed", "code": "worker_failed"},
    {"task_id": "execution-example", "sequence": 7, "kind": "task.failed", "code": "invalid_output"}
  ]
}
```

The application replay-validates work ownership and session, execution ancestry,
terminal state and sequence before projecting records. Codes come from a fixed
allowlist; no provider exception string, prompt, candidate output or credential
is copied. The envelope is capped at 2 KiB. Verification gets a separate bounded
one-second read deadline, including during cancellation cleanup.

`reason` normally uses the failed execution's terminal code, such as
`invalid_output`, `empty_output` or `budget_exhausted`. Work cancellation takes
precedence. A terminal `invalid_output` does not identify a particular validator
or prove that compiler/tests ran; inspect the separate validation events for that.
A failure before an execution record exists can identify only the
failed work. A completed execution may be referenced alongside failed work;
execution completion does not establish supervisor acceptance.

Missing, nonterminal, mismatched, corrupted, unknown-code or unavailable evidence
falls back to the original generic rejection. It does not fabricate a cause or
invent a task reference. Admission/schema/call-budget refusals before work starts
also retain that generic response.

These records are diagnostic, not a retry policy. In particular,
`provider_retryable_no_output` is a recorded code, not permission to retry a
delegation with confirmed or uncertain effects. Parent cancellation still
prevents resuming the coordinator, while its rejection record remains durable.

Single delegation and noncanceled batch items share this projection. Batch
cancellation still suppresses all item outputs. A configured per-child audit
runs once, after deterministic validation succeeds and before the supervisor
records acceptance. Its `completed`, `rejected`, `abstained`, `failed`, or
`not_run` status is advisory: it cannot replace deterministic validation or
turn failed, invalid, canceled, interrupted, or uncertain-effect work into a
successful result. Reviewer cancellation or lease loss likewise prevents
acceptance. The durable audit intent and outcome are paired with the worker
record so restart recovery reproduces the same sanitized envelope without
dispatching another review.

Standalone auxiliary audits also verify single and batch result references and
project child validation and terminal metadata; successful results require
durable supervisor acceptance, matching output, and an exact audit
intent/outcome pair when configured. See [audit evidence scope and limits](delegated-audit-evidence.md).
Deterministic evidence remains authoritative. Explicit user feedback retains
precedence for subjective work, same-model positive opinion cannot boost
fitness, and no change is made to model-pruning permissions or automatic retry
behavior.
