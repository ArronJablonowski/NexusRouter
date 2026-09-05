# Embedded Go client

Import `github.com/ArronJablonowski/DarwinRouter/sdk/v1` with Go1.27.1 or newer.
The versioned client is under development; this is not a tagged stable release.
The snippet below assumes the alias import
`darwin "github.com/ArronJablonowski/DarwinRouter/sdk/v1"` and standard `os`.

```go
client, err := darwin.New(darwin.ConfigOptions{
    ProjectFile: "config.yaml",
    LookupSecret: os.Getenv,
})
if err != nil { return err }
result, err := client.Run(ctx, darwin.Request{
    Version: 1, ModelID: "auto", Prompt: "Summarize this public text...",
})
```

The client embeds the same application service as the CLI and daemon, rather
than calling an HTTP server. Reuse one client for concurrent tasks so resource
reservations share one budget. Separate clients and separate processes do not
share an in-memory hardware budget. Construction does not start a daemon or hold
an open task database; no `Close` is needed. Calls open bounded-lived storage as
required. Callers own cancellation and should set appropriate deadlines.

Configuration is explicit: defaults → `UserFile` → `ProjectFile` → `Environment`
→ `Overrides`. Maps contain scalar YAML paths such as `mode` or
`workers.max_in_process`, not raw operating-system environment variable names.
The SDK does not discover configuration files or read process environment
implicitly. `LookupSecret` resolves configured environment/secret names; supply
`os.Getenv` or a concurrent-safe secret-store function. Never put credentials in
override maps. Files and maps use the same strict validation as the CLI.

### Replaceable resource measurements

Set `ConfigOptions.ResourceProfiler` to a `ResourceProfiler` implementation with
`Measure(context.Context) (resources.Measurement, error)`. Return
`Measurement{Version: 1, Snapshot: measuredSnapshot}` with fresh, truthful
measurements. `resources.HostProfiler{IncludeGPUs: true}` provides an optional
built-in implementation. Nil retains normal configured host profiling; an explicit
engine also supplies manual measurements when `hardware.auto_profile` is false.
Without an engine, disabling automatic profiling still denies local execution.

Admission retains its configured RAM/VRAM percentages, freshness checks, model
estimates, privacy rules and shared concurrency reservations. Unknown measurements
must remain unknown, not optimistic defaults. Errors, panics and incompatible
measurement versions fail closed; snapshot data is detached and its source label
normalized before use. Providers remain responsible for accurate facts: the runtime
cannot prove that an in-process callback measured the hardware honestly.

Engines are trusted Go code, not sandboxed plugins. They must honor cancellation,
support concurrent calls, avoid unauthorized network access, and return data they
do not mutate concurrently. A noncooperative callback cannot be forcibly stopped
without leaving work behind. Engines are not serialized into queued requests;
daemon execution uses the daemon's configured measurement source, not an SDK
callback. This interface replaces measurement only; the wider extension and
budget-recommendation contracts remain unfinished.

### Replaceable provider construction

Set `ConfigOptions.ProviderFactory` to a `providers.Factory` implementation
(`sdk.ProviderFactory` is an alias). Its `Build(ctx, providers.Connection)` returns
the public provider-neutral `providers.Provider`. Nil retains built-in adapters.
Admission still checks configured model capabilities, deployment mode, privacy,
context, resource capacity and budgets before execution. Injection does not add
new configuration kinds or let a model select arbitrary endpoints.

The version-one connection supplies provider ID, kind, endpoint, resolved API key
and an origin-restricted HTTP transport. Custom adapters must use that transport
for every network request, avoid logging credentials, honor cancellation and
implement the provider streaming contract (ordered chunks and verified completion).
Connection credentials are sensitive and must not be retained beyond their need.
The factory and returned providers are trusted in-process code, not sandboxed
plugins; policy cannot prevent arbitrary Go code from opening another transport.
Callers own concurrency safety and any external resources; the runtime does not
call a custom provider's Close method. Custom engines are not serialized into
queued submissions: daemon execution uses its own configured factory.

Explicit execution and provisional live text use the same injected adapter,
with normal runtime validation, redaction, journaling and cancellation. This is
provider construction, not a general extension-hook API.

### Read-only tool extensions

`ConfigOptions.Tools` accepts up to 16 `sdk.Tool` definitions (an alias for
`tools.Definition`), each with a provider-neutral JSON schema, fixed resource
scope, `ReadOnly: true`, and a context-aware handler. `ToolPolicy` governs only
the supplied custom definitions, not built-in tools, and accepts a
`tools.Policy`: only explicitly allowed effective permissions advertise or execute
a custom tool. Nil policy denies all tools; `Ask` does not grant approval.
Inherited denials remain effective. Definitions, schema bytes and the policy
chain are snapshotted by `New`, which rejects invalid schemas before storage.

For example, a policy for a registered `lookup_fact` tool can be supplied as:

```go
ToolPolicy: &tools.Policy{
    Default: tools.Deny,
    Rules: []tools.Rule{{Tool: "lookup_fact", Scope: "public_facts", Decision: tools.Allow}},
},
```

Custom tools do not enable filesystem access: `tools.enabled` controls the
built-in workspace reader separately. Names `read_file`, `delegate` and
`delegate_batch` are reserved. Extension execution requires a local model with
a known context capacity; ordinary context, resource and iteration limits remain
active. Delegated children do not inherit custom handlers. Handlers are trusted
in-process code, must truly be read-only, return `runtime.NoEffect`, obey
cancellation, and support concurrent tasks. They are not sandboxed or forcibly
interruptible, and arbitrary Go code can bypass transport policy. The caller
owns handler resources; no automatic Close is invoked. Synchronous `Run` and
streaming execution support extensions. `Submit` and submission-bound execution
reject active extensions because handlers have no durable implementation identity
yet; they are never silently serialized, dropped, or substituted on dequeue.
Durable extension identity and general lifecycle hooks remain unfinished.

### Operator-reviewed tool extensions

Set `ConfigOptions.ApprovalReviewer` to explicitly register write handlers and
effective `Ask` tools. Its type is
`func(context.Context, sdk.ApprovalPrompt) (actor string, allowed bool, err error)`.
The embedding application must authenticate the operator and obtain their
decision; do not use a model evaluator or an unconditional approval callback.
Nil preserves the read-only registration behavior described above. An inherited
denial cannot be overridden, and even an `Allow` write requires review per call.

The prompt includes a versioned request with task/call identity, exact scope,
argument/schema/policy digests and expiry, plus an isolated copy of the exact
arguments and tool description. Treat arguments as untrusted model output:
render them safely for inspection, never execute them as review instructions.
The copy may contain secrets. Raw preview fields are excluded from JSON
serialization and the approval ledger, but your callback must still avoid
logging or retaining them unnecessarily. Mutating the preview cannot change
the arguments sent to the handler. Known configured credentials in persisted
scope/tool identity or returned actor attribution cause rejection.

Review has a cooperative one-minute deadline. The runtime records the decision,
acquires a writer lease for the exact scope and consumes approval once before
dispatch. Handlers may return `runtime.ConfirmedEffect` after successful writes;
failures or uncertain results must not be retried automatically. Cancellation
and observed lease loss cancel the handler, but ownership is not released until
it returns. Handlers must join all work they start and obey cancellation: this
is trusted Go execution, not an OS sandbox. Scope equality is exact, not a
hierarchical path lock; the host must assign overlapping resources consistently.

Reviewed tools work in local `Run`, `RunStream`, and `RunTextStream`, subject to
normal admission and iteration limits. They do not grant filesystem access or
authority to delegated children. Durable queue intake remains rejected for active
custom tools. Process-crash reconciliation of interrupted effects remains unfinished.

For separately submitted decisions, set `ApprovalPresenter` instead of
`ApprovalReviewer` (configuring both is rejected). The presenter receives the
same private preview and returns after safely displaying it; returning nil is
not approval. The runtime waits within the same one-minute deadline for a
durable decision. Call `client.DecideApproval(ctx, approvals.Command{Expected:
preview.Request, ID: stableDecisionID, Allowed: true}, authenticatedActor)` from
the operator control flow, or use the CLI/API decision controls documented in
the repository README. The actor must be authenticated by the embedding host.
The full request is matched atomically before idempotency or state transitions.
Fresh-clock retries with the same ID/action/actor preserve the original decision
and may return a now-consumed or revoked record; they never restore authority.
Denial, cancellation, expiry, failed presentation or prior consumption prevents
dispatch. Presenter authority is never inherited by delegated children.

`client.InspectApproval(ctx, taskID, approvalID)` reads one task-bound record;
`client.ListApprovals(ctx, approvals.ListOptions{TaskID: taskID, Limit: 25})`
reads a metadata page using the public `approvals` package. Set `AfterCallID` to
`page.NextAfterCallID` to continue, stopping when it is empty. Limits are 1–100.
Pages are ordered lexically by tool-call ID and are not a frozen multi-page
snapshot. Restart the scan to find new IDs sorting before a previous cursor.
These operations never invoke review or execution, create/migrate storage, or
expose raw arguments/lease tokens. Actor/scope metadata may still be sensitive;
`Consumed` is spent authority, not evidence that repeating an effect is safe.

`client.ApprovalExecutionStatus(ctx, taskID, approvalID)` returns one coherent
read snapshot of the approval, validated call completion and scope-wide writer
state. `RecordedEffect` is a durable tool report, not artifact proof. Writer
expiry does not establish process termination, and open calls do not establish
whether an effect occurred. This method never retries work or releases leases;
general interrupted-effect reconciliation is not implemented.

### Replaceable task context estimates

`ConfigOptions.ContextEstimator` implements the public `providers.ContextEstimator`
interface (also `sdk.ContextEstimator`). `Estimate(ctx, providers.Request)` sees
an isolated model request including messages, tool schemas and output schema.
Automatic routing measures each eligible model under one cooperative three-second
batch deadline; an error or oversized result makes that candidate ineligible.
The runtime measures again before every model turn and before applying steering.
Delegated tasks and auxiliary audits/summaries share the same estimator. Auxiliary
measurement sees the redacted, assembled review/summary prompt after its durable
attempt record is created; its three-second ceiling also falls within the overall
auxiliary timeout. Estimation failure prevents inference and records a failed
auxiliary attempt without changing the completed source task or accepting a draft.
Configured estimators require a known,
positive model context window.

The effective estimate is the larger of the custom value and the existing
serialized-byte estimate plus framing/output reserve. This extension cannot
lower that conservative floor, rewrite context or change permissions. Nil
preserves built-in behavior. Custom inputs are capped at 4 MiB serialized JSON;
invalid UTF-8, negative results, panics, errors and expired deadlines reject
measurement without exposing callback diagnostics. Each call receives at most
three seconds, but cancellation is cooperative and the host must ensure return.

Estimators are trusted in-process code and must perform local computation only,
honor cancellation and concurrency, and keep task data private. They are not
serialized in durable submissions: a restarting host supplies its current engine.
Canonical compaction checkpoints still use built-in estimates for reproducible
provenance; custom measurement controls actual inference admission, not those
stored values. This is the estimation component of the planned ContextEngine, not custom
context assembly or automatic semantic compaction, and is not a tokenizer-accuracy
or OS-isolation guarantee.

### Replaceable factual memory storage

`ConfigOptions.MemoryStore` accepts the public `memory.Store` contract (`sdk.MemoryStore`
is an alias). Nil retains SQLite. Injection does not enable memory: configure
`memory.enabled`, scope, privacy and context-size limits explicitly. Runtime use
is query-only; put/correct/touch/delete/expiry operations remain explicit actions
on the supplied store. The caller owns that store's lifetime and concurrency.

Returned facts must pass schema-version, provenance, scope, expiry, privacy,
UTF-8, count and size validation before context assembly. Configured credentials
are redacted; facts remain untrusted data rather than tool permissions or system
instructions. Query errors/panics fail admission without relaying backend details.
Queries receive a three-second context allowance, but callbacks must cooperate
with cancellation. The store must obey local-only policy and avoid unauthorized
egress; trusted in-process extensions are not sandboxed.

This store is not serialized with submissions and does not change CLI/API memory
management storage. A daemon uses its own configured store. Automatic memory
creation, semantic retrieval and application-level lifecycle hooks remain open.

### Replaceable procedural skill storage

`ConfigOptions.SkillStore` accepts `skills.Store` (`sdk.SkillStore` is an alias).
Nil retains filesystem storage. Configure skills.enabled, root, scope, privacy
and size limits explicitly; injection does not enable retrieval. Root remains
required configuration but is not opened when a custom store is supplied.
The runtime only calls Discover and Load, never drafts, activates, rolls back
or closes this caller-owned store. CLI/API skill management remains filesystem
backed; injected callbacks are not serialized with queued submissions.

Discovery must return active deterministically validated versions. Admission
checks count, exact scope/domain, identifiers, UTF-8, duplicate keys, bounded
draft structure and SHA-256 agreement with discovery metadata. Versions are
pinned across discovery/loading; automatic routing freezes one context snapshot.
Missing tool permissions exclude a skill rather than granting capabilities.
Content is redacted and remains untrusted reference material.

The overall discovery/loading allowance is three seconds, cooperatively enforced.
Errors and panics deny admission without leaking backend details. Hosts must
provide concurrency safety, cancellation, honest activation validation and local
egress compliance: this is not a sandbox or a proof of workflow correctness.
Automatic drafting, regression detection and lifecycle hooks remain unfinished.

Filesystem stores also implement the optional `skills.RevisionStore` extension.
Read `ActivationState`, then use `ActivateAt` or `RollbackAt` for delayed decisions
that must reject intervening activations, including A→B→A. The full revision is
checked under the mutation lock after candidate validation. Draft-only changes
do not invalidate it. Legacy `Activate`/`Rollback` compare active versions only.
Revision checks do not grant permission or replace deterministic validation and
the automatic-mutation kill switch; the runtime still does not mutate skills.

A trusted host using the filesystem implementation can opt into deterministic
revalidation via `FileStore.RevalidateAndRollback(ctx, state, validator)`.
Enable `SetAutomatic(true)` under operator policy first. Failed deterministic
evidence rolls back one activation and is committed with that transition;
passing evidence leaves history unchanged. The returned State is the checked
observation, not the post-rollback revision. Re-read ActivationState before a
subsequent decision. Error/panic/timeout is not evidence of a skill regression.
Validators must cooperate with the three-second deadline and must not perform
unapproved side effects. This host-invoked controller does not install background
monitoring, automatic drafting or statistical regression detection.

`Request.Version` must be1; missing or incompatible versions reject before
execution. Request/result records do not expose internal admission or lease
fields. Public provider messages, runtime events and session compaction records
are shared with the core. Do not concurrently mutate request data or secret
callbacks while a call uses them.

`RunTextStream` accepts a synchronous callback for provisional, incrementally
redacted assistant text after the corresponding lifecycle marker commits. It
withholds possible secret fragments across chunks and can include intermediate
assistant turns, but never tool contents or delegated child streams. Only a
successful return establishes task completion. Callback errors/panics cancel
execution; callbacks must cooperate with cancellation. Token text is not
persisted or replayable. The returned result contains the final answer, not an
aggregate of all intermediate turns.

`RunStream` accepts a synchronous callback receiving committed, redacted events
in journal order. It is lifecycle streaming, not raw token streaming. A callback
error or panic cancels work and returns `ErrEventDelivery`; a committed event is
not treated as an uncommitted append. Callbacks must return promptly, must not
wait for their own task to finish, and should treat model/tool content as
untrusted. Persisted state can outlive delivery: inspect it before retrying a
failed call using `client.InspectTask(ctx, taskID)` or the CLI/API inspection
commands. SDK embedding is trusted-process access, not an authentication or
isolation boundary.

Task cancellation and steering use durable controls. Steering applies only at
safe model/tool boundaries and requires a caller-chosen idempotency key. A
completed task can be followed with `ContinueTaskID`; interrupted work is not
silently resumed. Feedback and prior-ID revisions use existing immutable
accounting; costs are explicit, subjective feedback cannot overwrite objective
failures, and identical retries do not add fitness samples.

## Durable daemon submissions

`Submit(ctx, key, Request{Version: 1, ...})` stores queued work and returns a
versioned `submissions.Status`; it does not dispatch inference or start a worker.
A separately running daemon must use the same database and matching effective
configuration to execute it. Submission acceptance is not proof that execution
admission or output validation will succeed. Reuse a16–128-character printable
ASCII idempotency key with the exact request after uncertain delivery. Conflicting
reuse rejects; the raw key is hashed before persistence. Configured credentials
in the request are rejected rather than silently changing the stored intent.

Use `SubmissionStatus(ctx, id)` to inspect durable lifecycle and completed output,
`ListSubmissions(ctx, submissions.ListOptions{Limit: 25})` for metadata-only
discovery, and `CancelSubmission(ctx, id)` for durable cancellation. Import the
public `github.com/ArronJablonowski/DarwinRouter/submissions` package for list
options and queue conflict/capacity error identities. Listing supports limits1–100
and opaque insertion-fenced cursors; changing task states are not frozen across
pages. Read methods never create or migrate missing storage. Cancellation of
running work remains cooperative and does not prove side effects have stopped.
No SDK method silently starts a dispatcher; use the daemon for background work.

`InspectTask` returns a versioned `TaskSnapshot` with messages, sequence, task
state, pending tool calls and uncertainty flags. It reads one coherent SQLite
snapshot, bounded to10,000 events and8MiB of serialized history, with a ten-second
maximum context allowance. Missing, corrupt or oversized history returns no
partial content. Inspection never creates a missing database, migrates it,
executes a model/tool, or assumes an interrupted task is safe to retry.
`InterruptedTurn` indicates an unfinished turn in the observed log; for an active
task this is not proof the worker stopped. Returned conversation/tool content can
be sensitive even after credential redaction. Mutating the returned snapshot does
not change stored history. Histories beyond the inspection bounds require paged
event inspection through `ReadEvents` or the HTTP API rather than unbounded reconstruction.

`ReadEvents(ctx, taskID, afterSequence, limit)` provides paged durable events
directly to embedded consumers. Start at sequence0, process a validated page, then
save its `NextSequence` for reconnection. Limits are1–100 events with an8MiB page
payload budget; the reader also validates the current head independently.
An individually oversized event fails with `sessions.ErrEventTooLarge`; a cursor
beyond the current head fails with `sessions.ErrEventCursor`. Missing/corrupt
storage returns a generic inspection error and no partial events.

Each page is transactionally consistent, but the head can advance between pages.
`HasMore == false` means caught up at that read, not that an active task ended.
Check `State`, and poll again only as needed using your own deadline/backoff.
This method never dispatches, resumes or cancels work. It is not a live
subscription or an exactly-once delivery guarantee: persist your cursor only
after processing, and deduplicate consumer side effects across retries. Event
content can include sensitive prompts/tool output; render and store it safely.

For a compilable program, see `examples/sdk/main.go`. The SDK integration test
builds a separate temporary Go module using only public imports and a local
provider fixture. This establishes external consumption, not production-provider
qualification. General context/evaluator engines, automatic skill learning,
resource-budget recommendations, extension hooks, a signed release and full PRD SDK contract coverage
remain unfinished. Existing low-level packages are not a substitute for those
future application-level extension contracts.
