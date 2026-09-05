# Recoverable tool failures

The coordinator can receive a failed read or rejected delegated result, then
propose a corrected action. A successful repair does not rewrite the failed
attempt as successful work. This is bounded continuation, not automatic replay
of a tool call and not a general fallback guarantee.

## Trusted outcome contract

`runtime.ToolResult.Failed` describes success separately from side effects.
By default an explicit failure stops the task. A trusted handler may return
`Failed: true, Recoverable: true, Effect: runtime.NoEffect` with a nil Go error
only when it knows no effect occurred. `Recoverable` is invalid on a success or
any other effect. Model text cannot set these execution flags.

The runtime durably records the paired `tool.completed` event with
`code: tool_failed` before allowing a new model turn. Its provider message carries
`ToolFailed: true`. Cancellation, persistence failure, output/turn exhaustion,
generic execution errors, panics, uncertain effects and nonrecoverable outcomes
still stop execution. New model proposals pass the existing schema, policy,
approval, delegation and resource gates; budgets and consumed approvals are not
reset. Built-in `create_file` failures remain nonrecoverable.

## Reads and delegation

Built-in read errors and known safe delegation rejections use this explicit
failure contract. Delegation inspects the child execution journal: missing or
ambiguous executed-child evidence, dispatched pending tools, uncertainty and a
runner panic are not proof of safe completion. They remain terminal uncertainty.
An undispatched proposal is not by itself an executed side effect.

Batch results preserve ordered successful siblings when their outcomes are safe.
Any failed child marks the aggregate failed; repair is permitted only when every
failed child is explicitly recoverable and all effects are known absent. Invalid
or oversized child payloads cannot turn a terminal result into a recoverable one.
Generic errors, panics or unknown effects prevent safe aggregate repair.

## Provider and history behavior

OpenAI-compatible/Ollama requests prefix failed tool content with
`Tool execution failed.` and a newline; no unsupported custom wire field is sent.
The original content follows unchanged. Context estimation includes the prefix.
This text is status information, not a trusted instruction or permission grant.

The signed-in Codex bridge sends the original tool content with `success: false`
for a paused dynamic-tool response. It binds the expected completion status and
boolean to that exact pending call. Imported historical function-call outputs
use the same explicit text prefix because that history item has no success field.
The [official App Server documentation](https://learn.chatgpt.com/docs/app-server)
describes the experimental dynamic-tool request/response lifecycle and completion
status. Protocol/schema-informed fixtures and the opt-in live check below cover
the failure mapping for the pinned CLI version; later versions are not implied.

Session replay derives the failure flag from durable `tool_failed`, not by
parsing arbitrary JSON or error-like text. Branches, safe compaction and serialized
history retain paired failure status. Existing historical error-valued content
without a failure code is not retroactively relabeled or rewritten.

## Audit, learning and remaining limits

The orchestrator audit projection retains failed-step metadata without copying
raw tool arguments/results into execution evidence. A completed repaired task may
receive separate task-level feedback, but `observed_tools_v1` excludes its failed
trajectory from successful procedural grouping. Tests exercise real built-in read
execution, positive feedback, new-Service continuation and clean control workflows.

This does not add compiler/test execution, automatic skill activation, general
write repair, uncertain-effect recovery, coordinator filesystem write authority
or stronger process isolation. Existing subjective-feedback precedence remains
unchanged. Live provider behavior and full PRD acceptance remain separate gates.

## Live qualification

On September 5, 2026, signed-in `codex-cli 0.153.4` with `gpt-5.6-sol` passed:

```sh
DARWIN_CODEX_LIVE_FAILURE_REPAIR=1 go test -race ./internal/codexbridge \
  -run '^TestLiveCodexRecoverableToolProtocol$' -count=1 -v
```

The actual runtime and temporary SQLite journal executed one fixed, effect-free
fixture tool failure. At RPC write time the wrapper verified committed
`tool_failed`/NoEffect evidence; exactly one `success:false` response was sent.
The actual CLI then emitted `status: failed, success: false`, Sol produced
nonempty final output in the second runtime turn, and replay retained exactly
one failed tool step. The live test passed in 6.68 seconds (race-enabled package
run 8.093 seconds). The owner explicitly closed the session on completion.

This is real cloud coordinator inference with synthetic tool output, not local
LLM inference, subjective quality validation, general process containment or a
guarantee of future model behavior. The opt-in command consumes signed-in account
usage. It inherits the checked launcher's existing login and process-local
capability controls without reading/copying credential files or modifying user
configuration. Codex itself may update authentication/state. Test working
directories and SQLite are temporary; diagnostics report bounded protocol
metadata, not raw prompts, arguments or generated output. Ordinary tests skip it.

The same day's application-level live qualifier also passed:

```sh
DARWIN_CODEX_LIVE_REPAIR=1 go test -race ./internal/app \
  -run '^TestLiveCodexDelegationRepair$' -count=1 -v
```

The actual application Service launched one checked Sol coordinator. Its first
delegated loopback worker response was deliberately invalid Go; deterministic
validation rejected it and the parent retained a failed NoEffect tool result.
Sol then proposed a distinct sequential delegation. The controlled worker's
corrected source passed syntax validation and supervisor acceptance; Sol
completed in three runtime turns. The test observed exactly two worker calls,
one failed and one successful parent tool completion, five correctly linked
terminal task records, no pending or uncertain tools, zero remaining worker
leases, exact corrected source in durable history and removal of the temporary
coordinator directory. The test passed in 14.92 seconds (race-enabled package run
16.372 seconds). Generated code was parsed, not compiled or executed.

`make qualify-codex-repair` runs both explicit qualifiers sequentially; it uses
signed-in account usage and is not included in `make check`. A failure stops the
target. Each test has a 90-second context and bounded turns/delegation; these are
not currency or backend billing caps. Both use controlled local results, not a
real Ollama model. Earlier Sol→Ollama success evidence remains separate from
this failure-repair qualification. No production behavior was relaxed to make
the observed failure tuple pass, and no background learning was enabled.
