# Embedded Go client

`Client.SkillLearningState(ctx)` inspects the configured learner's persisted
phase, scan cursor and pending generation identity without starting background
work. Inspection remains available when learning is disabled. The daemon owns
the opt-in background loop; see [background learning](../../docs/background-learning.md).

`Client.ModelDeprecation` provides read-only, versioned model recommendations
using `DeprecationRequest`, `DeprecationPolicy` and `DeprecationReport`. It validates
inputs/results and never runs inference or creates storage. See the
[shared CLI/API/SDK contract](../../docs/model-deprecation.md).

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
built-in workspace reader separately. Names `read_file`, `create_file`, `delegate` and
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

`Tool.Behavior` distinguishes `sdk.BehaviorReadOnly`,
`sdk.BehaviorIdempotentWrite`, and `sdk.BehaviorNonIdempotentWrite`. Omission
defaults from `ReadOnly`: true is read-only, false is non-idempotent write.
Explicit declarations must agree with that boolean; even an explicitly
read-only tool must set `ReadOnly: true`. Unknown or contradictory declarations
fail construction. For an idempotent implementation, set
`Behavior: sdk.BehaviorIdempotentWrite` and leave `ReadOnly` false.

Both writer classes still require the reviewer/presenter and durable writer
lease below. `ApprovalPrompt.Request.ToolBehavior`, approval inspection and
task tool events retain the normalized declaration. It is bound into the
one-use approval and cannot enable retry after confirmed or uncertain effects.
It describes trusted handler behavior, not submission deduplication or a proof
that arbitrary Go code is idempotent. See [declared tool behavior](../../docs/tool-behavior.md).

SDK execution installs shared reader leases for allowed read-only tools and
exclusive leases for approved writes. Assign the same trusted `Scope` to any
overlapping resources; use `workspace` for filesystem extensions that can
overlap built-in file tools. Busy scopes reject rather than silently retrying.
Cancellation/expiry does not release a still-running callback. Hosts must join
all handler-owned work before returning; this is not an OS sandbox. See
[reader/writer execution](../../docs/reader-writer-execution.md), including
same-database coordination, legacy scope compatibility and crash limits.

Trusted handlers can return `runtime.ToolResult{Failed: true, Effect: ...}` with
a nil Go error for an explicitly known tool failure. The runtime records
`tool_failed` and, by default, fails the task while preserving the declared effect.
Only `Failed: true, Recoverable: true, Effect: runtime.NoEffect` with a nil Go
error permits a fresh model turn within existing budgets. This is not automatic
tool replay: each new proposal passes the same permissions and approvals. The
paired provider message carries `ToolFailed: true`, including after replay.
`NoEffect` alone is not a success/failure flag. A Go error or panic during a side-effecting
handler still means uncertain execution; do not downgrade an unknown effect by
declaring a certain result. Failed tool steps cannot seed successful workflow
grouping merely through separate positive task feedback.
See [recoverable tool failures](../../docs/recoverable-tool-failures.md).

The same reviewer/presenter controls support the opt-in built-in `create_file`
tool when configuration enables `tools.create_enabled` with an absolute
`tools.create_root` and the existing local file-tool settings. No custom handler
is needed. It creates new files only, never overwrites, and is not inherited by
workers or accepted for durable submission execution. See
[reviewed file creation](../../docs/reviewed-file-creation.md) for bounds and
filesystem trust requirements. Enabling creation without a review control is
rejected by SDK construction.

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

`ScopeLeases`, when non-nil, adds a versioned overlapping-scope observation:
`LiveReaders`, `ExpiredReaders`, `LiveWriters` and `ExpiredWriters`. Version 1
and overlap-policy version 1 cover at most 1,000 unreleased holders, using exact
generic scopes and the shared `workspace`/`create_*` filesystem family. Invalid
or overflowing observations fail without a partial status. A nil pointer is
legacy/unavailable data, not zero holders. `ScopeWriterState` remains exact;
it may be `none` while aliases have writers. Counts share the approval/journal
snapshot, expose no tokens/owners/holder task IDs, and never grant dispatch,
retry or release authority. `Live` describes unexpired leases, not proven
process health. Readers remain blockers for writers even when expired.

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

### Replaceable context planning

For assembly/retention strategy, `ConfigOptions.ContextEngine` accepts the public
`contextengine.Engine` contract (`sdk.ContextEngine` is an alias). It drives
real task assembly, per-turn estimation and compaction selection. It is mutually
exclusive with `ContextEstimator`; nil keeps the defaults. See
[context planning](../../docs/context-engine.md) for isolation, bounds, privacy,
frozen summary reviews and an implementation example.

### Replaceable factual memory storage

`ConfigOptions.MemoryStore` accepts the public `memory.Store` contract (`sdk.MemoryStore`
is an alias). Nil retains SQLite. Injection does not enable retrieval: configure
`memory.enabled`, scope, privacy and context-size limits explicitly. Runtime use
queries facts and optionally updates selected-fact last-use through `MemoryUseStore`.
Creation, correction, deletion and expiry remain explicit operations. The caller
owns that store's lifetime and concurrency.

Returned facts must pass schema-version, provenance, scope, expiry, privacy,
UTF-8, count and size validation before context assembly. Configured credentials
are redacted; facts remain untrusted data rather than tool permissions or system
instructions. Query errors/panics fail admission without relaying backend details.
Queries receive a three-second context allowance, but callbacks must cooperate
with cancellation. The store must obey local-only policy and avoid unauthorized
egress; trusted in-process extensions are not sandboxed.

This store is not serialized with submissions. `Client.Memory`, `Memories`,
`PutMemory` and `DeleteMemory` use it for configured-scope operator management;
these operations remain available with retrieval disabled and do not invoke
models. See [memory management](../../docs/memory-management.md) for revision
conflicts, redaction and uncertain-write handling. A daemon uses its own service's
store, and the legacy direct-database CLI is unaffected. Automatic memory
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
Background drafting, statistical regression detection and lifecycle hooks remain unfinished.

Filesystem stores also implement the optional `skills.RevisionStore` extension.
Read `ActivationState`, then use `ActivateAt` or `RollbackAt` for delayed decisions
that must reject intervening activations, including A→B→A. The full revision is
checked under the mutation lock after candidate validation. Draft-only changes
do not invalidate it. Legacy `Activate`/`Rollback` compare active versions only.
Revision checks do not grant permission or replace deterministic validation and
the automatic-mutation kill switch; ordinary task execution does not mutate skills.

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

For host-driven automatic drafting, filesystem stores provide
`DraftFromWorkflows(ctx, key, examples, generator)` with `skills.DraftGenerator`
or `DraftGeneratorFunc`. Supply 2–20 distinct successful task/session examples
in one domain, including trusted evaluation checks and redacted workflow steps.
LLM-only evidence is ineligible; accepted user feedback can support subjective
workflows without outranking objective failures. The generator receives owned
copies, and cannot invent the stored SourceSessions/SourceEvidence provenance.
The 30-second deadline is cooperative. Scope, automatic-change policy, output
shape and size are checked before storing an inactive version. The host owns
model routing, privacy and generation costs. The runtime does not yet discover
examples or schedule this operation automatically; activation is separate.

Use `skills.ModelGenerator` as a built-in `DraftGenerator` when a provider has
already passed host policy and resource admission. Configure Provider, Model,
ContextTokens, Timeout (at most30seconds), EstimatedCost and MaxCost; optional
ContextEstimator can only increase the conservative context floor. One tools-free
call must complete with stop and produce the exact workflow schema within64KiB.
`GenerateDetailed` returns the Draft, Model, optional cloned Usage and total
Elapsed time; `Generate` adapts it to the draft-only interface. Unknown usage is
nil, not zero. Estimated cost is not billing evidence. The host remains responsible
for privacy/redaction, resource reservation and durable attempt/accounting records.
No daemon route selection, retry or activation occurs in this component.

`ModelGenerator.GenerateRecorded(ctx, recorder, id, providerID, key, examples)`
wraps generation in the `skills.GenerationRecorder` contract. Begin must be a
single-winner durable claim, including for identical requests. SQLite schema16
provides that implementation internally. Finish stores the generated proposal
before returning it; cancellation uses an independent bounded cleanup write.
On terminal persistence failure, inspect the returned started attempt's ID.
Do not reissue inference under a new ID to bypass uncertainty. The input digest
binds admitted examples and generation parameters; the host must additionally
pin policy and estimator identity. This records proposals, not skill publication,
and is not exposed through the native management API.

`Client.GenerateSkillDraft(ctx, attemptID, modelID, key, taskIDs, maxCost)` connects
the application workflow to verified history. Supply a stable attempt ID, a
configured model ID and2–20 completed task IDs from distinct sessions in one
domain. Enable skills.enabled and skills.auto_draft with a configured root/scope;
the root is not opened or changed by generation. Current accepted final-attempt
evidence is selected from storage, including explicit subjective corrections.
Local-only source/skill policy cannot be offloaded to a cloud generator. Admission
uses shared execution/resource capacity and the configured context estimator.
Configured secrets are redacted from observations and generated drafts before
dispatch/persistence; exports can still contain other sensitive task content.
No automatic selection of source tasks, publication, activation or background
scheduling occurs. This does not mutate an injected SkillStore.

`Client.ListSkillGenerations(ctx, scope, after, limit)` returns metadata-only
summaries without generated content or source identifiers; limit is1–100 and
after is an exclusive lexical ID cursor. Pages are live observations, not a
frozen snapshot. `Client.InspectSkillGeneration(ctx, scope, id)` explicitly reads
the full saved attempt, including any proposal. Both reject scope mismatches and
never initialize/migrate storage or execute work. Protect full-record exports as
sensitive data. HasResult means a saved proposal, not a published/active skill;
started means uncertainty, never automatic retry permission.

`Client.PublishSkillGeneration(ctx, attemptID)` reads a saved drafted attempt in
the configured scope and publishes an inactive immutable file-store version.
It requires enabled skills and auto-draft, rejects injected SkillStore instances,
and rejects current credential collisions without rewriting the proposal.
Identical retries return the same version; conflicting attempt reuse rejects.
Publication does not revalidate source feedback, validate the workflow or activate
it. File catalogs upgrade to schema 2 on first publication; older binaries reject
that schema. Receipts and version visibility commit together, but pre-commit
failures may leave unreferenced files. There is no automatic cleanup.

`Client.SkillActivationState(ctx, key)` reads the configured file store's current
activation revision without creating it. `Client.ActivateSkillVersion(ctx,
expected, versionID, validator)` validates an existing immutable version and
activates it only if the observed revision remains current. Enable
`skills.auto_activate_after_validation`; the configured scope must match and
injected retrieval stores cannot be mutated. Read state again after success to
obtain the new revision. A stale observation rejects rather than silently
replacing another activation.

Supply a trusted `skills.Validator` that checks the actual candidate and returns
attributable deterministic evidence. Serialization validity or an LLM's opinion
is not sufficient. Validators are host Go code, not sandboxed tools: they must be
read-only, safe to invoke again, honor cancellation, and must not bypass tool or
privacy policy. Core activation uses a cooperative three-second deadline and
sanitizes callback failures; it cannot forcibly stop an uncooperative callback.
Current credential collisions in the candidate or proof identifier reject, not
silently rewrite evidence. There is no remote endpoint accepting self-declared
validation proof and no background activation scheduler.

`Client.RevalidateSkillVersion(ctx, expected, validator)` checks the currently
active version and rolls back to its validated predecessor on attributable
deterministic failure. Enable `skills.rollback_on_regression`; disabling new
activations does not disable this recovery control. Passing checks leave the
catalog unchanged. Validator errors, panic, cancellation or judge-only opinions
are not regression evidence and do not trigger rollback.

The returned `RegressionResult.State` is the checked observation, not the new
post-rollback state. Read `SkillActivationState` again before another decision.
Failure proof and the rollback transition commit together; stale revisions reject
without invoking a validator when detected at admission. The same scoped store,
credential, trusted-callback and cooperative-deadline rules apply as activation.
An initial version has no predecessor to restore. This method does not schedule
continuous checks or infer statistical regressions from task outcomes.

`Client.DiscoverSkillWorkflows(ctx, domain, after, scanLimit)` discovers accepted
source candidates from durable tasks. It requires enabled skill drafting and a
configured root/scope, but reads only the existing telemetry database. Limits are
1–20 scanned task heads, not 1–20 matches. Follow `Next` even for zero-candidate
pages; a full final page may be followed by an empty page. Metadata preserves
privacy and source/evaluation provenance without conversation text. Credential
collisions reject the page rather than rewriting identities or cursors.

Candidates qualify through current completed replay and accepted deterministic,
tool-result or user evidence, never judge-only ratings. Pages are coherent local
observations but do not freeze future feedback. Distinct source sessions and a
common domain remain generation requirements; use `GenerateSkillDraft` to
recheck the chosen IDs. Discovery is not semantic workflow clustering, an
automatic drafting scheduler or permission to publish/activate.

`skills.NewWorkflowSelection` constructs a bounded, content-addressed selection
from 2–20 same-domain candidates in distinct sessions, plus a destination key,
group/algorithm identifiers, configured model ID and policy digest. It owns and
sorts source metadata. `Validate` recomputes the versioned ID, including privacy
and source/evaluation bindings but excluding creation time. These are trusted
host inputs, not semantic grouping proof or policy authority.

`Client.PlanWorkflowSelection(ctx, modelID, key, group, algorithm, taskIDs,
maxCost)` verifies explicitly chosen sources and persists their identity plus
effective generation-policy binding. Planning performs no inference. SQLite
schema 17 preserves the first saved creation time when identical inputs are
planned again, including after a client restart.

`Client.GenerateSkillSelection(ctx, selectionID, maxCost)` rechecks saved source
and policy bindings before using the selection ID as a single-winner generation
attempt ID. Changed evidence or policy rejects instead of silently selecting new
inputs. Repeating a dispatched selection never authorizes another inference;
inspect the saved attempt after uncertain completion. Success remains an inactive
proposal; publication and deterministic activation are separate operations.

Policy binding currently conservatively includes the entire validated configuration
and explicit cost ceiling. Even an unrelated configuration change requires a new
plan. Credentials are excluded; injected provider/estimator implementations have
no durable identity and remain trusted host code. Source verification is against
the coherent snapshot used for generation, not a promise that feedback cannot
change during inference.

The caller chooses tasks and owns the grouping rule. These methods do not perform
semantic clustering, establish source tenancy, or start a background scheduler.
Planning alone is not a generation claim, and using the lower-level draft method
with a fresh attempt ID does not inherit selection-based deduplication.

`Client.GroupSkillWorkflows(ctx, taskIDs)` reads accepted task observations and
groups identical ordered tool names, domain and execution profile. It uses actual
successful paired dispatch events, not imported tool messages, arguments or
output text. No-tool observations and groups with fewer than two distinct
sessions are omitted. Group IDs describe the observed pattern independently of
which tasks are members; they are not semantic equivalence or permission to run
those tools.

`Client.PlanGroupedWorkflowSelection(ctx, modelID, key, taskIDs, maxCost)` requires
one group covering every requested source, then persists a bound selection with
no inference. The `observed_tools_v1` algorithm is reserved: callers cannot assert
it through `PlanWorkflowSelection`. Actual traces are revalidated in the same
source transaction during grouped planning and selection-based generation.
Generation still creates only an inactive proposal, with the stable selection
ID preventing automatic redispatch. These operations do not schedule work or
establish source tenancy.

`Client.AdvanceSkillWorkflowScan(ctx, name, domain, expectedRevision, scanLimit)`
saves a bounded discovery page and progress together in the configured scope.
Start at revision zero; use the returned scan revision for the next page. Enable
skills and auto-draft with a configured root/scope and an existing current-schema
database. The call does not initialize or migrate storage, open the skill root,
or invoke a model. Sensitive metadata is checked before persistence; identities
are rejected rather than rewritten when they collide with current credentials.

An epoch fences task membership by insertion sequence while reading live task
and feedback state. Later epochs revisit changed evidence and lower-ID arrivals.
`Client.SkillWorkflowScan(ctx, name)` reads current progress without advancing;
it requires enabled skills but remains available when auto-draft is disabled.
After uncertain delivery, retry the same name/domain/expected revision/limit:
the write may already have committed, and an exact retry returns its original
historical page even when newer pages exist. Metadata can still be sensitive.
These methods do not schedule scans, group candidates across pages, select a
workflow, or dispatch generation.

`Client.ConsumeSkillWorkflowScan(ctx, name, expectedRevision)` now performs the
separate grouping stage for the next saved scan page. Start consumption at zero
even if discovery has already saved several pages. After success, use the
returned receipt revision; after uncertain delivery, retry the same expected
revision. An exact retry returns its original historical receipt without
reapplying the page. Consumption requires enabled auto-draft and existing
current-schema storage, and does not invoke a provider or open the skill root.

The consumer refreshes acceptance and actual paired tool events transactionally.
Changed or no-longer-eligible examples are skipped; malformed evidence fails the
whole page. Matching domain/profile/ordered-tool observations accumulate across
pages within one epoch, keeping the lexical first task per session and at most
20 distinct sessions. Each epoch is limited to 1000 patterns; exceeding the cap
fails closed. A singleton is retained but cannot yet become a workflow group.

`Client.SkillWorkflowScanConsumption(ctx, name)` inspects the latest receipt.
`Client.SkillWorkflowScanBuckets(ctx, name, epoch, afterID, limit)` lists 1–20
buckets ordered by ID, including singletons. Advance `afterID` to the final ID
returned; an empty page ends inspection. Inspection works with auto-draft off.
Call `bucket.Group()` to require at least two sources, then pass its source task
IDs through `PlanGroupedWorkflowSelection` before `GenerateSkillSelection`.
Planning and generation revalidate evidence; a saved bucket is not fresh
acceptance, semantic equivalence, or permission to execute tools.

Discovery and consumption use independent revision cursors. Scope/name identify
one operator's scan, not source-data tenancy. Receipt/catalog integrity checks
run within a ten-second operation deadline; indexed long-history accounting and
retention remain future work. These operations do not start a background
scheduler, dispatch generation, publish drafts, or activate skills.

`skills.generation_budget` optionally caps generation across the configured skill
scope. Defaults are disabled, with `window: 24h`, `max_cost: 0`,
`max_attempts: 10`, `max_in_flight: 1`, and `cooldown: 1h`. When enabled, all
application generation routes share the same durable claim budget. The rolling
window counts estimated cost and every attempt, including failures; outstanding
started attempts count toward concurrency regardless of age. Cooldown applies
per skill name. A zero cost ceiling still permits zero-estimated-cost models,
subject to attempt and concurrency limits.

This is conservative admission accounting, not actual provider billing or a
background scheduler. Limits must be valid even while disabled: window 1 minute
through 30 days, attempts 1–1000, in-flight 1–attempt limit, finite nonnegative
cost, and cooldown zero through the window. History inspection is currently
bounded to 1000 records/8 MiB and fails closed beyond that bound; indexed budget
history and retention remain future work.

Policy is trusted client configuration, not an immutable database-wide limit:
another host-authorized client can disable or loosen it. Enabled clients still
count all recorded scope attempts, including earlier unbudgeted work. Cost
ceilings use the same units as configured model estimates. Budget denial returns
`skills.ErrGenerationBudget` without a generation claim or inference call.

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

`RunLiveStream(ctx, request, emitEvent, emitText)` combines both delivery paths.
Both callbacks must be nonnil. Events precede text for the same committed marker;
an error or panic in either callback stops both and cancels execution. Returning
success from a callback does not itself accept the task. Cancellation during
the terminal event callback returns cancellation even when durable completion
already exists and trailing text was withheld. Event payloads retain their
existing inspection content; only the text channel excludes tool/child output.
Rendering, line prefixes and display limits belong to the caller, not the SDK.
See [interactive streaming](../../docs/interactive-streaming.md).

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

`InspectTaskContinuation(ctx, taskID)` returns a versioned `ContinuationStatus`
without conversation content. Its lowercase JSON fields are `version`, `task_id`,
`sequence`, `state`, `history_eligible`, and `reason`. It reads replay state and
the final recovery checkpoint in one bounded read-only transaction. Reasons
`completed` and `recovered_delegation` mean only that the observed history can be
used as source context for a new explicit request. Other reasons include
`pending_tools`, `uncertain_effects`, `interrupted_turn`, `task_running`,
`task_failed`, and `task_canceled`. If several hazards coexist, pending tools take
precedence over uncertain effects, then interrupted turns. Missing, corrupt or
oversized history returns an error rather than partial metadata.

This is not model readiness or execution authorization. Normal provider,
privacy, resource, budget and tool policy checks still apply; no method silently
replays old tool calls. Codex CLI now supports explicit `Request.ContinueTaskID`
through typed history import into a new ephemeral thread, subject to its 1 MiB
injection-frame limit. Compaction/summary requests and in-flight steering remain
unsupported for that provider; see [the continuation guide](../../docs/codex-history-continuation.md).

`InspectTaskLeases(ctx, taskID)` returns `TaskLeaseStatus`: task/head metadata,
live/expired/released reader/writer counts, and validated recovery counts. The
SDK aliases `TaskLeaseCounts` and `TaskRecoveryCounts`. Feature groups are nil
when unsupported by an older database, not zero. Counts concern leases owned by
this task, not all conflicting scopes or descendants. Interrupted-child recovery
is counted on the worker lease's receipt. Inspection never probes guard files,
reclaims ownership, retries work or returns prompts, tokens, owner/scope names or
receipt bodies. See [task lease inspection](../../docs/task-lease-inspection.md).

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
