# Sol output audits through the signed-in CLI

The experimental `codex_app_server` provider can now perform NexusRouter's bounded
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
defines `turn/start.outputSchema`. NexusRouter uses it to constrain native review
generation to a closed audit object, pinning evaluator ID, rubric, domain and
allowed evidence references. The schema is included in context estimation.
NexusRouter still independently parses and validates every result, including limits
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
NEXUS__EVALUATION__LLM_JUDGE_ENABLED=true \
  ./bin/nexus audit --config examples/sol-codex-local-smoke.yaml \
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

## Daemon and SDK operation API

The daemon and `sdk/v1` expose the same version-one, restart-safe operation
contract over the application service. HTTP callers authenticate with
`Authorization: Bearer <NEXUS_API_TOKEN>`; browser-origin requests are denied.
Create one operation with:

```http
POST /v1/tasks/TASK_ID/audits
Authorization: Bearer REDACTED
Idempotency-Key: client-generated-key-0001
Content-Type: application/json

{"reviewer_model_id":"coordinator","max_cost":0.10}
```

The idempotency header must occur exactly once and contain 16–128 printable
non-space ASCII bytes. The strict JSON body is limited to 8 KiB and requires
exactly the two shown fields. Task and configured reviewer aliases are bounded,
path-safe IDs; a provider-native model name containing `/` or `:` must remain
behind its configured alias. `max_cost` is a finite, nonnegative admission
ceiling, not proof of provider billing. Query parameters, unknown/duplicate/null
fields, malformed numbers, browser origins and a `Last-Event-ID` header on this
POST are rejected before review dispatch.

A successful POST uses `Content-Type: text/event-stream`. Every `event: audit`
frame contains a versioned `AuditEvent`; its SSE ID is
`<opaque-audit-id>:<sequence>`. Sequence 1 is the durable `pending` admission.
Sequence 2, when present, is the sole terminal state:

- `completed`: the independent reviewer returned `accept`.
- `rejected`: the reviewer returned `reject`.
- `abstained`: the reviewer returned `abstain`.
- `canceled`: durable cancellation won before completion.
- `failed`: the admitted review ended without a valid audit.

If execution fails after sequence 1 without a durable terminal transition, the
stream can end with a fixed-code `event: error` instead of sequence 2. The
operation remains inspectable as pending; the error neither fabricates a result
nor authorizes another provider dispatch.

`terminal_disposition` is empty while pending and repeats the terminal status
otherwise. Terminal review results can include bounded findings, their opaque
evidence references, rubric/domain provenance, elapsed time and provider usage
when reported. A pending or failed result does not fabricate those fields.
Confidence is intentionally absent: model self-assessment is not acceptance
evidence. Every projection carries the fixed precedence
`deterministic`, `tool_result`, `user_feedback`, `llm_judge`. NexusRouter uses
objective checks and tool results ahead of explicit user judgment; for
subjective creative work, user feedback therefore outranks the optional model
judge.

The operation ID is an opaque, domain-separated SHA-256 digest of the caller
key. The raw key is not persisted or returned, and rotating the daemon token
does not change the operation identity. Retrying the exact task, reviewer, cost,
configuration and key replays committed events without another provider call.
Changing that intent under the same key returns HTTP 409. This operation-level
idempotency does not make provider or tool side effects elsewhere safe to retry.

Read and control the operation with:

```text
GET  /v1/tasks/TASK_ID/audits/AUDIT_ID
POST /v1/tasks/TASK_ID/audits/AUDIT_ID/cancel
GET  /v1/tasks/TASK_ID/audits/AUDIT_ID/events
```

Inspection returns one `AuditStatus` as JSON and never creates or migrates a
missing database. Cancellation requires `Content-Type: application/json` and
the exact body `{}`; it changes only a pending audit, while an already-terminal
result wins unchanged. It does not cancel the source task. The events route is
a finite SSE snapshot: omit `Last-Event-ID` for sequence zero, or supply the
canonical `<audit-id>:<nonnegative-sequence>` cursor. It emits committed `audit`
frames followed by an unidentified `checkpoint` containing `from_sequence`,
`next_sequence`,
`head_sequence`, and `has_more`. A pending operation has head 1; a terminal one
has head 2. A syntactically valid cursor beyond that durable head returns a
conflict rather than inventing an event. This is replay, not a live follow
subscription.

The corresponding embedded calls are `Client.RunAudit`, `InspectAudit`,
`CancelAudit`, and `ReadAuditEvents`. Their typed requests, statuses, findings,
usage, provenance, disposition, and callback delivery use the same validation
rules. HTTP and SDK execution are synchronous: the caller connection/context
owns the currently running reviewer. If delivery or the process is lost after
durable admission, restart inspection can still observe `pending`, but NexusRouter
does not guess whether provider inference occurred and never automatically
dispatches that admitted operation again. An exact POST retry or event replay
only returns durable state. Operators may cancel an indefinitely pending
operation after reconciling its external state.

Before any public projection, current configured credentials and sensitive
values are redacted from finding summaries. Identity/provenance fields that
collide with a secret fail closed rather than being rewritten ambiguously. There
are no dedicated source-prompt, candidate-output, tool-payload, provider-endpoint,
credential, raw-error, or idempotency-key fields. However, findings are untrusted
model-generated text and can quote or paraphrase task-derived content that exact
credential redaction does not recognize. Treat the complete status and event
stream as sensitive task inspection. Errors and route explanations do not copy
findings and use closed metadata/error contracts. Audit inspection follows the
task-qualified path, so an operation under a different task is indistinguishable
from an unknown operation.

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
inference. Prompt-only attempts completed but returned non-JSON, which NexusRouter
rejected; the structured-output request passed without weakening the parser.

Fixtures additionally cover durable-before-launch ordering, privacy and cost
denials, schema-aware context admission, cancellation, malformed output, tool
proposals, panic cleanup, automatic-review non-recursion and source/fitness
immutability. Default CI skips live inference. Host/process isolation, broad
CLI-version compatibility, live crash recovery, automatic review of every
delegated child, and complete PRD qualification remain open.

The public daemon/SDK lifecycle uses deterministic and controlled provider
fixtures in its default qualification. It has not been qualified against a live
external HTTP reviewer or paid cloud account. The opt-in signed-in Codex CLI run
described above is narrower evidence and does not qualify arbitrary external
reviewers, production billing, provider availability, or crash behavior during
live inference.
