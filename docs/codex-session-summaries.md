# Signed-in Sol session-summary drafts

The experimental `codex_app_server` provider can generate session-compaction
proposals using the configured exact `gpt-5.6-sol` model and signed-in Codex CLI.
This extends the existing summary command and NexusRouter-native HTTP summary API;
it does not enable automatic compaction, approve model output, or change the
source journal.

```sh
./bin/nexus summary --config path/to/config.yaml --task SOURCE_TASK_ID \
  --model COORDINATOR_MODEL_ID --keep 6 --max-cost 0.10
./bin/nexus summaries show --db path/to/darwin.db --id SUMMARY_ATTEMPT_ID
```

Use your configured model ID, known context window and cost estimate. The cost
ceiling is an admission check against that estimate, not an account billing cap.
The source must explicitly permit cloud use and have a safe removable prefix;
local/private or legacy-unknown privacy is rejected. Local-only mode cannot
launch this provider. The source model may also be the summarizer because
summarization is not an independent quality judgment. `evaluation.judge` does
not control explicitly requested summaries.

## Execution and acceptance

Provider construction is inert. The application records a durable `started`
attempt, then admits the complete schema-inclusive context before CLI launch.
One one-minute summarizer deadline includes estimation, startup and streaming.
The session uses an invocation-owned private working directory and the existing
checked native launch profile. It receives separate system guidance and an
untrusted user envelope containing the source messages, with no tools.

The native request uses a fresh closed schema with version 1 and six summary
arrays: decisions, requirements, pending work, failures, artifacts and activity.
The host parser independently rejects malformed/extra fields, duplicate keys,
invalid versions, oversized or empty output, and tool proposals. Empty arrays
allow honest abstention without forcing invented details. The host derives all
source identifiers and checkpoint provenance. Generation is structurally
validated, not proven semantically accurate.

The schema is carried by per-turn `outputSchema`, as documented in the
[official Codex App Server reference](https://learn.chatgpt.com/docs/app-server).
HTTP summarizers retain their default schema-off contract and parser support
for omitted categories; the common prompt asks for all six arrays.

Native input processing decodes structured tool output and arguments before
scrubbing configured credentials. Ambiguous duplicate argument keys and excessive
JSON depth are denied rather than silently changing the evidence. It retains
numeric precision and does not modify the source. A credential change affecting
the admitted input during estimation or startup rejects the call before
streaming; output redaction refreshes credentials after inference. This covers
literal and structured JSON string escaping, not arbitrary obfuscation or all
sensitive information.

If a newly configured credential collides with immutable source/model identity
after inference, publication of the draft is rejected. Existing started/source
records keep their historical identities; rotation does not rewrite history.

Success atomically stores a `drafted` attempt. Failure uses the existing bounded
cleanup and generic lifecycle codes. An unavailable store or crash can leave
`started` indeterminate; that is neither proof of a live process nor retry
authority. When the daemon starts, it synchronously scans at most 32 started
attempts before workers can dispatch new work. Its periodic reconciler continues
with bounded pages on roughly five-second ticks. Only an exact guarded local
owner independently proven stopped is closed as `interrupted` with code
`owner_interrupted`; active or unverifiable owners remain unchanged. A startup
scan failure prevents dispatcher startup. No automatic retry, fitness update,
source redispatch or summary activation occurs.

The periodic scan position advances past checked owners so one active or
unverifiable attempt does not starve later entries. Once it reaches the end it
begins a new live scan on a later tick. The public SDK also offers an explicit
1–100-item recovery page with an opaque decimal scan cursor. This mutating
reconciliation operation is distinct from the read-only lexical ID cursors used
to list attempts and recovery receipts. See
[SDK interrupted-attempt recovery](sdk-session-summaries.md#interrupted-attempt-recovery)
for inspection calls and exact cursor semantics.

An interrupted attempt deliberately contains no draft, partial output, token
usage or elapsed measurement. Its separate receipt retains only correlation and
source-checkpoint metadata, state/code and recovery time; it excludes generated
content, provider/model details and private process/lock metadata. Accounting
classifies the summarizer operation as failed with uncertain retry class and no
measured usage. That preserves operational uncertainty rather than asserting
that the provider did no work or that its configured cost estimate is a bill.
Recovery never invokes a provider, creates a review, activates a summary, changes
the source journal or contributes fitness evidence. A draft durably committed
before acknowledgement is retained as drafted and is not recovered again.

## Review and remaining gaps

Inspect the complete draft against the source before using the existing
`summary-review` operation. Approval is an operator decision, not an LLM judge.
Approved drafts can be used by compatible continuation providers, including
explicit [native compacted continuation](codex-compacted-continuation.md).
The native path preserves source/checkpoint binding and the transactional
approval boundary; draft generation itself still does not approve or apply it.

Trusted Go hosts may instead register a bounded set of named deterministic
summary validators and explicitly request validation of an existing draft. A
validation record binds the exact attempt, source sequence and digest, complete
draft digest, validator identity and prior review head. The operation ID is also
the immutable review ID, so retry after a lost acknowledgement returns the
recorded decision without invoking the validator again. Changed validator
semantics require a new identity.

Only an `approved` deterministic decision authorizes later continuation. A
`rejected` or `abstained` decision remains append-only and inactive; a later
authenticated operator review may supersede it through the ordinary
compare-and-swap chain. Validator errors, panics, malformed decisions, source or
draft drift, stale review heads and timeouts produce no review. Callbacks are
trusted cooperative Go code, not a sandbox, and must not perform side effects.
The SDK exposes `NewSummaryValidatorRegistry`, `ValidateSummary`, and the opt-in
`SummarizeTaskValidated` convenience. Draft generation itself remains
single-use; only the validation operation has lost-ack replay semantics.

The SDK now ships `NewSummaryIntegrityValidator` under the stable identity
`darwin.summary.integrity.v1`. It is a conservative deterministic linter, not a
generic semantic validator: it rejects binding/checkpoint drift, unsafe display
controls, duplicate normalized entries, and unsupported unambiguous issue,
revision, or URL anchors. When those checks pass it deliberately returns
`abstained`, never `approved`, because lexical support cannot prove a free-form
summary is complete or semantically faithful. Its bounded note contains counts
and reason codes only, not source excerpts or anchors. A later authenticated
operator may supersede that advisory review through the existing compare-and-
swap chain.

An LLM self-review is not a deterministic validator. Configured unattended
semantic approval, typed per-claim evidence and full PRD acceptance remain open.
Recovery does not resolve provider billing, recover output, authorize retry, or
prove semantic correctness. Owners that are active, remote or cannot be proven
stopped remain fenced. Legacy pre-schema-48 `started` summary rows have no
durable owner proof, remain fenced and unverifiable, and are never automatically
recovered or redispatched. Dedicated CLI/HTTP recovery-receipt inspection and
live signed-in native crash/power-loss qualification remain open. The native
launch profile is experimental, not a host/process isolation certification.

## Qualification

The opt-in test uses one real signed-in Sol call against a synthetic completed
cloud-eligible source served by a loopback fixture:

```sh
DARWIN_CODEX_LIVE_SUMMARY=1 go test -race ./internal/app \
  -run '^TestLiveCodexSummaryDraft$' -count=1 -v
```

It is skipped by ordinary CI. The observed run passed in 6.77 seconds (race
package 8.233 seconds), with one launch, one stream, one close, completion=true
and 490 response bytes. It verified durable inactive output, source provenance,
unchanged source events and removal of the owned working directory. No raw
source/draft content was logged. This is not live Ollama inference, semantic
summary qualification, native summary import or live signed-in crash-recovery
evidence.
