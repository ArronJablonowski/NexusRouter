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
failed call using the CLI/API inspection commands (the client does not yet expose
session replay). SDK embedding is trusted-process access, not an authentication or
isolation boundary.

Task cancellation and steering use durable controls. Steering applies only at
safe model/tool boundaries and requires a caller-chosen idempotency key. A
completed task can be followed with `ContinueTaskID`; interrupted work is not
silently resumed. Feedback and prior-ID revisions use existing immutable
accounting; costs are explicit, subjective feedback cannot overwrite objective
failures, and identical retries do not add fitness samples.

For a compilable program, see `examples/sdk/main.go`. The SDK integration test
builds a separate temporary Go module using only public imports and a local
provider fixture. This establishes external consumption, not production-provider
qualification. Pluggable provider/tool/context/memory/skill/evaluator/resource
engines, extension hooks, a signed release and full PRD SDK contract coverage
remain unfinished. Existing low-level packages are not a substitute for those
future application-level extension contracts.
