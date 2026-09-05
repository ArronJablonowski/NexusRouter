# Authenticated Codex coordinator integration

Status: framing, direct-process ownership and paused-tool correspondence primitives implemented; no
launchable DarwinRouter Codex adapter yet. Target model remains `gpt-5.6-sol`.

The installed Codex CLI can use its existing ChatGPT login. DarwinRouter must
not extract tokens from its credential files or silently require direct API
credentials. A local Codex process still performs **cloud inference**.

## Protocol evidence

The [official app-server documentation](https://learn.chatgpt.com/docs/app-server)
describes a JSON-line stdio client and an experimental dynamic-tool request /
response exchange. This integration needs `capabilities.experimentalApi`.
The installed `codex-cli 0.153.4` generated experimental JSON schemas were
inspected on September 5, 2026; these are a version-specific contract, not a
guarantee that later CLI versions remain compatible.

In that schema, `thread/start.dynamicTools` supports a `type: "namespace"`
entry with a name, description and nested tool definitions. Darwin tools will
use the `darwin` namespace. `item/tool/call` identifies `threadId`, `turnId`,
`callId`, `namespace`, `tool` and `arguments`. The outer RPC request ID is
distinct from `callId`. A text tool response contains `contentItems` with
`type: "inputText"` plus a `success` boolean.

## Runtime boundary

The existing runtime owns the following ordering:

1. Receive a provider tool **proposal**, without executing it.
2. Persist the assistant turn and tool-start event.
3. Apply Darwin's tool permissions and run its bounded worker.
4. Persist the tool outcome; fail on errors or uncertain effects.
5. Supply the successful tool message to the next provider invocation.

A Codex adapter must pause the RPC at step 1 and answer it only at step 5.
Executing delegation inside a provider callback would skip these boundaries.
`internal/codexbridge.Pending` binds the proposal to the original model,
catalog, schema, history and exact assistant/tool pair. It returns a single-use
response body, not permission to execute a tool. It rejects altered history,
wrong call IDs, extra steering messages and repeated resolution. Steering and
compaction need explicit future handling instead of silently changing a paused
Codex turn. RPC response delivery failure must abort, never rerun the worker.

`internal/codexrpc` supplies bounded JSON-line framing, strict envelopes,
opaque request IDs, serialized writes and payload-free local errors. Its
`StartProcess` primitive now owns an explicitly configured direct subprocess,
requires absolute executable/directory paths and an explicit environment, and
discards stderr. Cancellation/Close closes blocked pipe operations and reaps
the direct child. The host must defer Close even after normal exit to release
descriptors; OS exit is not a model-completion signal.

This transport is trusted-host plumbing, not the configured Codex launcher.
It does not supervise descendants, enforce an OS sandbox or disable tools.
The direct child uses Go's process lifetime handling; raw numeric process-group
signals are deliberately not used because they can race reaping and PID reuse.
A descendant-containment boundary is still needed for a real runtime that
spawns helpers. The capability, authentication and privacy integration below
is not implemented by an explicit environment alone. Tests use only the test
executable, not Codex, model inference or credential access.

## Required launch and lifecycle work

- Introduce a distinct cloud `codex_app_server` configuration kind. Do not
  fake an HTTP endpoint or weaken the custom HTTP factory's transport contract.
- Finish all privacy admission before process launch, including privacy of a
  resumed session. Current HTTP construction is inert; a subprocess constructor
  must preserve that property or move after admission.
- Give the task explicit ownership of shutdown. The existing HTTP factory does
  not call provider `Close`; a paused subprocess needs cleanup on persistence
  failure, cancellation, iteration limits, rejected tools and normal completion.
- Correlate initialize/thread/turn responses and notifications. Verify final
  completion rather than equating EOF, a text item, or a tool pause with task
  success. Track usage without double-counting across paused segments.
- Disable ambient shell, filesystem, MCP, apps, plugins, hooks, skills and
  auxiliary agents before launch/turn execution. Verify the actual exposed
  capabilities for the supported CLI version. Empty working directories and
  `read-only` sandboxing alone do not prevent reads of unrelated host files.
- Resolve user/project configuration isolation without altering the user's
  existing configuration or copying credentials. The inspected app-server help
  did not expose the `exec --ignore-user-config` flag; do not assume parity.
- Reject unsupported versions/configurations before submitting task data.
  Keep process stderr and remote error payloads out of durable telemetry.

The [configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference)
documents individual tool and feature controls. Those controls have not yet
been qualified as a complete capability-isolation boundary for this adapter.

## Acceptance still required

Test no-launch privacy denials, exact RPC attribution, unexpected server
requests, bounded output, cancellation/cleanup, child-process exit and failed
durable boundaries. Then run the requested supervised live test: Sol proposes
`darwin.delegate`, Darwin authorizes and executes the installed local Ollama
worker, Sol receives its untrusted result and produces a final answer. Preserve
the durable parent/worker/event linkage. The previously successful standalone
CLI and local-worker checks do not prove this integrated workflow.
