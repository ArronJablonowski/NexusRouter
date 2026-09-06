# Explicit Sol history continuation

The experimental `codex_app_server` provider now accepts an explicit
`--continue-task` source whose replayed history is eligible for continuation.
The source may be a completed task or an exact recovered-delegation checkpoint.
This starts a new Darwin task and a new ephemeral Codex thread; it does not
resume a stored Codex thread by ID or automatically restart interrupted work.

Completed sources also support explicit [reviewed compacted continuation](codex-compacted-continuation.md).
Recovered failed sources remain eligible only for ordinary uncompacted continuation.

## Protocol and trust boundary

The [official app-server documentation](https://learn.chatgpt.com/docs/app-server)
describes `thread/inject_items` for adding model-visible history without starting
generation. The installed CLI 0.153.4 experimental TypeScript schema was also
checked for the exact request and ResponseItem forms.

Darwin projects its provider-neutral messages into typed native items:

| Source | Native history item |
| --- | --- |
| System/user text | `message` with the original role and `input_text` |
| Assistant text | `message` with `output_text` |
| Assistant tool proposal | `function_call` with original call ID, name, arguments, and `darwin` namespace |
| Recorded tool result | `function_call_output` paired by original call ID |

The final new user message is not injected into the history. Only after the
import returns an empty-object acknowledgement does the adapter send that
message through `turn/start`. Historical tool items are context, not new tool
proposals or execution permission. A new tool proposal must belong to the
current tool catalog and passes the usual durable runtime/permission boundary.
The existing pending-call binding includes the full imported conversation.

Projection rejects invalid roles, unpaired or duplicate calls, malformed JSON,
invalid UTF-8, unsupported tool identities and oversized frames before sending
history. One complete injection frame, including envelope and worst-case thread
ID escaping, must fit 1 MiB. Histories are not silently truncated, summarized or
split into new user instructions. Normal runtime context limits also apply.

Before launch, the application resolves source privacy and re-redacts decoded
message fields using currently configured credentials. This protects against
credential changes since the source journal was written; original events remain
unchanged. Configured memory/skill context on an explicit continuation follows
the existing privacy and context rules and retains its message roles. Fresh
tasks still require one user message; this change does not enable fresh-task
multi-role knowledge injection. Local-only or unknown source privacy cannot be
sent to a cloud coordinator.

Tool output beginning with an object or array delimiter is decoded as bounded,
strict JSON before redacting string values and keys. This catches credentials
escaped inside Darwin's structured delegation envelopes. Duplicate keys,
redacted-key collisions, malformed structured content, excessive nesting and
oversized content are rejected before launch. Plain tool text uses literal
replacement. This is not universal de-obfuscation: encoded JSON inside a string,
base64 and other arbitrary encodings are not recursively interpreted.

Delayed `thread/started` and status notices are accepted only for the new thread.
Known compatibility notices use the same checked-control requirements as normal
streaming. Unexpected requests, unknown notices, wrong identities, failed imports
or malformed acknowledgements close the session without starting its model turn.

## Try a supervised continuation

First inspect history eligibility. Then, for a small non-sensitive source whose
privacy permits cloud processing:

```sh
./bin/darwin task continuation --db ./data/sol-codex-local-smoke.db --task SOURCE_TASK_ID
printf '%s\n' 'Review the saved worker result without repeating work.' |
  ./bin/darwin run --config examples/sol-codex-local-smoke.yaml \
    --model coordinator --continue-task SOURCE_TASK_ID \
    --set workers.delegate_model= --domain smoke
```

The override removes the active delegation tool for this review. It does not
delete historical tool results. No API key is required for this CLI route; it
uses the existing signed-in account and consumes cloud model usage.

## Qualification and remaining limits

Fixtures verify typed role/pair preservation, import-before-turn ordering,
source immutability, no historical tool redispatch, current-secret redaction,
failed-import withholding, and continued enforcement of new tool exchanges.
An application recovery fixture interrupts parent-result persistence after local
work finishes, reconciles the expired claim, and imports the restored result
without repeating local work or changing the source and child journals.

The opt-in live test
`DARWIN_CODEX_LIVE_HISTORY=1 go test ./internal/codexbridge -run '^TestLiveCodexImportedHistory$' -count=1 -v`
passed against Sol using synthetic history and an empty active tool catalog:
one import, one model turn, zero proposals, and the exact saved marker returned.
Initial attempts stopped before generation on delayed startup notices; those
observed orderings now have regression tests.

A subsequent application CLI run continued the earlier real Sol/local-worker
task `6BE4WNSJV2MLSJMAFQSSOJKTKU`. New task `HUXCS3QPL2IPQQDAIIDFN6FXXS`
completed with `42`, correctly reviewing the saved worker's Go function. Reopened
inspection confirms the new task's parent link, cloud-allowed privacy and
completed sequence 6; the source remains completed at sequence 73. Delegation
was disabled for the new task. This was not a new Ollama generation, execution
of that Go code, or a live recovered-crash continuation.

Automatic restart/resume, in-flight steering,
broad model/version compatibility,
full host/process containment, and crash recovery during import remain open.
Live tests are skipped by default. The pinned profile and limits are still
experimental, not a production or privacy-isolation certification.
Bounded [output audits through the Codex provider](codex-output-audits.md) are
now supported separately, as are [skill drafting](codex-skill-generation.md) and
[session-summary drafting](codex-session-summaries.md). Those capabilities do
not imply automatic application; explicit reviewed compacted-history import is
qualified separately in the linked compacted-continuation guide.
