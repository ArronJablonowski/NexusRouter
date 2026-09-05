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

`Request.Version` must be1; missing or incompatible versions reject before
execution. Request/result records do not expose internal admission or lease
fields. Public provider messages, runtime events and session compaction records
are shared with the core. Do not concurrently mutate request data or secret
callbacks while a call uses them.

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
qualification. Pluggable provider/tool/context/skill/evaluator engines,
resource-budget recommendations, extension hooks, a signed release and full PRD SDK contract coverage
remain unfinished. Existing low-level packages are not a substitute for those
future application-level extension contracts.
