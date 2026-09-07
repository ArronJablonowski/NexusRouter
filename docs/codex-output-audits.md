# Sol output audits through the signed-in CLI

The experimental `codex_app_server` provider can now perform Darwin's bounded
output audits, using the existing Codex login and `gpt-5.6-sol`. No separate API
key is needed. This is the same advisory audit service used by HTTP reviewers,
not Codex's repository-oriented `review/start` operation.

## Review contract

The application checks source completeness, privacy, reviewer identity,
estimated cost, resource requirements and bounded execution evidence first.
It then persists a review attempt before estimating the assembled request or
launching a CLI process. Context-estimator failure, context overflow and failed
attempt persistence cannot start native inference.

Each review owns one checked CLI session and a private temporary working
directory. Its one-minute review timeout includes startup and inference. The
session receives a trusted system rubric as a typed history item, followed by a
user JSON envelope containing untrusted requirements, candidate and evidence.
The active tool catalog is empty. Tool proposals, malformed output and failures
produce no accepted audit and are not automatically retried. The provider is
closed and the invocation-owned directory removed on completion or failure.

The [official app-server documentation](https://learn.chatgpt.com/docs/app-server)
defines `turn/start.outputSchema`. Darwin uses it to constrain native review
generation to a closed audit object, pinning evaluator ID, rubric, domain and
allowed evidence references. The schema is included in context estimation.
Darwin still independently parses and validates every result, including limits
that are stricter than the generation schema. HTTP reviewers retain their
existing default request format; embedded `evaluation.Reviewer` callers may opt
into `StructuredOutput` explicitly.

Current credentials are redacted before review. Structured historical tool
objects/arrays are decoded before scrubbing escaped string values and keys;
ambiguous keys, collisions, malformed JSON and oversized/deep payloads fail.
Original source journals remain unchanged. This is not arbitrary encoding
detection, and an audit may still contain other sensitive task information.

## Supervised use

Choose a completed, cloud-eligible task. An independent configured reviewer is
preferred, but Sol may run a separate bounded review invocation over its own
final attempt. Same-model agreement is not positive evidence; only a rejection
can become a capped advisory warning. Do not use a local-only or legacy
unknown-privacy task. Cloud eligibility is not inferred from changing the
configuration after a task was written.

With the existing sample profile, enable judging only for the command:

```sh
DARWIN__EVALUATION__LLM_JUDGE_ENABLED=true \
  ./bin/darwin audit --config examples/sol-codex-local-smoke.yaml \
  --task CLOUD_ELIGIBLE_TASK_ID --reviewer coordinator --max-cost 0.10
```

The sample's estimate is admission metadata, not a signed-in-account billing
cap. This command consumes model usage. The CLI prints the stored audit ID;
existing audit-inspection commands expose its findings and lifecycle.

For post-completion automatic review, configure `evaluation.auto_review_model`
and `evaluation.auto_review_max_cost`, and enable `llm_judge_enabled`. This runs
synchronously after a successful task, never recursively. Review failure leaves
the candidate's completed state and text intact and reports a failed audit
status. Admission still denies private histories. Same-model review does not
recursively review the review invocation.

Audits do not rewrite task outcomes or measured fitness evidence. The existing
routing policy may consume their separate, bounded advisory signal; creative
and unknown domains receive less weight, same-model accepts and abstentions are
excluded, same-model rejections are capped at 0.25 confidence, and direct
evaluation/user feedback supersedes that attempt's audit signal. A review is
not a compiler, test runner, skill activation, model-pruning decision or proof
of correctness.

## Qualification and remaining work

The opt-in live test is:

```sh
DARWIN_CODEX_LIVE_AUDIT=1 go test ./internal/app \
  -run '^TestLiveCodexTaskAudit$' -count=1 -v
```

On CLI 0.153.4, Sol rejected a synthetic candidate that only promised to produce
a requested Go function later. The validated audit contained two findings and
was persisted without changing the source's six events. The source was produced
by a loopback provider fixture declared cloud-eligible, not actual local-model
inference. Prompt-only attempts completed but returned non-JSON, which Darwin
rejected; the structured-output request passed without weakening the parser.

Fixtures additionally cover durable-before-launch ordering, privacy and cost
denials, schema-aware context admission, cancellation, malformed output, tool
proposals, panic cleanup, automatic-review non-recursion and source/fitness
immutability. Default CI skips live inference. Host/process isolation, broad
CLI-version compatibility, live crash recovery, automatic review of every
delegated child, and complete PRD qualification remain open.
