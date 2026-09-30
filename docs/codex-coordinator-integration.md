# Authenticated Codex coordinator integration

Status: experimental configured provider and one live Sol → Ollama → Sol
round trip verified on September 5, 2026. Target model remains `gpt-5.6-sol`.
Use `examples/sol-codex-local-smoke.yaml` with `--model coordinator` for fresh,
supervised, non-sensitive tasks or [explicit history continuation](codex-history-continuation.md).
Live continuation from the earlier completed Sol/local-worker task has also
passed. Automatic native-session resume after a process crash remains
deliberately fail-closed. Approved, plan-backed compaction can now roll a
completed native turn into a new checked session; deterministic qualification
has passed, while the dedicated live rollover check remains opt-in.
Durable [boundary steering](codex-steering.md) is now supported, with live
CLI 0.153.4 checks for both a completed native turn and a paused synthetic tool.
Historical sections below record earlier
implementation stages; they do not imply production qualification.

The live effect-free tool-failure protocol also passed on CLI 0.153.4: the actual
runtime committed `tool_failed` before sending `success:false`, observed Codex's
`failed/false` completion, then received a final Sol response. See
[recoverable tool qualification](recoverable-tool-failures.md#live-qualification)
for opt-in reproduction and the distinction between controlled worker results
and actual local-model inference.

The current pinned profile enables only `skip_host_skill_discovery` and
`code_mode_host` among its 134 feature controls. The latter is necessary for
the observed Sol tool path; the bundled host must be installed. A disabled-host
warning is a failure, not an informational notice to ignore. Explicit disabled
MCP/plugin/skill/hook inventories remain required. Exact known feature notices
are discarded without logging their text. Sparse account rate-limit objects
are also discarded, never interpreted as task usage or available spending.

See [the live evidence and reproduction command](initial-hybrid-test.md#first-successful-end-to-end-checkpoint).
Normal completion left no observed standalone Codex process in the subsequent
process snapshot, but the brief snapshots did not capture the helper's exact
lifetime. General descendant containment, cancellation across timing windows,
inherited prompt/built-in tool isolation and production readiness remain unqualified.

A subsequent macOS live cancellation test observed both the task-owned Codex
process and its Code Mode host at a blocked delegated HTTP request. Durable
cancellation stopped the request, persisted all three canceled task records,
and both observed processes disappeared. See the live cancellation checkpoint
in the testing guide. This is bounded evidence for that timing window, not
universal descendant containment or graceful upstream interruption.

The installed Codex CLI can use its existing ChatGPT login. NexusRouter must
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
entry with a name, description and nested tool definitions. NexusRouter tools will
use the `darwin` namespace. `item/tool/call` identifies `threadId`, `turnId`,
`callId`, `namespace`, `tool` and `arguments`. The outer RPC request ID is
distinct from `callId`. A text tool response contains `contentItems` with
`type: "inputText"` plus a `success` boolean.

## Runtime boundary

The existing runtime owns the following ordering:

1. Receive a provider tool **proposal**, without executing it.
2. Persist the assistant turn and tool-start event.
3. Apply NexusRouter's tool permissions and run its bounded worker.
4. Persist the tool outcome; stop on errors, uncertain effects or nonrecoverable failures.
5. Supply the successful or explicitly recoverable effect-free failed tool message
   to the next provider invocation, preserving its failure status.

A Codex adapter must pause the RPC at step 1 and answer it only at step 5.
Executing delegation inside a provider callback would skip these boundaries.
`internal/codexbridge.Pending` binds the proposal to the original model,
catalog, schema, history and exact assistant/tool pair. It returns a single-use
response body, not permission to execute a tool. It rejects altered history,
wrong call IDs, extra steering messages and repeated resolution. Compaction is
still forbidden while a native tool RPC is paused. At a completed native turn,
the adapter may instead perform the checked rollover described below. RPC
response delivery failure must abort, never rerun the worker.

## Plan-backed context rollover

Codex mid-task compaction is admitted only from a current version-two reviewed
context-compaction plan. Legacy in-memory approval is not enough. The runtime
first asks the active session to verify, without mutation, that its exact native
turn is finished and that the prospective replacement is a valid import ending
in committed user guidance. Tool-proposal segments cannot cross this boundary.

SQLite then atomically commits `context.compacted`, the exact live-suffix
evidence, and the plan's activated lifecycle fact. Only after that transaction
succeeds may the task owner close the old session. A close error is ambiguous
and poisons the owner; no replacement is launched. The next durable turn lazily
opens a fresh checked app-server process, imports the compacted prefix plus the
exact live suffix, verifies the import acknowledgement, and only then sends
`turn/start`. The first replacement request must retain the prospectively
checked steering prefix byte-for-byte. Later errors, cancellation, malformed or
missing acknowledgements, and process failure never reopen or retry a generation.

Historical tool calls/results are typed inert import items. Runtime lineage also
retains removed tool-call identities, so a replacement model cannot redispatch
an old call ID. Interrupted tasks remain terminal on recovery rather than
reconstructing uncertain native work.

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

`internal/codexbridge.Session` now drives initialization, ephemeral thread and
turn creation, item/text streams, paused dynamic calls and final completion
over an already-admitted `Wire`. It implements the provider contract but is
not installed in the HTTP factory or application configuration. Construction
does not send traffic. The host owns the wire and must close the session after
the entire runtime loop, including failures after a proposal is returned.

The prototype accepts one fresh user message, the configured model/tool catalog,
an optional output schema, and exact subsequent tool-result continuations.
It rejects historical import/steering instead of flattening message roles.
It snapshots input before callbacks, rejects ambiguous control keys, and checks
thread/turn/item identity and terminal status. Text deltas stream immediately
and must agree with the completed item. Reasoning frames are attributed and
bounded but are not emitted as answer text. Usage is reported as differences
between observed cumulative totals; missing or late usage is not invented.
There are limits of 4,096 frames, 16 MiB wire data, 256 items and 64 queued
startup frames per session. Unsupported protocol notifications fail closed.

The actual runtime loop and SQLite journal are exercised by controlled-wire
tests: no tool RPC response is sent before successful tool-result persistence;
injected tool-start/result persistence failures preserve the unresolved durable
boundary and prevent a response. This is not a live Codex/local worker test.

## Required launch and lifecycle work

- Introduce a distinct cloud `codex_app_server` configuration kind. Do not
  fake an HTTP endpoint or weaken the custom HTTP factory's transport contract.
- Finish all privacy admission before process launch, including privacy of a
  resumed session. Current HTTP construction is inert; a subprocess constructor
  must preserve that property or move after admission.
- Give the task explicit ownership of shutdown. The existing HTTP factory does
  not call provider `Close`; a paused subprocess needs cleanup on persistence
  failure, cancellation, iteration limits, rejected tools and normal completion.
- Qualify the implemented initialize/thread/turn and item correlation against
  the actual CLI, including notification ordering and usage arriving late.
  Keep historical session import, steering, and completed-turn rollover
  qualification current without weakening paused-tool correspondence checks.
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

### Observed launch configuration (CLI 0.153.4)

An opt-in no-inference probe now performs only initialization and `config/read`
in an empty temporary working directory. It uses the existing login locations,
an explicit environment allowlist and process-local configuration overrides;
it does not copy credentials or edit user configuration. Startup can still
refresh authentication, initialize extensions or write Codex state.

The probe requested all 135 advertised features disabled except
`skip_host_skill_discovery=true`. The effective configuration exposed 134
matching features; `apps_mcp_path_override` was absent and remains unknown.
Project-document loading, notify commands and web search were observed disabled.
However, `mcp_servers={}` and `plugins={}` left two MCP and twelve plugin entries
in the merged configuration. Empty-table overrides are therefore not evidence
of isolation. Entry counts alone also do not prove which tools are callable.
No thread or turn was created, and no task data was submitted.

Strict configuration rejected the legacy `tools.view_image` key; the probe
uses `features.view_image=false`. A real notification included `emittedAtMs`,
absent from the generated base envelope schema. The decoder now accepts this
only as a nonnegative integer on notifications, never as ordering authority.
The probe discards notification payloads and prints only projected metadata.
Default tests skip this live diagnostic; opt in with
`DARWIN_CODEX_LIVE_PROBE=1 go test ./internal/codexbridge -run '^TestLiveCodexLaunchConfigProbe$' -count=1 -v`.

### Explicit extension and skill controls

The probe now closes discovery before starting a second process with explicit
per-entry MCP/plugin disables. Overrides use sorted, quoted literal TOML keys
inside table values, preserving identifiers containing dots or `@` without
turning them into CLI key paths. The helper copies no original commands, URLs,
environment settings or credentials. Counts, identifiers and generated output
are bounded; returned arguments contain private identifiers and must not be
logged. Missing catalogs and malformed entries fail with static errors.

The second process observed both MCP entries and all twelve plugins explicitly
disabled. Its MCP inventory listed two servers with zero tools; hooks inventory
was empty, without errors or warnings. However, skill inventory still listed six
enabled skills despite `skip_host_skill_discovery=true`. A third process applies
`skills.config` entries disabling every observed skill at its exact path. It
reported all six disabled, zero MCP tools and zero hooks. No user configuration
was written. Skill paths and descriptions are withheld from diagnostic output.
Inventory errors, unknown enablement and new entries must never be interpreted
as disabled; a later real launcher must repeat checks on its own live process.

The [configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference)
documents MCP and skill enablement controls. The observed installed CLI also
accepted plugin-level `enabled=false`. The [app-server reference](https://learn.chatgpt.com/docs/app-server)
documents the inventory calls used here. These calls are not a complete catalog
of built-in model tools, nor proof of process containment or prompt isolation.
The probe still submits no thread, turn or task data. CLI metadata capture is
now bounded during reading rather than checked only after allocation.

These observations do not admit the launcher for inference. Built-in tool/context
verification, application wiring and lifecycle containment remain open.

### Same-wire session preflight

`NewCheckedSession` accepts a trusted host feature profile and an already-owned
wire. Construction is inert. `Prepare` performs initialization once, then checks
effective configuration and MCP/skill/hook inventories on that same connection
without creating a thread or sending input. `Stream` uses the same path and
rechecks immediately before thread creation even after an earlier `Prepare`.
Control request IDs are distinct across checks; a prior successful observation
is not a reusable permission token. Failures close the connection and cannot
answer a server tool request or send task data.

Checks require exact feature values, explicit per-entry disables, zero MCP
tools, disabled skills, empty hooks/errors/warnings and matching skill/hook cwd.
Missing inventories, pagination, aliases and malformed fields fail closed.
These are configuration/inventory checks, not atomic protection against external
configuration changes or a complete built-in capability attestation. The host
must supply the full supported feature profile and enforce privacy before launch.

Installed CLI metadata identifies `apps_mcp_path_override` as **removed**; the
pinned diagnostic now omits that obsolete override. All 134 remaining supplied
feature controls are observed with their expected values. The unsolicited
startup notice is `remoteControl/status/changed`; checked RPC calls accept only
an explicit `disabled` status and discard identity metadata. Other statuses or
unrecognized traffic fail. A disabled notice during later answer streaming still
fails closed; that availability case is not qualified by startup observations.

The opt-in diagnostic adds a fourth process using the actual checked-session
`Prepare` path. It passed with the existing login and submitted no thread, turn
or user input. Controlled-wire tests separately verify Prepare-to-Stream reuse,
fresh checks after preparation, changed-state rejection, cancellation and no
task-data release after failed checks. Real inference and integrated delegation
remain pending, as do task-owned launcher/application integration and containment.

### Task-owned internal launcher

`LaunchChecked` now performs the launch sequence and returns the checked session.
It requires explicit `hybrid`/`cloud_only` mode and `cloud_allowed` privacy before
any metadata subprocess starts. The host must resolve session privacy first;
setting a string here is not a substitute for that application policy decision.
The experimental profile is pinned to `gpt-5.6-sol` and CLI `0.153.4`.

The launcher validates an absolute executable, an empty host-owned cwd, and an
explicit environment limited to HOME, PATH, TMPDIR and CODEX_HOME. It preserves
existing login locations without reading/copying credential files. CLI metadata
is captured with a 64KiB limit, sanitized errors and a bounded pipe wait. The
profile validates the actual 135-row metadata format, including two-word
`under development` stages, and builds deterministic feature/control arguments.

A discovery process initializes and reads configuration/skills without starting
a thread. It is closed before the final process launches with explicit extension
and skill disables. The final process runs checked-session preflight on its own
wire. Discovery and setup have a 15-second deadline; the returned session uses
the task's lifetime instead, and the task owner must defer Close over the entire
agent/tool loop. Errors clean up all successfully owned connections.

The opt-in `TestLiveCodexCheckedLauncher` passed against the signed-in CLI,
including a second preflight after setup returned. It sent no model request.
Default CI skips live launch tests. This remains an internal experimental entry
point: application provider configuration, built-in tool/context qualification,
descendant containment and integrated live delegation are still outstanding.

### Explicit application task integration

Configuration now accepts an experimental `codex_app_server` provider with an
absolute `executable`, no HTTP endpoint and no API-key environment setting. Its
models must have cloud locality, positive context capacity and the pinned Sol
model name. Executable paths are redacted in configuration display. HTTP provider
contracts remain unchanged and reject executable settings.

Explicit task execution constructs the selected provider after resolved history
privacy and supported context-shape checks. Codex currently requires one fresh
user message; history import and multi-role memory/skill context are rejected
before launch rather than flattened. The task owns the returned provider and
closes it across model/tool/journal errors, then removes only its own temporary
working directory. Context-size estimation still runs in the runtime before
inference. HTTP construction also occurs later now, so failed provider creation
can leave an initialized task database even though no task was started.

Fixture tests exercise real application delegation to a local HTTP/Ollama fixture
with a simulated Codex provider: durable parent/child linkage, local privacy,
result return and task-owned cleanup pass. This does not qualify live protocol
behavior. Automatic model discovery/health and auxiliary model use do not launch
Codex and are not supported for this kind yet; the sample selects an explicit model.

The first real application attempt failed before delegation. An opt-in inference
diagnostic reached thread/turn creation and isolated the rejection to queued
deprecation/warning notifications. The adapter still rejects them. Known feature
mentions include deprecated landlock/web-search flags, host skill discovery and
Code Mode host configuration. Their semantic impact must be understood before
adding narrowly scoped handling. `TestLiveCodexProposalProtocol` is separately
gated by `DARWIN_CODEX_LIVE_INFERENCE=1`, makes a real model attempt, and never
executes or answers a proposed tool; default CI skips it. It currently fails,
while no-inference launcher qualification passes. Full live delegation remains open.

Test no-launch privacy denials, exact RPC attribution, unexpected server
requests, bounded output, cancellation/cleanup, child-process exit and failed
durable boundaries. Then run the requested supervised live test: Sol proposes
`darwin.delegate`, NexusRouter authorizes and executes the installed local Ollama
worker, Sol receives its untrusted result and produces a final answer. Preserve
the durable parent/worker/event linkage. The previously successful standalone
CLI and local-worker checks do not prove this integrated workflow.
