# DarwinRouter

A Go-based, local-first agent runtime with adaptive model routing. The product specification is in [DarwinRouter_PRD.md](DarwinRouter_PRD.md).

## Development status

The executable supports layered configuration, explicit-model headless tasks, and an authenticated loopback HTTP service with durable SQLite/WAL history. Provider calls use an allowlisted transport, with loopback-only enforcement for local models. Automatic routing, tool execution and interactive streaming are not yet connected to user-facing execution. See the implementation evidence for remaining work; this is not a released MVP.

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

The temporary local module path is `darwinrouter`. Replace it with the actual hosting path when publishing the Go SDK; no GitHub owner or repository has been assumed.

## Configuration

```sh
./bin/darwin config validate --config examples/local.yaml
./bin/darwin config show --config examples/local.yaml --set workers.max_in_process=1
DARWIN__MODE=local_only ./bin/darwin config validate
```

Precedence: defaults → OS user config directory `/darwinrouter/config.yaml` → working-directory `config.yaml` → `DARWIN__SECTION__FIELD` environment variables → repeated `--set section.field=value` flags. `--user-config` and `--config` select explicit files; missing explicit paths are errors. Nested mappings merge; arrays replace wholesale. Environment and CLI overrides address scalar settings only. Unknown fields, duplicate keys, aliases, nulls, and multi-document YAML are rejected. Configuration files are limited to 1 MiB.

Provider keys are referenced by `api_key_env`; the loader never resolves credential values. Configuration text is literal (shell `${...}` expansion is not performed). Set concrete endpoint/database values in files or override scalar settings through the environment. The display redacts endpoints and database paths. Local-only configuration validation is not network enforcement; that guarantee requires the later transport-policy sprint.

## Run an explicit-model task

Replace `local-model-id` in `examples/local.yaml` with an installed Ollama model, then run:

```sh
./bin/darwin run --config examples/local.yaml --model local-fast < prompt.txt
./bin/darwin task show --db ./data/darwin.db --task TASK_ID
./bin/darwin resources
./bin/darwin run --config examples/local.yaml --model local-fast --continue-task TASK_ID < followup.txt
```

The prompt is read from stdin (maximum 1 MiB). The completed answer goes to stdout; the durable task ID goes to stderr. This initial command requires an explicit project config, optionally accepts `--user-config` and repeated `--set` scalar overrides, and uses environment overrides. Unlike `config`, it does not discover user/project configuration paths yet. The task has a five-minute timeout and one model turn; tool proposals are not executed. Known configured provider keys are redacted from persisted content and the returned answer. Partial token text is not persisted. Other sensitive-content redaction policies remain unfinished. No paid/live-provider qualification has been performed.

`task show` opens an existing database read-only and prints reconstructed conversation state as JSON, including pending tools and uncertain outcomes. It never creates a database or resumes work. Its output includes session content; treat exports as sensitive. `resources` reports host measurements with unavailable sensors represented as null.

`--continue-task` starts a new task from a completed task's saved conversation in the same database and session. The source remains immutable, and the new task records its parent. Missing, unfinished or uncertain-effect histories are rejected. Histories created on local models (and legacy histories without a privacy marker) cannot be continued on cloud models. This is completed-session continuation, not interrupted-task recovery. Combined history is limited to 4 MiB pending token-budget/compaction integration.

## Local HTTP service

Set `DARWIN_API_TOKEN` to a securely generated secret of at least 32 characters, then run `darwin serve --config examples/local.yaml`. The configured daemon address must be loopback. This foreground process stops on SIGINT/SIGTERM and cancels active requests during shutdown. It is not yet an installed operating-system service.

All endpoints require `Authorization: Bearer <token>`:

- `GET /health`: application/database health; provider health is explicitly not checked yet.
- `POST /v1/tasks`: JSON `{"model_id":"local-fast","prompt":"Hello"}` with optional `continue_task_id`. This initial endpoint waits for durable completion before returning HTTP 201 with `task_id`, `text`, and `turns`.
- `GET /v1/tasks/{id}`: reconstructed task/session state.

Requests are bounded by configured worker concurrency, a 1 MiB JSON body limit, and a five-minute execution deadline. Duplicate and unknown JSON fields, browser-origin requests, and unauthenticated requests are rejected. API token text is included in application credential redaction. Async submission, idempotency keys, SSE, separate cancellation, OpenAI-compatible endpoints, full provider health, and service installation are unfinished. Do not automatically retry a timed-out submission; a durable task may already exist.

## Next sprints

1. Connect provider calls to the durable runtime and cancellation lifecycle.
2. Implement schema-validated tool registration and scoped permissions.
3. Enforce transport policy before exposing live inference through the CLI.
4. Expand cross-provider and crash-recovery qualification.

See [implementation evidence](docs/progress.md) for completed local work and remaining checks by Linear issue.

See the [Linear project](https://linear.app/darwinrouter/project/darwinrouter-mvp-fc9fe6d48fda) for the full delivery backlog.
