# Steering the Codex coordinator

The experimental Codex CLI adapter now consumes durable user guidance at the
same safe boundaries as DarwinRouter's provider-neutral loop. Use `/steer TEXT`
in `darwin chat`, the separate `darwin steer` command, or the existing authenticated
HTTP/SDK steering controls. This does not change the queue, policy or permissions.

The runtime commits `steering.applied` before dispatching the next model segment.
That means guidance entered the conversation, not that the model obeyed it. Input
arriving during inference waits until the segment completes or a proposed tool's
result has been durably recorded. No active inference interrupt is introduced.

## Native continuation

- At a paused native tool request, the adapter verifies the exact prior request,
  assistant proposal and paired tool result, followed only by bounded plain user
  messages. It sends `turn/steer` with the exact `expectedTurnId`. Only a matching
  acknowledgement allows the pending tool response to be delivered once. Guidance
  remains user input, never fabricated tool output or permission to rerun work.
- After a native turn completes, the next request must match the complete prior
  conversation including the emitted assistant answer, followed only by guidance.
  The adapter starts a new turn in the same ephemeral thread with that guidance,
  the same model and output schema, and no new environments. It does not launch
  another process, recreate the thread, reimport history or repeat tools.

These controls follow the [official app-server protocol](https://learn.chatgpt.com/docs/app-server).
Installed Codex CLI 0.153.4 generated types also confirm the required active-turn
precondition and `turnId` response. The coordinator remains exactly `gpt-5.6-sol`.

## Limits and failure behavior

Original model, tool catalog, structured-output schema and earlier messages are
immutable. Compaction or arbitrary history replacement is rejected. Existing
task turn/context/permission limits remain in force. The adapter additionally
bounds steering to 32 messages across its lifetime, at most 64KiB per message;
the complete control frame must fit 1MiB. Oversized batches fail, not split or
truncate. Lifetime received-frame/byte and 256-item limits are not reset between
native turns. Thread-scoped item and turn identifiers cannot be reused.

Token usage remains cumulative per native thread; reported segment usage is the
increment since the previous report, not the full total charged again. Missing
usage stays unknown under the existing runtime contract.

Only checked compatibility notices, same-thread status, matching user-item
lifecycle and same-turn monotonic token usage may precede a steering response.
Unknown/action-bearing or incorrectly attributed notices fail closed. A failed,
malformed, mismatched or uncertain response closes the session without retrying
guidance or releasing the pending tool response. The local tool may already have
completed; inspect its durable result before choosing any follow-up. No replay
of confirmed or uncertain effects is authorized.

## Verification scope

Protocol fixtures exercise both native boundaries, exact history/catalog binding,
identity reuse, cumulative usage, pre-acknowledgement notices, lifetime limits,
oversized frames and failure before tool-response delivery. Actual application
tests use a synthetic native wire and owned local HTTP worker with real SQLite
journals: tool results and steering must be committed before native control,
the local worker runs once, and failed steering cannot produce task success.
Cancellation prevents a new native turn. These are not evidence of semantic
obedience, production resilience, broad CLI-version compatibility or automatic
restart/resume.

On September 6, 2026, the opt-in real CLI 0.153.4 / signed-in Sol test passed
both boundaries. The completed case used one thread and two turns. The paused
case used one thread, one turn, one steering control and one synthetic tool
response. Both returned the revised marker with no additional tool proposal;
no local worker or actual tool was run. Together the cases took 15.60s, not a
performance benchmark. No private user history or project files were submitted.

Explicit supervised reproduction (uses account inference usage):

```sh
DARWIN_CODEX_LIVE_STEERING=1 go test -race ./internal/codexbridge -run '^TestLiveCodexSteeringBoundaries$' -count=1 -v
```

Default CI skips this test. Live application/interactive-chat steering with actual
Ollama work, all cancellation/crash windows and broad compatibility remain open.
