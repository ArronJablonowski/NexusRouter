# DarwinRouter

A Go-based, local-first agent runtime with adaptive model routing. The product specification is in [DarwinRouter_PRD.md](DarwinRouter_PRD.md). The repeatable release-candidate gate is documented in [MVP qualification](docs/mvp-qualification.md).

## Development status

Local release preparation now supports reproducible four-target archives,
checksummed manifests, explicit Ed25519 signing and offline trusted-key
verification. See [release packaging](docs/release-packaging.md) and
`make qualify-release`. No release has been published; distribution identity,
license approval and full runtime qualification remain separate gates.

The executable supports layered configuration, automatic or explicit-model tasks, line-oriented interactive chat, and an authenticated loopback HTTP service with durable SQLite/WAL history. Provider calls use an allowlisted transport, with loopback-only enforcement for local models. Operator memory and skill commands, opt-in local read tools, reviewed new-file creation and bounded existing-file replacement are available. Interactive CLI chat, the OpenAI-compatible HTTP endpoint and Go SDK support provisional, incrementally redacted live text. General patch editing, delegated writes and unattended write approvals remain unfinished; reviewed custom Go SDK tools have separate safety requirements. See the implementation evidence for remaining work; this is not a released MVP.

Application tasks reject empty or whitespace-only final answers with a durable deterministic failure; tool-only intermediate messages remain valid. Independent model audits can run manually or automatically and remain advisory. Explicit user revisions of subjective evaluation records preserve history and avoid duplicate fitness samples.

Known effect-free read/delegation failures can return explicit failure evidence
to the coordinator for a fresh, bounded repair turn. Failed steps remain in the
audit history and cannot become successful workflow-learning evidence. This is
not automatic replay of tools or permission to retry uncertain writes. See
[recoverable tool failures](docs/recoverable-tool-failures.md).

Trusted SDK tools can declare read-only, idempotent-write or non-idempotent-write
behavior. The runtime binds it to execution history and approvals; both write
classes retain per-call review and a writer lease, without automatic replay.
See [declared tool behavior](docs/tool-behavior.md).

Application tools now coordinate shared reads and exclusive writes through
durable leases, including nested filesystem roots. See
[reader/writer execution](docs/reader-writer-execution.md) for the cooperative
boundary, upgrade requirements and unresolved crashed-holder reconciliation.

Automatic routing separately tracks nonempty-output validity from the latest 100 checked terminal attempts per model/provider/domain/profile. It verifies each check against saved final output and task status. Failures discount the quality component using sample confidence and recency decay; passing this check never proves semantic quality or creates cost/latency measurements. This objective signal remains active when LLM judging is disabled. Legacy tasks without a check contribute no assumed outcome. Compiler/test validation and broader objective checks remain unfinished.

For Go-generation tasks, opt into `darwin run --config path --model auto --validate go_source < prompt.txt`, or supply `"validation":"go_source"` to native `POST /v1/tasks`. Ask for a raw complete Go source file: prose and Markdown fences are rejected, not extracted. The check parses at most 1 MiB of UTF-8 source without loading imports, compiling, or executing anything. Invalid syntax fails the task and records objective evidence; syntax-valid code can still have type errors, missing dependencies, security bugs or failing tests. Validation uses the redacted output that is persisted and delivered. Validity populations are separated by requested validation mode; the OpenAI-compatible endpoint does not expose this extension.

Read-only model recommendations are available with `darwin models deprecation
--config config.yaml --model MODEL_ID`. The report requires sufficient persisted
evaluation evidence and operator approval for any later model changes; it does
not automatically prune models. The same inspection is exposed through authenticated
`POST /v1/models/deprecation` and the Go SDK's `Client.ModelDeprecation`.
See [model deprecation](docs/model-deprecation.md).

`darwin daemon start|status|stop --config config.yaml` now controls a background
daemon on macOS/Linux using authenticated, instance-bound loopback requests.
Set `DARWIN_API_TOKEN` first. See [daemon lifecycle](docs/daemon-control.md) for
readiness, shutdown semantics and limits; `serve` remains the foreground option.

### Background skill learning

The daemon can now scan repeated accepted tool workflows and publish generated
skills as inactive drafts. It requires explicit `skills.learning.enabled: true`
and an aggregate generation budget; it remains off by default. Inspect its
durable cursor with `darwin skills learning status --config config.yaml`.
See [background learning](docs/background-learning.md) for setup, recovery,
privacy boundaries and validation requirements.

Trusted Go hosts can register a named deterministic validator and explicitly
start learning plus durable regression monitoring through the shared
[configured lifecycle](docs/configured-learning-supervision.md). The stock binary
ships no qualified domain validator and fails closed on unknown selections.

`darwin task skill-outcome --config config.yaml --task TASK_ID` inspects recorded
fresh skill versions and current task feedback without running anything. It keeps
quality evidence separate from nonempty-text/Go-syntax checks. The same report is
available through the SDK and authenticated HTTP API; see
[skill outcome attribution](docs/skill-outcome-attribution.md).

`darwin skills compare --config config.yaml` accepts a bounded JSON task-set
request on stdin and compares two recorded skill versions using current quality
evidence. Reports are advisory; creative tasks require user feedback and no model
judge can qualify. See [outcome comparison](docs/skill-outcome-comparison.md) for
the SDK/HTTP interfaces, sample requirements and statistical limitations.

`darwin skills compare-select --config config.yaml` automatically selects bounded
latest exposure windows before examining outcomes. See [selection rules and
schema27 migration](docs/skill-comparison-selection.md). This remains diagnostic;
it cannot automatically roll back a skill.

Trusted Go hosts can separately opt into operation-bound
[outcome-policy rollback](docs/skill-outcome-rollback.md). It is disabled by default,
requires an exact first-activation/predecessor binding, and preserves a durable
receipt for rollback or no-action decisions. A durable preselection intent fences
failed or interrupted attempts against automatic reselection; trusted hosts can
inspect intents, selected-evidence checkpoints and receipts. Exact retries can
finish from a saved checkpoint only after its fixed source tasks still match
current evidence, policy and activation checks. Corrected feedback blocks
completion without selecting replacement tasks. No daemon loop starts automatically.

### Managed local model residency

Dedicated Ollama endpoints can opt into `manage_residency: true` for inspected,
confirmed low-memory model switching. This requires exclusive use by one
DarwinRouter Service; shared servers must leave it disabled. Logical reservation
release alone does not unload a model. See [model residency](docs/model-residency.md).

### Replaceable context planning

Embedding applications can replace context assembly, estimation and compaction
selection through `ConfigOptions.ContextEngine`. Optional memory/skill bundles
can be reordered or omitted without rewriting protected history or tool pairs.
See [context planning engines](docs/context-engine.md) for actual execution
wiring, summary approval boundaries and a Go SDK example.

### Scoped memory in task context

Stored facts can now enter task context when an operator configures a scope:

```yaml
memory:
  enabled: true
  scope: my-project
  local_only: true
  max_facts: 8
  max_bytes: 16384
```

The default empty scope disables retrieval. Facts come from the same configured
SQLite database used by `darwin memory` commands. Retrieval pages through scoped,
unexpired candidates (at most 1,024 facts, 8 MiB and a cooperative three-second
deadline). It ranks positive unique keyword overlap with the current user request,
then confidence and ID, selecting at most `max_facts` whole facts fitting the
serialized message budget. Unmatched facts are omitted. This is deterministic
local lexical retrieval, not semantic search; exceeding scan bounds fails admission.
Known credentials are
redacted before model dispatch. Context includes fact ID, revision, provenance,
and confidence in a JSON data envelope, preceded by a fixed instruction that
memory is untrusted factual context, never tool or policy authority.

With `local_only: true`, nonempty memory context pins automatic hybrid tasks to
local models. Explicit cloud tasks and cloud-only mode omit memory. With
`local_only: false`, cloud-eligible automatic routing loads only shareable facts;
explicit local models may also retrieve private facts. Context admission and
execution share one selection snapshot. Before its first admitted model dispatch,
SQLite validates each selected fact's complete identity and updates last-use;
correction, deletion or expiry detected then blocks dispatch. A later deletion
cannot recall an in-flight request; the next fresh task retrieves current facts. Continuations
retain historical messages, including previous memory snapshots: deleting a fact
or disabling retrieval does not erase its copies from session history. Memory
inspection remains read-only. Last-use means a dispatch attempt, not verified
provider receipt; see [memory retrieval and use](docs/memory-retrieval.md) for
snapshot races and custom-store behavior. Retrieval does not create/correct facts
automatically. Prompt separation is not proof of injection immunity;
tool permissions remain enforced independently.

### Procedural skills in task context

```yaml
skills:
  enabled: true
  root: /absolute/private/skill-store
  scope: my-project
  local_only: true
  max_skills: 3
  max_bytes: 16384
  generation_budget:
    enabled: false
    window: 24h
    max_cost: 0
    max_attempts: 10
    max_in_flight: 1
    cooldown: 1h
```

Root and scope are empty by default, so loading is opt-in. The configured root
must already exist and satisfy the store's private-directory and no-symlink
checks. A task's `--domain` (or native API `domain`) selects an exact skill tag;
an omitted domain uses `general`. Discovery reads metadata first, then loads
only matching active versions up to the candidate limit. Drafts are never
loaded. Active versions require prior activation through the store's trusted
deterministic-validator interface; creating a draft with the CLI is insufficient.

Skills requiring unavailable tools are skipped. Whole workflows must fit the
serialized message budget; no workflow is truncated. The context contains
version/provenance, steps, configuration, tool requirements and risks as
untrusted JSON data, with configured credentials redacted. It cannot grant tool
access or approval. Automatic routing and execution share the same snapshot.

Local-only skill context pins hybrid tasks local and is omitted for explicit
cloud/cloud-only tasks. Setting `local_only: false` authorizes sharing selected
content from that scope with configured cloud models. Loading never drafts,
activates or revises a skill. Explicit generation, validated activation and
deterministic regression rollback are available to trusted hosts; opt-in Go-host
learning and regression supervisors can schedule those operations. Standalone
daemon validator configuration, production workflow validators and semantic relevance selection
remain unfinished. As with memory, old skill snapshots remain in continuation
history; changing an active version does not rewrite prior session messages.

Advisory audits now influence automatic routing quality with bounded weight: creative/unknown domains receive less influence than coding/math/structured-output domains. Only the newest review per attempt counts; abstentions do not score, and direct evaluation/user feedback excludes that attempt's audit signal. Audits do not become measured execution samples or change cost/reliability statistics. `llm_judge_enabled: false` disables both review calls and advisory routing influence.

## Build and verify

Requires Go 1.27.1 and Make. SQLite uses the pinned pure-Go `modernc.org/sqlite` dependency; no C compiler is required for ordinary builds. Race tests require the platform's supported race-detector toolchain.

```sh
make check
make build
./bin/darwin help
./bin/darwin version
```

`make check` checks formatting without rewriting files, enforces the 1,000-line maximum on handwritten Go files (including tests), runs `go vet`, tests with the race detector, and builds every package. `make fmt` intentionally rewrites Go formatting. CI runs the same checks on Linux and macOS once this repository is pushed to GitHub.

## Layout

- `cmd/darwin`: thin executable entry point.
- `internal/cli`: command parsing, output, and exit behavior.
- `internal/config`: typed YAML settings, merging, overrides, and validation.
- `runtime`: versioned event envelope, event kinds, and validation.
- `providers`: streaming contracts, HTTP adapters, bounded protocol parsing and local conformance fixtures.
- `routing`: eligibility filters, normalized evidence ranking, bounded exploration and fallback selection.
- `tools`: schema-validated registry and scoped read-only authorization boundary.
- `policy`: owned HTTP transport with endpoint allowlisting and loopback-only egress mode.
- `memory`: factual-memory contracts backed by SQLite, with provenance and scoped privacy-aware queries.
- `skills`: private local versioned procedural workflows with validation-gated activation and rollback.
- `internal/telemetry`: SQLite migration, atomic event append, and paginated replay.
- `cmd/check` and `internal/quality`: source quality gates.
- `docs/architecture.md`: package boundaries and implementation sequence.

The module path is `github.com/ArronJablonowski/DarwinRouter`. The development
[Go SDK](sdk/v1/README.md) embeds the same application service through `sdk/v1`;
see [the compilable example](examples/sdk/main.go). It is not yet a tagged stable
release, and full application-level extension contracts remain unfinished.

SDK `InspectTask`, CLI `task show` and the daemon task-inspection route reconstruct
one bounded SQLite snapshot (at most10,000 events/8MiB of serialized history).
Inspection does not resume execution or clear uncertain tool effects. An
unfinished turn in an active task is not proof its worker has stopped; inspect
lifecycle state before deciding what to do next. Missing, corrupt or oversized
history returns an error without partial conversation output.

For a metadata-only check, use `darwin task continuation --db path --task TASK_ID`,
SDK `InspectTaskContinuation`, or authenticated `GET /v1/tasks/{task}/continuation`.
The versioned result contains `task_id`, observed `sequence`, `state`,
`history_eligible`, and a fixed `reason`; it excludes prompts, tool arguments and
outputs. Eligible history is either completed or an exact restored-delegation
checkpoint. Pending tools, uncertain effects, interrupted turns, ordinary
failures and canceled tasks are not eligible. This observation grants no retry
permission and does not check the selected provider, privacy, resource or context
budget. The experimental Codex CLI provider supports explicit typed-history
continuation within its [documented limits](docs/codex-history-continuation.md).
Inspecting readiness never repairs or resumes a task.
The CLI exits zero for a successful inspection even when `history_eligible` is
false; scripts must inspect that field, not treat command success as permission.

Task-owned lease and recovery counts are available through
`darwin task leases --db path --task TASK_ID`, SDK `InspectTaskLeases`, and
authenticated `GET /v1/tasks/{task}/leases`. These metadata-only observations
exclude lease tokens, owner/scope names and conversation content. Unexpired
leases are not proof of living owners; counts do not authorize release or retry.
See [task lease inspection](docs/task-lease-inspection.md) for limits and schema
availability.

To identify tasks holding an overlapping resource scope, use
`darwin resources leases --db path --scope workspace`, SDK `InspectScopeLeases`,
or authenticated `GET /v1/resources/leases?scope=workspace`. This bounded,
read-only list grants no execution or recovery authority. See
[scope holder inspection](docs/scope-holder-inspection.md).

## Configuration

The daemon durably flags expired unreleased leases. Inspect them with
`darwin resources attention --db path`, SDK `ListLeaseAttention`, or authenticated
`GET /v1/resources/attention?state=open&limit=25`; an alert
never grants release or retry authority. See [lease attention](docs/lease-attention.md)
for schema-26 upgrade guidance and observation limits. Inspect recorded transitions
with `darwin resources attention-history --db path --id ATTENTION_ID`.

Process-ownership guards default to private `DarwinRouter/process-owners`
storage beneath the OS user configuration directory. Set
`DARWIN_PROCESS_OWNER_DIR` before startup to override it with an absolute private
directory. Keep referenced guard files: deleting them does not safely release
leases. See [ownership and retention](docs/process-lifetime-ownership.md).

The daemon can also fail a verified orphaned read-only worker before any execution
child was recorded, including death after acquiring its reader but before
`worker.started`. Recovery preserves source events, creates no child or accepted
output, and atomically releases the exact worker reader with a distinct receipt.
The parent requires separate reconciliation and ownership proof. See
[interrupted worker recovery](docs/orphan-worker-recovery.md) for eligibility,
actual SIGKILL qualification and unsupported cases. Interrupted execution children
can also resolve 1–32 already-dispatched, explicitly read-only calls as fixed
host failures, then fail the child and worker without repeating tools or inference.
Up to 64 held child readers must match the worker's exact stopped execution image;
they remain held until separately proven terminal-reader recovery. Undispatched
proposals, writes, legacy behavior and uncertain effects remain unsupported.

```sh
./bin/darwin config validate --config examples/local.yaml
./bin/darwin config show --config examples/local.yaml --set workers.max_in_process=1
DARWIN__MODE=local_only ./bin/darwin config validate

# Read-only configuration catalogs; output is safe for diagnostics.
./bin/darwin providers list --config examples/local.yaml
./bin/darwin models list --config examples/local.yaml

# Query the authenticated health report of the running daemon.
DARWIN_API_TOKEN=replace-me ./bin/darwin doctor --config examples/local.yaml
```

The provider and model catalogs only inspect configuration and never perform
inference or mutate runtime state. Provider output omits endpoints and credential
environment-variable names. `doctor` validates the daemon's comprehensive
`/v1/health` report and exits nonzero when the daemon is unavailable or not ready.

Precedence: defaults → OS user config directory `/darwinrouter/config.yaml` → working-directory `config.yaml` → `DARWIN__SECTION__FIELD` environment variables → repeated `--set section.field=value` flags. `--user-config` and `--config` select explicit files; missing explicit paths are errors. Nested mappings merge; arrays replace wholesale. Environment and CLI overrides address scalar settings only. Unknown fields, duplicate keys, aliases, nulls, and multi-document YAML are rejected. Configuration files are limited to 1 MiB.

Integer fields (counts, token limits, byte sizes, and schema version) require
unquoted integer values in YAML: `max_facts: 8`, not `8.5` or `"8"`. Values must
fit their integer type and pass the field's limits. Environment/CLI overrides
still use text such as `--set memory.max_facts=8`, but fractional or overflowing
values are rejected. Type-invalid lower-precedence files or environment values
are rejected even if a later layer overrides them. Genuine floating-point
settings, such as estimated cost and routing weights, remain supported.

Provider keys are referenced by `api_key_env`; the loader never resolves credential values. Configuration text is literal (shell `${...}` expansion is not performed). Set concrete endpoint/database values in files or override scalar settings through the environment. An Ollama provider with no `endpoint` uses the deterministic standard `http://127.0.0.1:11434` endpoint; DarwinRouter does not scan ports, use DNS, or discover a remote destination. Other provider kinds require their endpoint or executable explicitly. The display redacts endpoints and database paths. HTTP providers may set `request_timeout` from `100ms` through `5m`; omission retains the five-minute default, and a shorter caller deadline remains authoritative. The timeout covers discovery and the complete streaming response. Custom provider engines receive the same deadline cooperatively. Provider execution uses an owned transport enforcing loopback-only destinations in local-only mode. Recognized loopback addresses and `localhost` are pinned in every mode, including hybrid/cloud calls through a local proxy; remote HTTPS hosts still use normal DNS. Cloud routes and the Codex coordinator are rejected before construction in local-only mode, and remote metrics export uses the same boundary. This is not an operating-system sandbox: trusted in-process provider, tool, context, evaluator, or store extensions remain responsible for any networking they perform themselves.

Add up to 64 environment-variable names under `security.redact_env` when
project-specific sensitive values beyond provider/API credentials must be
removed from runtime input, output, durable events, route-related context and
inspection surfaces. Store only the variable names in YAML and place their
values in the process environment; literal secret values do not belong in
configuration. This is exact literal redaction, not semantic data-loss
prevention or protection against encoded/obfuscated variants.

## Run an explicit-model task

Replace `local-model-id` in `examples/local.yaml` with an installed Ollama model
and set that model's `ram_bytes` to a conservative positive estimate covering
weights, maximum context/KV memory and runtime overhead. The sample deliberately
uses zero to deny execution until this estimate is supplied; configuration
validation alone does not establish execution readiness. On Apple unified
memory include GPU allocations in RAM and leave `vram_bytes` zero. Then run:

```sh
./bin/darwin run --config examples/local.yaml --model local-fast < prompt.txt

# Stream committed lifecycle events and the final result as JSON lines.
./bin/darwin run --config examples/local.yaml --model local-fast --json < prompt.txt
./bin/darwin task show --db ./data/darwin.db --task TASK_ID
./bin/darwin resources
./bin/darwin run --config examples/local.yaml --model local-fast --continue-task TASK_ID < followup.txt
```

`run --json` emits versioned JSONL envelopes: `{"version":1,"type":"event","event":{...}}` for each committed, redacted runtime event, then `{"version":1,"type":"result","result":{...}}`. Failure results include a generic top-level `error` and task IDs without partial output text. Exit status is 0 for success, 1 for execution/output failure, and 2 for invalid arguments. Configuration and input failures before execution are reported on stderr and may produce no JSON record. No plain answer is appended to JSON stdout. Raw token text remains suppressed; completed turn text is redacted. Closing an output pipe cancels execution and permits durable cleanup instead of terminating immediately on SIGPIPE. Actual stdout pipes also have cancelable, fifteen-second bounded writes, so a reader that stops draining cannot indefinitely block cleanup. Custom embedded writers and regular files retain their own blocking semantics. This remains headless execution with stdin consumed as one prompt, not an interactive prompt loop or resumable event delivery. Signal cancellation is installed before input reading. Treat an absent final result as an unknown delivery outcome and inspect task history before retrying.

The prompt is read from stdin (maximum1MiB, nonblank UTF-8, with a30-second input allowance). Canonical terminals and pipes support cancellation and retain their original descriptor ownership/flags; regular-file kernel reads and custom readers remain cooperative. The completed answer goes to stdout; the durable task ID goes to stderr. This command requires an explicit project config, optionally accepts `--user-config` and repeated `--set` scalar overrides, and uses environment overrides. Unlike `config`, it does not discover user/project configuration paths yet. Execution has a separate five-minute timeout and configured runtime turn limits; file tools require explicit opt-in. Known configured provider keys are redacted from persisted content and the returned answer. Partial token text is not persisted. Other sensitive-content redaction policies remain unfinished. No paid/live-provider qualification has been performed.

`task show` opens an existing database read-only and prints reconstructed conversation state as JSON, including pending tools and uncertain outcomes. It never creates a database or resumes work. Its output includes session content; treat exports as sensitive. `resources` reports host measurements with unavailable sensors represented as null.

On macOS, the profiler also reads Foundation's reported thermal state. Serious
or critical readings block new local reservations; failed or unknown readings
remain null rather than being called cool. The optional `thermal_state` label
distinguishes nominal, fair, serious and critical reports. See
[thermal profiling](docs/macos-thermal-profiling.md) for API caveats and limits.

On Linux, read-only thermal sysfs observations compare published passive, hot
and critical trip points with sampled temperatures. Reached thresholds deny new
local reservations; incomplete measurements remain unknown. No guessed Celsius
limits, fan-state inference or cooling-setting changes are used. See
[Linux thermal profiling](docs/linux-thermal-profiling.md) for bounds and the
distinction between threshold evidence and actual kernel throttling.

On Linux, cgroup-v2 measurements cap host RAM by every visible ancestor's
`memory.max` and `memory.high`, and cap available RAM by the corresponding
`limit - memory.current` headroom (saturated at zero). Treating `memory.high`
as a capacity boundary is a conservative routing policy: the kernel defines it
as a throttling threshold, not an OOM limit. CPU counts also honor visible
`cpu.max` quotas and effective cpusets; fractional quotas allow at least one
worker and otherwise round down. These measurements feed normal admission.
See the [kernel cgroup-v2 documentation](https://docs.kernel.org/admin-guide/cgroup-v2.html).

Discovery uses bounded proc/mount records and checks membership again after
sampling. Missing, malformed, ambiguous, or changing hierarchies fail closed.
Legacy v1 memory hierarchies and namespace paths containing `..` are currently
unsupported; use a trusted SDK resource profiler for unsupported deployments.
Only visible v2 limits are accounted for—hidden namespace ancestors, legacy CPU
controllers, external inference-server limits, and concurrent kernel changes
are not inferred. Swap figures remain host-level and thermal sensors may remain
unknown. These are observations, not OS-enforced reservations or container
isolation. One isolated Docker Linux/arm64 profile has been verified; broader
container/hardware qualification remains outstanding. Run
`make qualify-linux-cgroup` for the optional cached-image check described in
[Linux qualification](docs/linux-qualification.md).

`resources` also includes `gpu_inventory`, a separate per-device diagnostic survey.
On Linux it queries `/usr/bin/nvidia-smi` and AMD DRM sysfs concurrently; each
source reports `observed`, `unavailable`, or `unsupported` with byte counters.
It never sums separate GPUs. Models with an explicit `gpu_device` binding also
use these observations for per-device admission, as described below. Unbound
configurations keep GPU subprocesses out of the routing path. NVIDIA needs its
existing driver utility at the
fixed path; no software is installed. AMD cards missing PCI vendor/counter files
make that source unavailable. Driver errors are not printed. Sysfs cancellation
is cooperative around bounded reads, not a guarantee against a stalled kernel.
GPU identifiers appear in this local diagnostic output; treat hardware exports
accordingly. Apple unified memory remains in the host snapshot, not a fabricated
discrete-GPU inventory.

`--continue-task` starts a new task from a completed task's saved conversation in the same database and session. The source remains immutable, and the new task records its parent. Missing, unfinished or uncertain-effect histories are rejected. Histories created on local models (and legacy histories without a privacy marker) cannot be continued on cloud models. This is completed-session continuation, not interrupted-task recovery. Combined input is limited to 4 MiB and configured per-model context admission still applies.

### Inspecting tool approvals

Inspect durable approval metadata without invoking the reviewer or executing work:

```sh
darwin approvals list --db path/to/events.db --task task-id --limit 25
darwin approvals show --db path/to/events.db --task task-id --id approval-id
darwin approvals execution --db path/to/events.db --task task-id --id approval-id
```

The authenticated daemon exposes `GET /v1/tasks/{task_id}/approvals?limit=25`
and `GET /v1/tasks/{task_id}/approvals/{approval_id}`. Lists accept `after` as an
exclusive tool-call ID cursor and limits from 1 to 100 (default 25). Continue with
the response's `next_after_call_id`; an absent cursor ends that scan. Ordering is
lexical tool-call ID order, not time order. Pages are individually consistent,
not a frozen snapshot: restart a scan to find intervening insertions before a cursor.

Responses contain scope, digests, validity window, state and operator decisions,
not raw arguments or lease tokens. Treat actor/scope metadata as private.
`consumed` means dispatch authority was spent, not that a write succeeded or is
safe to repeat. These read-only commands never migrate/create a database or
approve, revoke, resume or retry anything.

`execution` (also `GET /v1/tasks/{task_id}/approvals/{approval_id}/execution`)
correlates the approval, validated task journal and scope-wide writer lease in
one read snapshot. It reports an open or completed call, any durably recorded
effect, and a `none`, `live` or `expired` writer observation. A recorded effect
is the tool's report, not independent artifact verification. An expired writer
does not prove its process stopped, and an open call cannot distinguish a crash
before a write from one after a write. Neither observation permits automatic
retry or lease release; interrupted-effect reconciliation remains unfinished.

The additive `scope_leases` object counts live/expired readers and writers
across every overlapping scope, including `workspace` and legacy `create_*`
aliases. Both live and expired unreleased holders still conflict. The existing
`scope_writer_state` retains its exact-scope meaning and can say `none` while
alias blockers exist. Missing `scope_leases` means a legacy/unknown observation,
not zero blockers. Counts expose no holder tokens or identities, and even all
zero counts are not permission to dispatch. See [reader/writer execution](docs/reader-writer-execution.md).

### Recording an operator approval decision

For SDK tasks configured with `ApprovalPresenter`, inspect the exact private
preview before approving. The presenter must display the arguments safely; an
inspection digest alone is not a substitute for understanding the proposed effect.
Submit a JSON object containing `expected` (the complete inspected `request`),
`id` (a stable, unique decision ID), and `allowed` (a JSON boolean):

```sh
darwin approval-decision --config path/to/config.yaml < decision.json
```

The daemon also accepts authenticated
`POST /v1/tasks/{task_id}/approvals/{approval_id}/decision` with that JSON body.
No actor or timestamp fields are accepted. CLI attribution uses the invoking
OS user ID; the API uses `api_operator`, representing its shared bearer credential,
not per-person identity. SDK `DecideApproval` instead requires host-authenticated
actor attribution. Decision bodies are limited to16KiB and reject duplicate,
missing, unknown and null fields, including inside the expected request.

The full request must still match. A fresh approval/denial requires an unexpired,
uncanceled pending call; denying an approved but unconsumed request revokes it.
Reuse the same decision ID, action, actor and expected request after uncertain
acknowledgement. A retry returns current state without changing the original
decision time; it cannot restore consumed authority. Never switch actor/transport
identity on such a retry. These controls open only existing current-schema WAL
storage, without creation or migration, and never themselves execute a tool.

The waiting SDK runtime polls durable decisions and consumes approval under its
writer lease before dispatch. CLI/API controls do not register write handlers or
resume crashed tasks. Interrupted-effect reconciliation remains unfinished.

### Steering an active task

With the daemon running, authenticated clients can send
`POST /v1/tasks/{task_id}/steering` with JSON:

```json
{"idempotency_key":"unique-client-message-1","text":"Focus the answer on the migration risks."}
```

The response is202 while pending and includes a message ID, not the submitted
text or key. Inspect with `GET /v1/tasks/{task_id}/steering/{message_id}`.
`GET /v1/tasks/{task_id}/steering` lists all pending/applied message receipts in
insertion order. The lifetime cap makes the list bounded; no pagination or query
parameters are needed. List responses never include guidance text.
Reusing a key with the same persisted text returns the original record;
different text conflicts. Keys are hashed before storage and configured secrets
are redacted from guidance. Task/session content still needs privacy care.
Control endpoints have separate bounded capacity from execution and cancellation.

Steering is applied before a model turn or after a complete tool batch, never
halfway through a tool-call/result pair. It does not interrupt a running provider
or stop tools already proposed; use cancellation to request stopping. The
`steering.applied` SSE/replay event means the guidance was durably added to context,
not that another provider turn executed or that the model obeyed it. The runtime
checks again at completion, so guidance accepted before the terminal transaction
causes another bounded turn rather than being silently ignored.

Limits are32 messages over a task's lifetime,64KiB per message,4MiB serialized
conversation at steering admission, and the model's configured context limit.
`runtime.max_turns` defaults to8 (allowed1–32) for all tasks; enabled tools also
apply `tools.max_turns`. Guidance does not reset these budgets or change model,
privacy, tool permissions or resource reservations. Context/turn exhaustion can
fail the task with guidance pending or applied-but-not-executed. Failed/canceled
tasks retain those records for inspection; they are not automatically resumed.
New guidance for a terminal task is rejected, but duplicate-key receipts remain
retrievable. Use completed-session continuation for a new follow-up task.

Steering storage was introduced in schema14; current storage migrates
transactionally to [schema28](docs/event-kind-index.md). Back up operational databases
before upgrades; older binaries cannot open schema28. The runtime turn
configuration changes durable submission fingerprints, so older queued requests
require explicit configuration-mismatch handling. A full-screen interactive editor
and general interrupted-session recovery remain unfinished.

From a second terminal using configuration that points to the same task database:

```sh
./bin/darwin steer --config examples/local.yaml --task TASK_ID --key unique-message-1 < guidance.txt
./bin/darwin steering list --db ./data/darwin.db --task TASK_ID
./bin/darwin steering show --db ./data/darwin.db --task TASK_ID --id MESSAGE_ID
```

`steer` stores guidance only; the already-running task consumes it at its next
safe boundary. It does not launch or resume a stopped task. Retrying after an
uncertain CLI/output error must use the same key and text. Enqueue and list
commands print metadata only; `show` explicitly exports the stored guidance and
should be treated as sensitive. Listing and inspection open existing storage
read-only and never initialize missing databases or migrate legacy stores.

Input must be a UTF-8 file or pipe, not a directly attached terminal, and finish
within a five-second input allowance. Size remains 64 KiB; blank and invalid
UTF-8 input is rejected. Pipe cancellation borrows a descriptor, restores its
flags and leaves the caller's descriptor open. Regular-file kernel reads and
custom embedded readers remain cooperative rather than forcibly interruptible.
Output uses existing cancellation/broken-pipe handling.

### Line-oriented interactive chat

```sh
./bin/darwin chat --config examples/local.yaml --model auto
```

Enter one prompt per line; wait for its final answer before entering the next
prompt. Each successful task becomes the next task's persisted conversation
context. Failed or canceled tasks never silently become continuation sources.
Use `/status`, `/cancel`, `/steer TEXT`, `/new`, `/help`, or `/quit`.
`/new` clears the continuation pointer only when idle; it does not delete history.
While a task runs, ordinary lines are rejected explicitly; `/steer TEXT` queues
guidance once a task ID is available. Guidance applies at safe runtime boundaries,
not mid-tool execution. Prefix a prompt with `//` for a literal leading slash.

Ctrl-C requests cancellation during work and exits when idle. `/quit` and SIGTERM
cancel and join active work. EOF waits for active work to finish. A normal session
exit returns zero even if an individual task failed; use headless `run` for
per-task exit status. Each task retains the five-minute execution timeout.
The command prints lifecycle progress and provisional live assistant text with a
`| ` prefix on each explicit model-output line. It prints `[task completed]` only
after successful return, without duplicating the final answer. Terminal escape
sequences, clipboard controls and bidi formatting are filtered across text
fragments; stored content remains governed by runtime redaction. Live text has
a 1 MiB per-task display bound and can include intermediate assistant turns.
See [interactive streaming](docs/interactive-streaming.md) for callback ordering,
output failure and privacy boundaries.

Chat accepts canonical terminals and UTF-8 files/pipes, with up to64KiB per line
(the OS terminal line discipline may impose a smaller limit). Terminal echo and
canonical settings are unchanged. Idle input has no timeout; cancellation joins
borrowed pipe/terminal reads and restores descriptor flags. Custom embedded readers,
writers and regular-file kernel operations remain cooperative. A full-screen
editor, multiline editing, automatic context compaction and interrupted-task resume
are not implemented. Chat supports the same routing/continuation flags as `run`,
but JSON output belongs to `run --json`.

Give explicit feedback before starting another task:

```text
/feedback rejected 0
/feedback-show
/feedback-revise EXPECTED_EVALUATION_ID accepted
```

`/feedback accepted|rejected COST` requires the observed final-attempt cost;
zero is explicit, never inferred from missing provider usage. These commands
target only the latest successful answer displayed in this chat. Starting another
task or using `/new` clears that feedback target; feedback is rejected while busy,
after a failed/canceled task, or before any answer has been displayed. Use the
standalone `feedback --task` commands to address older answers explicitly.
`/feedback-show` exposes evaluation IDs, outcomes and evidence sources, not answer
text. It can include objective evidence, which is not subjectively revisable. Corrections
require the exact prior ID and retain immutable history; identical retries do not
add fitness samples, and conflicting/stale revisions fail. Observed cost cannot
be corrected through the subjective revision command. User feedback cannot erase
objective validation failures. These commands are operator actions, never model
tools or automatic interpretation of conversational text.

To shorten a completed conversation, supply an operator-reviewed summary file:

```json
{"decisions":["Keep the existing public API"],"requirements":["Preserve local-only privacy"],"pending_work":["Add integration coverage"],"failures":[],"artifacts":["src/router.go"],"activity":["Inspected router.go; no files modified"]}
```

```sh
./bin/darwin run --config examples/local.yaml --model auto --continue-task TASK_ID --compact-keep 6 --compact-summary summary.json < followup.txt
```

Compaction retains at least the requested recent message count, expanding backward to keep tool-call/result batches complete. All original system messages remain. Summary fields are untrusted reference data, not permissions; each category permits at most 128 nonblank entries and the serialized summary is limited to 64 KiB. At least one summary entry and one removable non-system message are required. Configured credentials are redacted before summary use. Models need known `context_tokens`; compaction does not guarantee that the resulting input fits.

The new task atomically records its compacted input and a versioned summary checkpoint with source task, event sequence, source-conversation SHA-256 and removed-message count. It also records the first retained recent-message index and its source event sequence, plus before/after context estimates. These estimates cover the source and compacted history only, excluding the next prompt, freshly retrieved knowledge and tool catalog; they use conservative serialized-byte accounting, not a tokenizer. Legacy checkpoints may omit these additive fields. Inspect with `task show` or `GET /v1/tasks/{id}` after restart. The original history remains untouched, including any sensitive content; compaction is not deletion.

The Go `sessions.Summarizer` component generates bounded proposals, and the application now exposes explicit draft generation through the CLI:

```sh
./bin/darwin summary --config path/to/config.yaml --task TASK_ID --model SUMMARY_MODEL_ID --keep 6 --max-cost 0
./bin/darwin summaries list --db ./data/darwin.db --task TASK_ID
./bin/darwin summaries show --db ./data/darwin.db --id SUMMARY_ATTEMPT_ID
```

The selected model needs configured `context_tokens` and `estimated_cost`; local models also need `ram_bytes`. The default zero cost ceiling permits only a configured zero-cost estimate. The sample local configuration needs this operator-supplied metadata before summary generation. Summarization may use the source model because it is not an independent quality audit; `evaluation.judge` does not disable explicitly requested summaries.

The exact signed-in Sol `codex_app_server` model can also draft summaries of
cloud-eligible sources using the same command. See [native session-summary
drafts](docs/codex-session-summaries.md). Explicit [native compacted continuation](docs/codex-compacted-continuation.md)
can use an operator-supplied summary or approved stored draft; generating a
draft does not approve or apply it.

Generation makes one auxiliary call without tools or retries. Application admission enforces deployment mode, source privacy, resource reservation and cost metadata, then persists a `started` attempt before dispatch. Configured credentials are redacted from input and draft content. Success atomically stores the proposal with `drafted` status; failure stores a generic code. Cancellation cleanup is bounded independently. A crash or unavailable store can leave `started` indeterminate—it is not proof that a summarizer is still running. Inspection opens storage read-only and supports up to 100 records per page, with optional task filtering and an exclusive `--after` ID cursor.

Drafting never modifies the source, starts a continuation or affects fitness. Inspect the full proposal and verify its accuracy before recording an operator review:

```sh
./bin/darwin summary-review --config path/to/config.yaml --attempt SUMMARY_ATTEMPT_ID --decision approved --note "Describe the source checks supporting approval"
./bin/darwin summary-reviews --db ./data/darwin.db --attempt SUMMARY_ATTEMPT_ID
./bin/darwin run --config path/to/config.yaml --model auto --continue-task TASK_ID --summary-attempt SUMMARY_ATTEMPT_ID < followup.txt
```

`--summary-attempt` uses the frozen draft's retained-message count and summary; it cannot be combined with manual compaction flags. The draft must match the source and have a current approval. Its review ID is recorded in the new task's compaction metadata. Approval is checked again in the same SQLite transaction as task start, so a rejection committed before that start blocks dispatch. To change a decision, use `summary-review --expected CURRENT_REVIEW_ID --decision rejected --note "Explain the issue"` with the same config and attempt. Stale decisions conflict; history is immutable and limited to 100 reviews per attempt. Review notes are capped at 4 KiB and credentials are redacted.

Review is a local operator attestation, not automated proof of accuracy, and does not itself run a model. Rejection blocks subsequent direct admissions of that stored draft; it does not cancel already-started work or erase summary copies in existing sessions. Newly configured redaction that changes an approved summary requires a fresh draft and review. The manual summary-file route remains available for explicitly operator-supplied summaries.

Embedded Go applications can generate, inspect, list and review these same
proposals through the [SDK session-summary workflow](docs/sdk-session-summaries.md).
The host must authenticate the reviewing operator; calling the SDK does not
grant a model authority to approve its own draft.

Source provenance refers to unchanged durable history, even when the auxiliary input was redacted. Estimates are operator estimates, not billing guarantees; summaries, review notes and inspection output can contain sensitive session information. Automatic semantic validation/application, crash reconciliation and mid-task compaction remain unfinished.

### Model-callable bounded workers

Delegation is opt-in. Set `workers.delegate_model` to an existing configured
model ID with a known `context_tokens` capacity and `estimated_cost`:

```yaml
workers:
  max_in_process: 3
  delegate_model: local-worker
  delegate_max_calls: 4
  delegate_max_cost: 0
  delegate_read_tools: false
  delegate_max_turns: 4
```

The parent receives a `delegate` tool accepting a prompt (up to 16 KiB) and
`validation: text` or `go_source`. The operator chooses the worker model; model
output cannot select a different provider or grant permissions. By default each
child has one inference turn, a 30-second deadline and a 64-KiB output limit.
It receives only the explicit prompt, with no ambient history, memory or skills.
It cannot delegate recursively. A local-only parent cannot send its child to
the cloud, even in hybrid mode.

To permit workspace inspection, explicitly enable `workers.delegate_read_tools`
alongside the parent's `tools.enabled` and `tools.read_root`. The worker must be
local. It receives only `read_file`, borrowing the parent's already-open root;
it cannot reopen a changed path, escape the root, write files or acquire new
permissions. Its allow rule is subordinate to the parent's policy: deny or ask
does not become permission. The registry stays open until all child work has
joined. Disabled delegation tools remain inference-only.

Read-tool children use at most `delegate_max_turns` (2–8), additionally capped
by `runtime.max_turns` and `tools.max_turns`. Tool-call/result pairs and validation
are recorded in the child's ordinary runtime history. The 30-second deadline
and output cap remain unchanged across the whole child run.

The parent also receives `delegate_batch` with a `tasks` array of two to four
objects using the same `prompt` and `validation` fields. Independent tasks run
concurrently when capacity permits. Its `results` array preserves input order;
each element is the normal delegate envelope or a bounded error. All items
reserve from the same `delegate_max_calls` allowance as single calls. A batch
that exceeds the remaining allowance starts no children and consumes no calls.
Once admitted, failures still count toward that allowance.

Every child is joined before returning, including after cancellation or failure.
Accepted sibling results can be returned alongside individual failures, but
cancellation suppresses all delivery. Each encoded result is limited to128KiB
(including JSON escaping); larger results become per-item errors and remain
available in durable child history. The overall tool response is below1MiB.
Large batches can still exceed a parent's configured context capacity.

Parents retain their task slots and hardware reservations. Children acquire
additional capacity without waiting; unavailable capacity produces a bounded
tool error that lets the parent continue. Thus a one-slot or one-model system
cannot delegate yet. Sharing/unloading a parent's local model reservation is
not implemented. Separate model tool calls are sequential; `delegate_batch`
and independent parents can run children concurrently within the shared ceiling.

Each invocation records a supervisor work task and a separate inference task.
The work task links to the parent; the inference task links to the work task.
Successful tool results contain both IDs and `untrusted_output`. Worker acceptance
and completion are durable before the result is released. `text` checks only
nonempty output; `go_source` additionally parses Go syntax, not types or tests.
These checks do not prove task correctness or replace user feedback. The inference
task holds the answer history; work-task lifecycle events hold acceptance evidence.

Parent cancellation and work-task cancellation cancel and join the child.
Submission ownership fences all three logs; stale ownership cannot begin a child
turn. Completed submission trees can be reconstructed without rerunning children;
incomplete delegation remains operator-inspection-only.
Delegation does not automatically update fitness or audit the child.

`delegate_max_calls` is 1–16 per parent execution, including failed admitted
attempts. `delegate_max_cost` is a separate **per-child configured estimate** ceiling,
not the parent's request budget or a measured billing cap. Up to max-calls times
that ceiling can be spent in addition to parent inference. For read-tool children,
configuration conservatively requires the model's per-turn estimate times
`delegate_max_turns` to fit this ceiling. Provider billing can differ from
estimates. Disable delegation by leaving `delegate_model` empty.

## Local HTTP service

Set `DARWIN_API_TOKEN` to a securely generated secret of at least 32 characters, then run `darwin serve --config examples/local.yaml`. The configured daemon address must be loopback. This foreground process stops on SIGINT/SIGTERM and cancels active requests during shutdown. It is not yet an installed operating-system service.

All endpoints require `Authorization: Bearer <token>`:

- `GET /health`: lightweight database and live supervisor check. Its legacy response still declares `providers_checked: false`; it performs no provider discovery.
- `GET /v1/health`: detailed operational report described below, including bounded provider/model discovery.
- `GET /v1/resources/attention`: read-only paginated lease-attention records;
  optional `state`, `after`, and `limit`, with defaults `open` and `25`.
  See [lease attention](docs/lease-attention.md) for observation and upgrade limits.
- `GET /v1/resources/attention/{id}/history`: append-only observation history;
  optional `after_sequence` and `limit`, with defaults `0` and `25`.
- `POST /v1/tasks`: JSON `{"model_id":"local-fast","prompt":"Hello"}` with optional `continue_task_id`. With a continuation, use either `summary_attempt_id` for a currently approved stored draft or `compaction` with `{"keep":6,"summary":{"decisions":["Retain existing API"]}}` for a manual summary, not both. The same admission rules apply as in the CLI. This initial endpoint waits for durable completion before returning HTTP 201 with `task_id`, `text`, `turns`, and the optional `route_estimated_cost` described under automatic routing.
- `GET /v1/tasks/{id}`: reconstructed task/session state.
- `POST /v1/tasks/{id}/cancel`: send JSON `{}` to durably request cancellation. HTTP202 means the request was recorded while the task was running, not that execution has already stopped; HTTP200 reports an already-terminal task. Repeating the request is naturally idempotent for that task and retains the original request ID/time. `GET /v1/tasks/{id}/cancellation` reports durable request status and the current task state. Two independent control slots keep these operations available when execution capacity is full. Current runners observe requests through SQLite, including requests from another service instance/process. Database transaction order resolves cancellation versus completion: a cancellation recorded first prevents later normal events and completion, while a terminal event recorded first remains terminal. Already-started tool effects may finish and must be recorded; cancellation does not roll them back. A stopped/orphaned runner can retain a pending request until recovery is implemented. Post-completion auxiliary audits have their own lifecycle and are not canceled through this task endpoint.
- `GET /v1/tasks/{id}/events`: read-only SSE replay of one durable snapshot page, at most100 events and8 MiB of serialized event data. Reconnect with `Last-Event-ID: <task_id>:<last_received_sequence>`; omit the header to start from sequence0. Events use the same IDs and JSON as live streaming. The final `event: checkpoint` contains `from_sequence`, `next_sequence`, `head_sequence`, `state` and `has_more`, without an event ID or task result. When `has_more` is true, fetch another page from `next_sequence`; when false, the reader is caught up to that snapshot only. A running task may subsequently add events. Disconnecting replay never cancels or re-executes the task. Cursor mismatches/malformed headers return400; cursors beyond the durable head return409; unknown tasks return404 and oversized records return413. Honor503 `Retry-After` capacity responses, including immediately after closing a prior replay connection. This is bounded replay, not a continuous follow stream; inspect task history before retrying any mutating submission.
- `POST /v1/tasks/stream`: the same native task request, delivered as live Server-Sent Events. Each committed, redacted runtime event has `event: <kind>`, `id: <task_id>:<sequence>` and JSON `data`. A final `event: result` contains the public task result or a generic error; it has no resume ID. Automatic fallback can produce multiple task IDs, each with its own sequence. Runtime events arrive while work runs, but raw model-token text remains suppressed for credential safety; completed turn text is redacted before delivery. The connection owns execution: disconnects cancel remaining work with durable cleanup. This is not detached submission or replay; `Last-Event-ID` is rejected, and retrying a POST can execute a new task. Request-format errors use ordinary JSON HTTP errors before streaming; execution/admission errors after headers are reported in the final SSE result. Clients must inspect that result rather than treating HTTP 200 as task success. The same authentication, privacy, capacity and five-minute request deadline apply; stalled writes have a fifteen-second deadline.
- `POST /v1/summaries`: JSON `{"task_id":"TASK_ID","model_id":"SUMMARY_MODEL_ID","keep":6,"max_cost":0}` generates one draft and returns its persisted summary-attempt record with HTTP 201. It waits for completion; it does not activate the draft. A failed admitted invocation returns a generic error and `summary_attempt_id` for inspection.
- `GET /v1/summaries/{id}`: inspect a stored summary attempt, including draft content when available.
- `POST /v1/summaries/query`: read-only JSON query with optional `task_id`, exclusive `after` ID cursor and `limit` (1–100, default 100). `{}` lists the first page across tasks. Query parameters remain disallowed; filters use this bounded body instead.
- `POST /v1/summaries/reviews`: JSON `{"attempt_id":"SUMMARY_ATTEMPT_ID","decision":"approved","note":"Describe your source checks"}` records an operator decision with HTTP 201. Use `rejected` to deny direct reuse and supply `expected_id` with the current review ID for later decisions. Stale decisions return 409; admission/policy denials return 422. Possession of the daemon token authorizes this operator action; keep it out of model-accessible files.
- `GET /v1/summaries/{id}/reviews`: read the immutable review chain, capped at 100 entries.
- `POST /v1/feedback`: JSON `{"task_id":"TASK_ID","outcome":"accepted","attempt_cost":0}` (or `rejected`). Requires an observed final-attempt cost. Identical retries return 200 without adding samples; conflicts return 409, and ineligible task histories return 422. The body limit is 4 KiB and feedback shares daemon admission capacity with tasks.
- `GET /v1/feedback/{task_id}`: original final-attempt evaluation followed by its revision history.
- `POST /v1/feedback/revisions`: JSON `{"task_id":"TASK_ID","expected_id":"EVALUATION_ID","outcome":"rejected"}` (or `accepted`). Uses the same subjective-only correction policy as the CLI, with a 4 KiB body limit and shared capacity. Identical retries return 200; stale/conflicting corrections return 409; evidence-policy denials return 422. Execution measurements cannot be changed through this endpoint.

All summary endpoints share task concurrency capacity and enforce authentication, origin denial and request deadlines. Summary POST bodies require JSON and are limited to 8 KiB; review notes remain limited to 4 KiB. Overload returns 503 with `Retry-After`. Review is still operator attestation, not an automatic quality judge. The OpenAI-compatible endpoint does not accept these Darwin-native extensions.

`POST /v1/chat/completions` accepts `model`, text-only system/user/assistant `messages`, optional `stream`, and `stream_options.include_usage` when streaming. Other OpenAI parameters are rejected. Streaming responses use live, incrementally redacted assistant text (`X-Darwin-Stream-Mode: live-redacted`). Known credentials are withheld across chunk boundaries; partial secret matches can delay text delivery. Content is provisional and may include intermediate assistant turns; tool arguments/results and delegated child streams are not exposed. Only successful durable task completion can produce the finish chunk and `[DONE]`. Failures after headers produce a sanitized SSE error without a success marker. Disconnects cancel execution. Token text is not durably replayable; native lifecycle events remain the inspection/replay interface.

With `"stream_options":{"include_usage":true}`, ordinary chunks carry
`usage:null`. After successful completion, a separate chunk with `choices:[]`
reports known prompt, completion and total token counts before `[DONE]`, following
the [OpenAI streaming usage shape](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create).
`X-Darwin-Usage-Scope: successful-task-model-turns` identifies Darwin's accounting
scope: all model turns in the successful task, excluding failed routing attempts,
delegated child tasks and auxiliary audit/summarization calls. This is not a
whole-request billing total. Missing, invalid or overflowing counts produce a
sanitized `usage_unavailable` stream error instead of fabricated totals, a finish
chunk or `[DONE]`; the underlying task may already be durably completed, so this
metadata error is not permission to retry side effects. Explicit reported zero
counts are valid. Without the option (or with `false`, `{}` or `null`), streaming
usage is omitted. Nonstreaming responses omit unavailable usage. Options are only
accepted with `stream:true`; obfuscation and full OpenAI parameter parity remain
unsupported.

Requests are bounded by configured worker concurrency, a 1 MiB JSON body limit, and a five-minute execution deadline. Duplicate and unknown JSON fields, browser-origin requests, and unauthenticated requests are rejected. API token text is included in application credential redaction. Inference qualification and operating-system service installation remain unfinished. Do not automatically retry a timed-out synchronous task POST; a durable task may already exist.

### Operational health

Authenticated `GET /v1/health` returns a versioned report with `checked_at`,
`status`, `ready` and machine-readable checks for the daemon, database,
supervisor, host resources, configured providers and models. HTTP 200 means
`ready: true`; HTTP 503 can carry a valid not-ready report. Invalid/unavailable
diagnostics return a generic error instead. Health has one independent request
slot; saturation returns 503 with `Retry-After: 1` without occupying execution
or cancellation capacity.

Readiness requires a healthy database and supervisor plus at least one enabled
configured model found in a provider catalog and passing the applicable coarse
resource checks. Unknown supplemental measurements or unavailable alternatives
produce a degraded report even when another model remains available. Catalog
presence is **not an inference test**: it does not prove model loading, task
capability, remaining quota, context fit or output quality. Normal task admission
still checks its own constraints.

Probes use a five-second application budget, two-second per-provider deadlines
and at most four concurrent catalog calls; the endpoint allows six seconds for
completion and report validation. Configurations over 64 providers or 256 models
return a bounded configuration-limit report without probing the pool. Built-in
probes honor cancellation; custom in-process hooks must do so too. Discovery is
not cached into routing fitness and runs no inference, tools, model pulling or
database migrations. Local-only mode denies external discovery; disabled model
localities are not probed, and mixed-provider configurations must satisfy each
model's transport policy independently.

Reports expose configured identifiers (safely aliased if they contain known
credentials), never endpoints, database paths, raw provider errors, unconfigured
catalog entries or credentials. RAM/CPU observations are validated; missing GPU
or thermal data remains unknown rather than healthy. Resource observations are
estimates, not memory reservations. Supervisor state includes startup, shutdown,
latched failures and per-worker/reconciler heartbeats; a heartbeat older than
15 seconds is reported stalled. Heartbeats continue during provider execution,
but cannot by themselves prove useful model progress. These are bounded live
observations, not an atomic system-wide snapshot or a full production health
qualification.

### Detached durable submissions

`POST /v1/submissions` accepts the same native task JSON and requires one
`Idempotency-Key` header containing 16–128 visible ASCII characters. HTTP 202
acknowledges durable queue admission, not model eligibility or successful work.
Retrying the same key with the same decoded request and configuration returns
the same submission; changed intent or configuration returns 409. A terminal
retry returns 200. Keys are hashed, not stored in plaintext. Do not put secrets
in keys. Known provider credentials or the API token in the serialized request
cause rejection rather than silent rewriting. Other request content is stored
locally in the private database for execution; this is not an encrypted queue.

`GET /v1/submissions/{id}` reports queued/running/succeeded/failed/canceled state,
linked task IDs, lease expiry and a final result when available. Use each linked
task's inspection/event-replay endpoints for committed history. Submission
status does not expose queued request bodies, key hashes or ownership tokens.
`POST /v1/submissions/{id}/cancel` with JSON `{}` cancels queued work immediately
or requests cancellation of the active worker, including its auxiliary review.
It never rolls back tool effects. Closing a submission/status HTTP connection
does not cancel detached execution; use the explicit cancellation endpoint.

The daemon polls durable queued work every 250 ms with `workers.max` workers,
sharing the execution limit with synchronous tasks. Intake has two independent
slots; inspection/cancellation use two shared control slots. At most 128 queued
requests are admitted; capacity errors return 503 with `Retry-After: 1`.
Each claimed job has a five-minute budget, a 30-second ownership lease and
five-second heartbeats. Ownership and cancellation are checked transactionally
before ordinary task-event writes; cleanup may still record completed effects.

Queued work survives restart but is pinned to its original configuration digest:
a daemon with changed configuration will not claim it. Cancel and resubmit
with a new key if you intentionally change the configuration. Graceful shutdown
cancels and joins active workers, leaving unclaimed requests queued. A crashed
running job can be requeued only if it has **no durable task start**, as described
below. Already-terminal histories can instead restore the result without any
reexecution. Eligible single-task model-only histories are closed as failed (or
canceled when requested), preserving partial output without returning it as an
answer or retrying inference. See [interrupted model recovery](docs/interrupted-model-recovery.md).
Other partial histories may remain running with `lease_expired: true` and are
not automatically retried: effects may already have occurred. Inspect them
before deciding on replacement work. General
orphan reconciliation, safe operator reassignment and queue retention remain
unfinished.

### Discovering and controlling durable work

`GET /v1/submissions` lists metadata without loading queued prompts or completed
outputs. Optional query parameters are `state`, `limit` (1–100, default 25), and
`after` (the opaque `next_cursor` from a prior page). Duplicate or unknown query
parameters are rejected. Listing uses the independent control capacity, not
execution capacity. Other endpoints still reject query parameters.

Pages contain `items`, `has_more` and `next_cursor`. Continue while `has_more`
is true. Each traversal excludes submissions inserted after its first page;
start a new traversal to discover new work. State and lease observations are
fresh per page, not a historical snapshot spanning pages: a state-filtered item
can move out of the filter between reads. Keep the same state filter when using
a cursor. Metadata pages are bounded to 1 MiB and may contain fewer items than
the requested limit. Inspect an individual submission to retrieve its result.

The CLI can operate on the same private SQLite store:

```sh
darwin submit --config examples/local.yaml --key unique-request-key-001 --model local-fast < prompt.txt
darwin submissions list --db /absolute/path/tasks.db --state running --limit 25
darwin submissions show --db /absolute/path/tasks.db --id SUBMISSION_ID
darwin submissions cancel --db /absolute/path/tasks.db --id SUBMISSION_ID
```

`submit` durably queues the request and prints JSON; it does not start a daemon
or wait for model execution. Run `darwin serve` with matching configuration to
execute it. It accepts the run command's constraints and continuation options,
but not `--json`: submission output is already a single JSON status. Preserve
the exact key, input and configuration for an idempotent retry. Do not place
credentials in the key or prompt. Input is bounded to1MiB nonblank UTF-8 under
the30-second submission deadline; pipe/terminal reads support cancellation.
Regular files and custom readers remain cooperative. Show/list are read-only; cancel
requires local access to the configured database. An expired running lease is
an inspection signal, not proof that all effects have stopped, and these
commands never reassign or replay uncertain work.

### Safe recovery before execution

The daemon inspects one page of at most 100 running submissions on startup and
every five seconds. Only matching-configuration, expired claims with **no
persisted `task.started` event** can be requeued. The absence check and owner
replacement share a transaction with task-start admission. An old owner is
fenced from starting a task, renewing its claim or publishing a result after
recovery. A task-start commit—even if its acknowledgement was lost—prevents
automatic requeue. This relies on the runtime's requirement that no model/tool
execution occurs before the task start is durably committed.

An eligible request returns to the bounded queue; a pending cancellation instead
becomes terminal canceled. If the queue is full, recovery waits for capacity.
At most three automatic requeues are permitted; a further expired undispatched
claim becomes failed with `recovery_exhausted`. No request body or idempotency key
changes, and an existing idempotency key continues to identify the same request.
This does not resume an interrupted conversation or retry tool effects.

Each decision is recorded transactionally in an immutable recovery history with
its action, reason and timestamp. Inspect it using
`GET /v1/submissions/{id}/recoveries` or
`darwin submissions recoveries --db /absolute/path/tasks.db --id SUBMISSION_ID`.
These read-only views return at most four JSON records and expose no claim tokens
or queued content. Automatic recovery after partial inference/tool execution is
not implemented.

### Restore a completed result after lost acknowledgement

If execution reached a durable terminal event but the process died before saving
the submission result, the same supervisor can restore that result from history.
It requires an expired claim, matching configuration and complete validated
journals for all linked tasks. Recovery supports one final parent, up to31
contiguous prior no-output fallback attempts, and depth-one worker/inference
children, including parallel batches. The entire tree is bounded to66 tasks,
10,000 events and8MiB.
Unsupported or corrupt
histories remain inspection-required; they are not replayed or declared successful.

Successful reconstruction checks final output, paired tool events, model and
provider identity, nonempty-output evidence, and requested Go-syntax evidence.
Fallback lineage must form an immediate chain of preceding retryable no-output failures. The
worker lifecycle must contain durable validation before acceptance, and accepted
text must match its sole successful inference child. Child privacy cannot exceed
the parent's policy. Missing or nonterminal nodes prevent reconstruction.
The result retains the original parent task ID, final text and finish reason;
PreviousTaskIDs contains only preceding fallback attempts in execution order,
never child tasks.
All child IDs remain inspectable through submission status. Usage is
summed only when every turn has complete, nonnegative, nonoverflowing usage;
otherwise it remains unknown. Failed/canceled outcomes never expose partial text.
The optional `route_estimated_cost` is reconstructed from task-start facts and
summed across top-level fallback roots only.
A pending submission cancellation overrides delivery without rewriting the
recorded execution history.

The result and a `terminal_history` recovery record are committed atomically,
and the former owner is fenced. This path performs no model, tool, fallback,
evaluation or audit calls and does not add fitness evidence. Audit results are
not reconstructed: `audit_status` is `not_recovered`, and existing audit records
remain independently inspectable. Partial conversations and uncertain work are
not resumed by this mechanism.
See the [crash-recovery qualification matrix](docs/crash-recovery-matrix.md) for
the tested provider, tool, compaction, fitness and worker transaction boundaries.

## Automatic routing and local knowledge

Record operator feedback on a completed task with `darwin feedback --db path --task TASK_ID --outcome accepted --attempt-cost 0` (or `rejected`). Supply the observed final model-attempt cost explicitly; zero is appropriate only when known. This updates immutable user-feedback evidence and domain fitness atomically. Identical retries do not add samples; conflicting initial feedback or a pre-existing evaluation requires the explicit revision workflow below. Feedback covers the final attempt, not every preceding tool/model turn. No model tool can invoke this adapter. CLI submission output contains no task contents. The authenticated HTTP submission endpoint uses the same initial-record rules.

Explicit subjective corrections are available through `darwin feedback show --db path --task TASK_ID`, then `darwin feedback revise --db path --task TASK_ID --expected EVALUATION_ID --outcome accepted` (or `rejected`), or through the HTTP endpoints above. Revisions preserve original evidence and execution measurements, adjust only the quality contribution, and retain one sample per attempt. A user assessment can supersede subjective judge/user evidence, not objective test/tool evidence. Stale revisions conflict; identical retries are idempotent. The revision chain is capped at 100 revisions. Separate objective/subjective fitness dimensions remain unfinished; stored advisory audits are not automatically converted into judge fitness contributions.

Opt in to the built-in `read_file` tool with a narrow workspace directory:

```yaml
tools:
  enabled: true
  read_root: /absolute/path/to/workspace
  max_turns: 8
```

This allows local models to read UTF-8 regular files up to 64 KiB within that directory. Relative paths and symlinks cannot escape the configured root. Do not include credentials or other files the model should not see in this scope. Enabling file tools excludes cloud execution; explicit cloud selection is denied. Tool results become sensitive durable session content. Tools default off. Separately opt in to `create_enabled` and an absolute `create_root` for terminal-reviewed new-file creation; see [reviewed file creation](docs/reviewed-file-creation.md). Opt-in `replace_enabled` with an absolute `replace_root` supports [reviewed existing-file replacement](docs/reviewed-file-replacement.md), with exact old/new content and retained recovery copies. Delegated writes and unattended approvals remain unfinished. Tool-enabled models require `context_tokens` metadata. Each turn checks serialized context including tools and schemas plus a 1,024-token reserve; overflow ends the task without discarding durable tool results. Tokenizer-based accounting, automatic compaction and budget-exhaustion recovery remain unfinished.

Use `--model auto` (or API model `auto`) to select an eligible model using durable domain fitness. Configure each model's `context_tokens`, `estimated_cost`, and local `ram_bytes`; missing metadata fails closed. Context admission currently estimates serialized input bytes plus a 1,024-token reserve. Cost and memory estimates are trusted operator inputs, not measured guarantees. Successful model discovery is cached for up to five seconds per provider/endpoint/credential/privacy identity; execution failures invalidate it. Failed discovery is not cached. Explicit and automatic local reservations share one application service; discovery caches are also service-local. Neither is shared across separate processes.

Automatic routing defaults to a zero-cost ceiling. CLI routing controls are `--domain`, `--profile`, repeated `--capability`, `--context-tokens`, `--max-cost`, and `--local-required`. Native task JSON exposes corresponding `domain`, `profile`, `capabilities`, `context_tokens`, `max_cost`, and `local_required` fields. Explicit selection bypasses ranking, not resource reservations. Explicit zero cost preserves its legacy cost override, while a positive cost ceiling and requested capabilities/context are enforced.

Every explicitly selected local model now requires a positive `ram_bytes`
estimate and a fresh usable host profile. RAM/VRAM pressure, stale measurements,
reported thermal pressure or occupied local concurrency deny admission before
task storage/provider dispatch. Local requests hold their reservation through
execution and release it on success, failure or cancellation. Automatic routes
reserve once, using the same budget; auxiliary reviews and summaries also share
that service's budget. Cloud explicit execution does not require local profiling.
The `auto` local concurrency setting now adapts to measured usable headroom:
below 16 GiB permits one local execution, 16–64 GiB permits up to two, and above
64 GiB permits up to `workers.max_in_process`. Usable headroom means the memory
percentage ceiling minus observed host use and outstanding reservations. A
bound discrete-GPU request uses shared RAM headroom for the global tier and its
own GPU headroom for a per-device tier; two independent small GPUs can each run
one execution if shared RAM/CPU limits allow it. Unbound custom aggregate
profiles retain the smaller RAM/VRAM tier. Apple unified memory uses RAM only.
CPU thread count also caps automatic admission; unknown CPU count
permits one. Every candidate must still fit its declared footprint. The policy
recomputes at admission, so earlier reservations or increased pressure can reduce
the next request's limit without canceling active work. A numeric concurrency
setting keeps its fixed cap and still enforces memory/thermal checks.

These limits count active in-process local executions, not distinct resident
models. Capacity denial rejects by default; bounded waiting is opt-in below.
The runtime does not unload resident models or dynamically resize contexts.
Existing model allocations may overlap observed
host use and reservations; admission deliberately takes no credit for that overlap.
Setting `hardware.auto_profile: false` disables service host measurements. No
manual profile source is configured yet, so local execution is then unavailable
even with a numeric concurrency limit; eligible cloud execution remains possible.
Use a persistent daemon/service for shared reservations: separate one-shot Go
calls and independent CLI processes cannot coordinate this in-memory budget.
Detailed health also reports local models without RAM metadata as unavailable
(`model_metadata_missing`), even if the provider's catalog lists them. Health
remains a coarse observation, not a reservation or guarantee of task admission.

### Explicit discrete-GPU bindings

For a Linux backend already pinned to a device, set a local model's `gpu_device`
to `nvidia:GPU-<UUID>` or `amd:cardN`, using the identifier from `darwin resources`.
Provide positive conservative `ram_bytes` and `vram_bytes` footprints. Both
host and device observations must be fresh; missing devices, failed probes,
ambiguous inventories and Apple unified-memory/double-pool configurations are
denied before dispatch. Device percentages and outstanding reservations apply
per device; host RAM, CPU and worker ceilings still apply across the service.

This is **operator-declared placement**, not automatic backend affinity control.
The backend must actually use that device; a wrong declaration can make resource
accounting unsafe. Use separately pinned backend instances where needed. AMD
card indices may change after reboot; recheck them. Multi-device sharding, MIG
partition binding, automatic placement verification, cross-process reservations
and live Linux GPU qualification remain unfinished. Unbound VRAM requests still
fail with the built-in profiler rather than choosing an arbitrary GPU. Legacy
custom aggregate profiles cannot hold overlapping aggregate/device reservations.

Bound configurations perform GPU measurements during local/automatic admission
and auxiliary review/summary admission. Driver latency therefore adds routing
overhead (NVIDIA's subprocess is bounded to one second plus pipe cleanup);
the routing latency target is not yet qualified on these systems. Cloud-only
services do not enable these probes. Persisted automatic route records omit the
full inventory and hardware identifiers, keeping only the selected device's
scalar capacity observation. Adding or changing a binding changes the durable
submission configuration fingerprint; queued work requires the existing explicit
configuration-mismatch handling rather than silently moving devices.

### Bounded resource-pressure waiting

```yaml
hardware:
  local_pressure_policy: wait  # Default: reject
  local_queue_timeout: 30s     # Allowed: 100ms through 5m
```

Waiting applies to explicit and automatic task admission, including detached
submissions executed by the daemon. A capacity-denied request replans about every
250 ms (one quarter of the allowance for sub-second timeouts) until it is
admitted, canceled or its queue allowance expires. Automatic
routing repeats admission planning using current storage and the existing
five-second model-discovery cache. Eligible cloud alternatives are tried before
waiting; explicit model selection and local-required/privacy constraints are
never silently changed. Missing metadata, invalid/stale measurements, disabled
profiling and provider/policy errors do not trigger pressure retries.

The queue allowance begins after acquiring the service execution slot; waiting
for that slot remains governed by the caller's overall deadline. Waiters occupy
those slots, so at most `workers.max_in_process` requests are active or waiting
inside admission. This is bounded polling, not FIFO fairness, an extra durable
queue, or a dedicated cloud-capacity reservation. Direct auxiliary review and
summary commands still reject resource pressure rather than use this wait policy.

Once admitted, provider/tool execution uses the original caller deadline, not
the admission timeout. No already-started task is replayed by pressure waiting.
Admission timeout returns an error without creating a task; automatic planning
can still initialize routing storage. A detached submission keeps its existing
running claim and heartbeat while waiting, with no task ID until execution starts;
submission cancellation remains the way to cancel it at that stage. Its wait is
not a fresh submission or retry of model/tool effects. Existing explicit fallback
rules remain unchanged; an allowed fallback has its own admission allowance within
the caller's overall deadline.
Pressure expiry finalizes a detached submission as failed, distinct from explicit
submission cancellation. Waiting for the shared profiling lock is cancelable;
custom in-process profilers must still honor the context they receive.

These new configuration fields participate in submission configuration fingerprints.
Inspect pending submissions when upgrading: work pinned to an older fingerprint
must be explicitly canceled/resubmitted under a new key rather than silently run
under changed configuration.

Automatic execution may traverse an ordered chain of at most32 route attempts
after provider-declared retryable first-turn failures with no text, steering or
tool proposals and a successfully persisted failure. Alternatives prefer new
failure domains and are rechecked for current eligibility, resources and
remaining estimated-cost budget before dispatch. An alternative that becomes
ineligible without starting a task can be skipped; partial output, validation
failure, tool activity, cancellation, persistence ambiguity and confirmed or
uncertain effects stop the chain. Local-task privacy remains local on every
fallback. Each executed attempt has its own durable task ID and references its
immediate predecessor; CLI/native results list all previous attempts in order.
Returned text and finish reason belong to the final attempt. Native
`route_estimated_cost` sums the configured estimates for all admitted
top-level route attempts; it excludes delegated workers, audits and actual
provider billing. Token usage is present only when every route attempt has
complete durable counts, so today's retryable no-output fallback chains report
usage unavailable rather than exposing final-attempt tokens as a request total.
Explicit model requests do not auto-fallback. Validation failure intentionally
stops automatic fallback; bounded coordinator repair remains a separate explicit
workflow. HTTP413 and the exact OpenAI-compatible
`context_length_exceeded` error normalize to a durable, non-retryable
`context_overflow`; untrusted error prose cannot grant retry authority.
Adaptive retry policies remain unfinished. Loopback qualification exercises an actual
Ollama `503` followed by an OpenAI-compatible SSE completion, with distinct
provider endpoints, exact cumulative estimated-cost admission, durable retry
lineage and redacted route attribution. The paired local-required case proves
that the cloud adapter receives neither discovery nor inference traffic. This
is protocol-fixture evidence, not a live-provider availability claim.

`darwin memory list|show|export|put|delete --config path` inspects and maintains factual
memory through the configured scope and credential-redaction boundary. Put reads
a complete fact as strict JSON from stdin and requires `--expected 0` for creation
or the current revision for correction. Delete requires `--id` and `--expected`.
Storage must already exist. The explicit `--db path --scope scope` form remains
legacy raw-storage access without configured credential redaction; never mix it
with `--config`. Deletion is logical, not secure erasure of WAL or backups.

The CLI, daemon and Go SDK provide configured-scope memory inspection,
creation/correction and revision-checked deletion. CLI `memory export --config`,
SDK `ExportMemory` and authenticated POST `/v1/memory/export` also return one
consistent scoped snapshot of all current
facts, including expired/private facts, with configured-secret redaction. The
bounded snapshot supports at most 1,000 facts and an 8 MiB encoded envelope;
overflow fails instead of silently truncating or collecting inconsistent pages.
It is not a database backup or restore operation. These operations remain
available when prompt retrieval is disabled and do not invoke models. See
[operator memory management](docs/memory-management.md) for the authenticated
HTTP contract, redaction, storage requirements and uncertain-write handling.

`darwin skills list|show|history|state|draft|rollback --root path --scope scope` maintains procedural skills. Draft reads strict JSON from stdin; rollback requires `--name` and `--expected-version`. Inspection never initializes stores. Activation still requires a trusted programmatic validator; these commands do not enable automatic skill mutation. Treat memory and skill exports as sensitive.

Rollback undoes the latest activation that has not already been reversed, not
the latest appearance of a version ID. Reactivating an older version therefore
does not make past undo operations reusable. Rollback stops at the first active
version; it does not deactivate that version or delete immutable drafts. Reads
validate the complete activation chain, validation evidence and final active
pointer. Inconsistent histories are rejected without automatic repair or file
changes. `--expected-version` alone guards the current version, not a unique
activation epoch. Read `skills state --name NAME` and supply its `revision` as
`rollback --expected-revision REVISION` alongside its active version to reject
stale observations even after A→B→A changes. Draft-only changes preserve the
revision. This is a concurrency check, not authorization or an automatic
regression detector.

Trusted Go hosts can call `FileStore.RevalidateAndRollback` with an observed
activation state and a deterministic validator after enabling automatic changes
with `SetAutomatic(true)`. A valid failed check restores the predecessor and
atomically records its evidence ID in the activation history. Passing checks do
not change history; validator errors, panics, cancellation, stale revisions and
the kill switch prevent rollback. The callback has a cooperative three-second
deadline and is not sandboxed. `skills history` now includes transition metadata
and regression evidence without workflow bodies. There is no daemon-triggered
regression monitoring or statistical outcome detector yet; model-only judgments
cannot drive this operation.

`FileStore.DraftFromWorkflows` lets a trusted host generate an inactive skill from
2–20 successful examples in one domain, from distinct tasks and sessions.
Evidence uses the normal deterministic/tool/user precedence; judge-only examples
are rejected. The host must supply verified, redacted completed-work records.
Session and winning evidence IDs are recorded from those inputs, overriding any
generator-proposed provenance. Generation is cooperatively bounded to 30 seconds,
and the automatic-change switch is checked again before persistence. Drafts still
require separate deterministic validation and activation. A model-backed generator
must use the host's privacy, routing and cost controls; this callback API does not
install background workflow discovery.

`skills.ModelGenerator` implements that callback using one tools-free provider
call. It enforces context admission, a maximum 30-second cooperative timeout,
estimated cost limits and a strict 64 KiB workflow-only response schema. Tools,
partial completion, malformed output and invented provenance are rejected.
`GenerateDetailed` additionally returns the model, optional reported token usage
and elapsed time; missing usage remains unknown. The host must supply an admitted
policy-bound provider and reserve resources, redact content and durably record
the attempt. There is no retry or activation inside the generator, and daemon
routing/scheduling of generation is not yet wired.

`ModelGenerator.GenerateRecorded` adds durable execution tracking through a
`skills.GenerationRecorder`. SQLite schema16 implements the recorder: each
attempt ID can be claimed once, and completed proposals or sanitized failures
are stored atomically. The initial record precedes estimation and inference.
Repeated IDs never dispatch again; terminal persistence failure returns the
started observation for inspection, not permission to retry. Cancellation still
allows a bounded terminal write. Drafted records are proposals, not published or
active skills. SQLite inspection supports individual IDs and scoped pagination;
generation and publication are available through the Go service/SDK, CLI and
authenticated native HTTP API.

The application service and Go SDK now provide `GenerateSkillDraft` for explicit
task IDs and a configured generator model. It selects current accepted
final-attempt evaluations from completed tasks in one read-only snapshot, rather
than accepting caller-declared success. It preserves paired tool history, ignores
system prompt material, and requires distinct sessions and a common domain.
Newer negative user feedback disqualifies an earlier positive example. Generation
honors skill scope/auto-draft settings, deployment mode, source privacy, context,
resource and cost limits. Inputs and output are redacted before dispatch/storage;
the returned record is a durable proposal, never an active skill. Explicitly
enabled background learning can drive selection and generation. The experimental
signed-in `codex_app_server` path now supports exact Sol skill drafting with a
closed output schema, lazy launch after durable admission and no tools. See
[Codex skill drafting](docs/codex-skill-generation.md) for privacy and test limits.

Go hosts can now call `DiscoverSkillWorkflows(ctx, domain, after, scanLimit)` to
find current accepted examples without supplying task IDs first. It scans 1–20
task records per page and returns metadata only: task/session identity, domain,
privacy, evaluation/source digests and journal sequence. `Next` advances across
ineligible tasks too; a full page may have no candidates and still have a cursor.
Continue until `Next` is empty (the last full page may require one empty read).
Pages are live observations, not one frozen snapshot. Choose distinct sessions
and let generation revalidate the resulting 2–20-task set. Discovery never
opens the skill root, dispatches a model, publishes or activates a skill.
Background scheduling and semantic grouping of repeated workflows remain open.

Go hosts can also use durable discovery/consumption through
`AdvanceSkillWorkflowScan` and `ConsumeSkillWorkflowScan`. Consumption groups
matching observed tool sequences across saved pages, refreshes the source
evidence transactionally, and saves a repeat-safe receipt. Read-only receipt
and bucket inspection survives restarts. Groups remain heuristic candidates;
selection planning and generation must revalidate them. See the
[SDK learning workflow](sdk/v1/README.md) for revision handling, bounds and
privacy requirements. Background scheduling and automatic skill activation
are not implied by these operations.

Operator surfaces expose that same read-only discovery:

```sh
darwin skill-generations discover --config /path/to/config.yaml --domain creative --scan-limit 20
```

The authenticated HTTP equivalent is
`GET /v1/skills/workflows?domain=creative&scan_limit=20`. Supply the returned
cursor with CLI `--after` or URL-encoded HTTP `after` to continue. Limits count
scanned records, not matches; duplicate/unknown parameters and empty explicit
cursors reject. The route shares task capacity and has a cooperative ten-second
deadline. Neither surface creates storage or invokes a model.

Discovery inspects the configured database for one operator; a skill scope is
the destination catalog scope, not tenant or source-project isolation. Protect
candidate metadata exports and use separate databases where isolation is needed.

The storage layer also supports immutable `WorkflowSelection` records. A stable
selection ID binds the destination skill, trusted grouping-rule identity,
configured model, policy digest and complete source provenance. Its creation
time is excluded from the hash; repeated saves return the first committed record.
Schema 17 adds the selection ledger without changing existing generation records.
Storage does not verify that a caller's grouping is semantically meaningful or
that source feedback is still current. Saving a selection neither dispatches
inference nor reserves a generation attempt; scheduler integration is unfinished.

Generate from two or more accepted completed tasks, then publish explicitly:

```sh
darwin skill-generations generate --config /path/to/config.yaml --id workflow-attempt-1 --model generator --name review-workflow --tasks TASK_A,TASK_B --max-cost 0
darwin skill-generations show --db /path/to/darwin.db --scope project --id workflow-attempt-1
darwin skill-generations publish --config /path/to/config.yaml --id workflow-attempt-1
```

Both mutation commands require a configuration file and take scope exclusively
from its skill settings. Generation accepts 2–20 distinct task IDs from separate
sessions in the same domain. Enable `skills.enabled` and `skills.auto_draft`,
configure `skills.root`/`skills.scope`, and supply generator context/cost metadata
(plus RAM estimates for local models). The default cost ceiling is zero.
Generation IDs are single-use: on failure or lost output, inspect the saved
attempt rather than automatically retrying inference. Publication retries with
the same saved attempt are safe and return the existing version. Neither command
activates a skill. JSON output includes workflow content and should be protected.

The daemon exposes the same actions with bearer authentication and no browser
origin or query parameters. Send `Content-Type: application/json`:

```http
POST /v1/skills/generations

{"version":1,"id":"workflow-attempt-1","model_id":"generator","name":"review-workflow","task_ids":["TASK_A","TASK_B"],"max_cost":0}
```

```http
POST /v1/skills/generations/workflow-attempt-1/publish

{"version":1}
```

Scope remains bound to daemon configuration, not request input. `max_cost` is
optional and defaults to zero. Requests are limited to 8 KiB, share task capacity,
and use cooperative 30-second generation / 10-second publication deadlines.
Successful responses are HTTP 200 with the saved attempt or immutable version.
Malformed input returns 400 (oversized bodies 413, wrong media type 415); capacity
or missing adapters return 503. A service failure returns a generic 422 and does
not establish whether an attempt committed: inspect the caller-chosen ID before
deciding what to do next. Neither endpoint retries inference or activates skills.
Model names in stored attempts are provider names and may differ from the
configured `model_id` alias.

Inspect saved generation without re-running it:

```sh
darwin skill-generations list --db /path/to/darwin.db --scope project --limit 25
darwin skill-generations show --db /path/to/darwin.db --scope project --id ATTEMPT_ID
```

Lists contain metadata only; show includes the saved proposal and may contain
sensitive content. Authenticated HTTP equivalents are
`GET /v1/skills/generations?scope=project&limit=25` and
`GET /v1/skills/generations/ATTEMPT_ID?scope=project`. List pagination uses an
exclusive `after` ID, in lexical order, with limits1–100. All reads are scoped,
bounded and non-mutating; missing databases are not created or upgraded.
A started record remains uncertain and does not authorize redispatch.

`PublishSkillGeneration(ctx, attemptID)` publishes a saved drafted proposal as an
inactive immutable skill version. It requires enabled skills, auto-draft and the
configured root/scope, and rejects injected retrieval stores. Exact retries
return the same version; conflicting reuse of an attempt ID rejects. Current
credential collisions reject before opening the skill root. Publication does not
recheck source-feedback freshness or validate/activate the workflow.

The first publication upgrades the file catalog to schema 2, so older writers
reject it instead of dropping receipts; SQLite now uses schema 17. Version
visibility and its receipt share one catalog commit. A failure before that commit
can leave an unreferenced version file; automatic cleanup and power-loss
qualification remain open.

Go hosts can validate and activate a published version through
`SkillActivationState` and `ActivateSkillVersion`, using the configured scope
and an observed activation revision. The activation setting must be enabled and
a trusted deterministic validator must accept the actual candidate. Model review
alone is not activation evidence. Validators are cooperative read-only host code,
not sandboxed commands; they must obey privacy/tool policy and support repeated
invocation. Stale revisions, failed validation, credential collisions and
callback errors reject without activating the candidate. This does not schedule
automatic learning or expose remote proof submission through CLI/HTTP.

`ActivateSkillVersionOnce` adds durable operation IDs for retry after a lost
acknowledgement. A matching receipt never reactivates a version after rollback.
Inspect it with `SkillActivationOperation`; validation and policy gates still
apply. See [activation operations](docs/skill-activation-operations.md) for the
file-catalog schema-3 upgrade and remaining controller work.

Trusted Go hosts may explicitly start `StartLearningWithValidation` with a named
deterministic validator. A SQLite activation intent is saved before validation;
operation receipts make restart acknowledgement safe after activation or rollback.
The ordinary daemon remains draft-only. See
[validated learning](docs/background-learning.md#opt-in-validated-learning-for-go-hosts).

`RevalidateSkillVersion` provides host-driven deterministic recovery: with
`skills.rollback_on_regression` enabled, a trusted deterministic failing check
can restore the validated predecessor of the observed active version. Passing
checks are non-mutating; callback errors or model opinions do not cause rollback.
The returned state identifies the version checked, so inspect activation state
again after rollback. New activation may be disabled independently. Go hosts can
explicitly start `StartSkillRegression(ctx, interval, validator)` for periodic,
scope-wide checks with caller-owned cancellation and health. Ordinary validation
failures advance to later keys while health stays degraded. The monitor does not
infer statistical regressions from task outcomes or run generated test commands.
See [regression monitoring](docs/skill-regression-monitor.md) for policy, restart,
admission-failure and cooperative-shutdown limits.

For a durable check receipt, Go hosts can use `RevalidateSkillVersionOnce` with
an operation ID, stable validator identity and exact expected activation. Passes
retain an audit receipt without changing activation revisions; failure receipts
and rollback commit together. Exact retries do not rerun validation or rollback.
This schema-4 catalog extension leaves the legacy periodic monitor unchanged. See
[regression operations](docs/skill-regression-operations.md) for compatibility,
inspection and limits.

For restart-safe scheduling, opt into `StartDurableSkillRegression` or drive
`DurableSkillRegressionStep` with a stable monitor name and validator identity.
The schema-5 catalog persists cadence, exact pending checks and failure fences;
callbacks may run concurrently, but closed failed checks cannot commit a late
rollback. See [durable monitoring](docs/durable-skill-regression-monitor.md) for
bounded retention, policy bindings, inspection and remaining daemon limitations.

`darwin audit --config path --task TASK_ID --reviewer MODEL_ID --max-cost 0` reviews saved output with an independent configured model. The reviewer requires context and cost metadata, plus memory estimates for local execution. Local history cannot be reviewed in the cloud. Shared services reserve local resources during review; separate CLI processes do not share reservations. Review calls have no tools and no automatic retries. Configured credentials are redacted from review inputs and findings; other sensitive content still requires operator care.

The experimental signed-in Sol CLI provider now supports this audit service,
including structured generation and independent result validation. See the
[Codex output-audit guide](docs/codex-output-audits.md) for privacy requirements,
supervised commands, live evidence and remaining limits.

To audit successful tasks automatically, set `evaluation.auto_review_model` to a configured independent model ID and `evaluation.auto_review_max_cost` to an estimated cost ceiling (default zero). `evaluation.llm_judge_enabled: false` disables manual and automatic review. Automatic review runs synchronously after task completion, adds up to a minute within the request deadline, and reports `audit_id`/`audit_status` through native task responses and CLI stderr. A failed review does not change the completed candidate task. OpenAI-compatible responses do not expose these native audit fields. Model estimates are not billing guarantees.

The orchestrator's reviewer can assess code and missing deliverables, but its
opinion is not a compiler, test runner, or proof of completion. The current
evidence policy distinguishes these cases:

| Observation | Treatment |
| --- | --- |
| Blank required final answer | Objective validity failure; tool-only intermediate turns are excluded. |
| Nonblank answer repeats the request, promises future work, or omits deliverables | Reviewer cites requirements and output; the finding remains advisory. |
| Suspected code defect | Advisory code review; claims about executed tests require supplied execution evidence. |
| Creative quality or taste | Bounded advisory influence; explicit user feedback takes precedence. |
| Insufficient evidence | Reviewer may abstain; no success is invented. |

User feedback removes the reviewed attempt from the advisory population, and
conflicting advisory evidence from other attempts cannot reverse the direction
of direct quality feedback. Objective validity remains separate: liking an
answer does not erase a mechanically verified failure. Review is opt-in, and
the current automatic hook audits successful runs only; eligible failed tasks
can be reviewed explicitly. It does not automatically repair or rerun a task.

Reviews receive separately citable `execution_<sequence>` records for persisted
tool completions and validation outcomes, in addition to conversation history.
Each record identifies its turn and attempt, so earlier tool work is not confused
with final-answer validation. A `candidate_execution` reference identifies the
final answer's turn, attempt and completion sequence. Tool error codes, side-effect status and explicit
validation booleans remain distinct; tool completion alone does not prove tests
passed. The projection excludes raw tool text and arguments, scrubs configured
credentials, and admits at most 250 records totaling 64 KiB. Oversized metadata
denies review instead of silently omitting failures. These references identify
events within the audit's task, not independently verified external receipts.

`darwin audits list --db path --task TASK_ID` and `darwin audits show --db path --id AUDIT_ID` inspect immutable advisory audits. `darwin audits attempts --db path --task TASK_ID` inspects admitted review lifecycles, including failures. Lists accept `--after` and `--limit` (1–100); inspection never creates storage. Audit records are separate from fitness and retain reported usage rather than fabricated dollar costs. Findings may contain sensitive content, so protect exports.

Review execution persists `started` before calling the reviewer, then records the validated audit and `completed` status in one transaction, or `failed` with a generic code. Cancellation cleanup has an independent five-second storage deadline. Admission denials do not create attempts. A crash or storage failure can leave an attempt `started`; this means indeterminate, not proof that a review is still running. Automatic reconciliation is not implemented. Failed reviews never become candidate performance evidence. The standalone audit-storage API remains available for imported records; only `CompleteReview`, used by application execution, guarantees atomic audit/lifecycle persistence.

## Durable lifecycle metrics

`darwin metrics --db /absolute/path/to/darwin.db` reads a versioned JSON snapshot
from existing storage. The daemon exposes the same snapshot through authenticated
`GET /v1/metrics`, with a separate one-request diagnostic slot. Requests with a
body, query parameters or browser origin are rejected. Missing or unreadable
storage returns an error, not a fabricated empty population.

Metrics snapshot version 7 contains fixed groups for tasks, submissions, review attempts,
evaluation records, audit records, submission recovery records and all sixteen
canonical durable runtime-event kinds. Tasks,
submissions and reviews are grouped by stored lifecycle state; the other groups
count stored records. A separate fixed audit-outcome group counts accept,
reject and abstain verdicts without evaluator, candidate, evidence or finding
identity. These remain advisory: they do not assert objective correctness,
replace user feedback or become direct fitness samples. Counts come from one SQLite read transaction and survive
service restarts. Older schemas explicitly mark unsupported groups unavailable.
No prompts, output, task/model/provider IDs, paths or arbitrary labels are included.

The `queue_age` group classifies every currently queued submission into one of
seven fixed age buckets from less than one second through at least one hour, or
`invalid_time`. Its total must equal the durable queued-submission count. This
distinguishes a fresh queue from sustained wait pressure without exposing exact
arrival times or submission identity; it is not an arrival rate, service rate,
historical wait distribution or execution-duration measurement.

Schema-28-and-newer stores also expose cumulative provider-turn and tool-call
duration histograms derived from paired durable events. Missing starts, missing
ends, invalid/future timestamps, and backward clocks remain explicit unavailable
samples and reconcile with the canonical event counts. Labels are limited to
`provider` and `tool`; model, provider, tool, task, turn, attempt and call
identities remain private. These durations are lifecycle wall time—tool timing
can include approval waits—not provider-reported server latency or live in-flight
measurements.

Application-backed snapshots also contain a fixed live resource block for CPU
threads, RAM, swap, aggregate VRAM, thermal pressure and unified-memory status.
Every measurement carries an availability bit, so unsupported probes and
profiling failures are not reported as observed zero. Cloud-only or
profiling-disabled services report explicit unavailability without probing;
the storage-only CLI omits live resources. OTLP exports only fixed measurement
names and values—never device IDs, GPU inventory, profiler provenance, thermal
state strings or host identity. Resource values are instantaneous host readings,
not queue depth, model residency or reservation accounting.

Runtime-event counts cover provider turns, tool calls, worker lifecycle, routes,
steering and errors without exporting event envelopes or payloads. These are
joined by fixed operation counts for fallbacks, compactions, skill-context use,
exploration and capacity/budget/privacy/health route exclusions. These are
current stored-population **gauges**, not monotonic counters, validated
success rates or proof that a `running` task/`started` review is alive. A completed
task is not necessarily semantically correct. Evaluation revisions are not
additional base evaluations; audit and recovery counts do not rate candidate
quality. The reader checks lifecycle metadata, not every underlying event or
opaque record body. Use health, task history and audit inspection for that detail.

The storage query has a three-second context limit, the application a four-second
limit, and the HTTP route a five-second limit. Count queries and database integrity
checks scale with stored data and may time out on large or pressured databases;
only response size and label cardinality are fixed. Reads neither migrate storage
nor dispatch inference. An explicit CLI/SDK [OTLP metrics export](docs/metrics-export.md)
can now send one aggregate snapshot through the network policy. Opt-in daemon
configuration and an owned SDK exporter also support periodic fresh snapshots,
supplemental health and cancellation. The legacy `opentelemetry_enabled` switch
now aliases configured periodic metrics export. A separate explicit CLI/SDK
[OTLP trace export](docs/traces-export.md) sends up to 32 recent terminal task
roots with paired provider/tool/worker children and fixed route, evaluation,
fallback, compaction, skill-context, steering, error and top-level submission
queue-residency observations. Queue waits use seven fixed buckets and expose no
submission identity or exact arrival time. Every paired tool completion also
emits its fixed none/confirmed/uncertain effect class without tool identity or
result content. It uses
fresh non-durable wire IDs and no session content or durable identities.
Fixed route-constraint observations expose only mode/privacy/health/policy/
credential/capacity/context/budget/capability reason classes, never candidates.
`telemetry.trace_export` enables an
independent sequential daemon exporter, while SDK hosts can own one explicitly;
health is supplemental and delivery remains best effort. The legacy
`opentelemetry_enabled` alias remains metrics-only.
Schema29 also supplies [task-duration histograms](docs/task-duration-metrics.md)
and explicit unavailable timing counts, including recovery terminals. Traces,
outside this bounded lifecycle slice, queue arrival/service rates,
per-device pressure, cost histograms, retention and production-scale
observability qualification remain unfinished.

## Next sprints

1. Add approved single-writer tools and interrupted delegation-tree recovery.
2. Expand safe fallback qualification and automatic validated outcome updates.
3. Add automatic context summarization and knowledge maintenance beyond current operator-compacted continuation, scoped factual memory and validated procedural-skill retrieval.
4. Add live events, recovery and cross-provider qualification.

See [implementation evidence](docs/progress.md) for completed local work and remaining checks by Linear issue.

See the [Linear project](https://linear.app/darwinrouter/project/darwinrouter-mvp-fc9fe6d48fda) for the full delivery backlog.
