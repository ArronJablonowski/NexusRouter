# Signed-in Sol session-summary drafts

The experimental `codex_app_server` provider can generate session-compaction
proposals using the configured exact `gpt-5.6-sol` model and signed-in Codex CLI.
This extends the existing summary command and Darwin-native HTTP summary API;
it does not enable automatic compaction, approve model output, or change the
source journal.

```sh
./bin/darwin summary --config path/to/config.yaml --task SOURCE_TASK_ID \
  --model COORDINATOR_MODEL_ID --keep 6 --max-cost 0.10
./bin/darwin summaries show --db path/to/darwin.db --id SUMMARY_ATTEMPT_ID
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
authority. No automatic retry, fitness update, source redispatch or summary
activation occurs.

## Review and remaining gaps

Inspect the complete draft against the source before using the existing
`summary-review` operation. Approval is an operator decision, not an LLM judge.
Approved drafts can be used by existing compatible continuation providers.
**The native Codex task path still rejects compacted/reviewed-summary
continuations**; this checkpoint adds drafting, not that import qualification.
Uncompacted explicit Codex history continuation remains a separate capability.

Automatic summary validation/application, mid-task compaction, native compacted
history import, summary-attempt crash reconciliation and full PRD acceptance
remain open. The native launch profile is experimental, not a host/process
isolation certification.

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
summary qualification, native summary import or live crash-recovery evidence.
