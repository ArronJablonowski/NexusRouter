# Delegated failure feedback

When a dispatched delegation fails, the coordinator receives a bounded
diagnostic envelope instead of treating the rejected candidate as accepted
output. The existing `error` discriminator is retained. If durable evidence can
be verified, additive version-1 fields identify the work, execution and terminal
records. Successful `untrusted_output` envelopes are unchanged.

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
cancellation still suppresses all item outputs. The standalone auxiliary audit
projection does not yet traverse these child references; that remains separate
work. No change is made here to fitness updates, subjective feedback weighting,
model pruning permissions or automatic retry behavior.
