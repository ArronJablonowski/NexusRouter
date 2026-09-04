# DarwinRouter

A Go-based, local-first agent runtime with adaptive model routing. The product specification is in [DarwinRouter_PRD.md](DarwinRouter_PRD.md).

## Development status

The executable supports layered configuration and explicit-model headless tasks with durable SQLite/WAL history. Provider calls use an allowlisted transport, with loopback-only enforcement for local models. Automatic routing, tools, interactive streaming and the daemon are not yet connected to the CLI. See the implementation evidence for remaining work; this is not a released MVP.

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
```

The prompt is read from stdin (maximum 1 MiB). The completed answer goes to stdout; the durable task ID goes to stderr. This initial command requires an explicit project config, optionally accepts `--user-config` and repeated `--set` scalar overrides, and uses environment overrides. Unlike `config`, it does not discover user/project configuration paths yet. The task has a five-minute timeout and one model turn; tool proposals are not executed. Known configured provider keys are redacted from persisted content and the returned answer. Partial token text is not persisted. Other sensitive-content redaction policies remain unfinished. No paid/live-provider qualification has been performed.

## Next sprints

1. Connect provider calls to the durable runtime and cancellation lifecycle.
2. Implement schema-validated tool registration and scoped permissions.
3. Enforce transport policy before exposing live inference through the CLI.
4. Expand cross-provider and crash-recovery qualification.

See [implementation evidence](docs/progress.md) for completed local work and remaining checks by Linear issue.

See the [Linear project](https://linear.app/darwinrouter/project/darwinrouter-mvp-fc9fe6d48fda) for the full delivery backlog.
