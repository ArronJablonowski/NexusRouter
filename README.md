# NexusRouter

A local-first agent runtime that routes tasks to models and external agent harnesses, learns from evaluated results, and supports secure execution across trusted systems.

Built in Go, with a CLI, an authenticated Web UI, an HTTP API, and an embeddable Go SDK.

[Quick start](#quick-start) · [Documentation](#documentation) · [Development](#development) · [Project status](#project-status)

## What it does

- **Adaptive routing:** Select eligible models using task requirements, resource limits, privacy policy, and recorded quality evidence.
- **External harnesses:** Integrate Hermes Agent, OpenClaw, Pi Agent, Goose, and OpenHands. Model and harness versions are tracked together so feedback applies to the combination that produced the result.
- **Secure remote execution:** Dispatch, inspect, and cancel tasks on trusted NexusRouter instances through HTTPS or SSH, with explicit pairing and scoped authorization.
- **Web UI and Kanban:** Chat, inspect models and routing decisions, manage durable work, and review task evidence in an authenticated browser interface.
- **Persistent history and learning:** Retain task events, feedback, scoped memory, and versioned skills in durable local storage.
- **Controlled tools:** Apply explicit filesystem scope, network policy, resource admission, and approval requirements to supported operations.

Routing learns from evaluated outcomes; a completed task alone is not evidence of correctness. Available integrations and qualification limits are documented in the guides below.

## Quick start

### 1. Build from source

You need **Go 1.27.1** and **Make**. Ordinary builds use pure-Go SQLite and do not require a C compiler.

```sh
git clone https://github.com/ArronJablonowski/NexusRouter.git
cd NexusRouter
make build
./bin/nexus help
```

### 2. Configure a local model

Start Ollama with a model installed, then use [examples/local.yaml](examples/local.yaml) as your starting configuration. Before running a task, update its model entry:

| Setting | What to supply |
| --- | --- |
| `model` | The name of an installed Ollama model. |
| `context_tokens` | A conservative supported input-token limit. |
| `ram_bytes` | A conservative memory estimate covering weights, maximum context/KV memory, and runtime overhead. |
| `estimated_cost` | Your per-attempt cost estimate; zero only when appropriate for local execution. |

The example intentionally sets context and RAM estimates to zero. Execution is denied until valid estimates are supplied. On Apple unified-memory systems, include GPU allocations in `ram_bytes` and leave `vram_bytes` at zero.

```sh
./bin/nexus config validate --config examples/local.yaml
./bin/nexus models list --config examples/local.yaml
```

Configuration validation checks the settings; it does not prove that the model is ready or fits in available memory.

### 3. Run a task or start a chat

```sh
printf '%s\n' 'Explain how a work queue works in three sentences.' | \
  ./bin/nexus run --config examples/local.yaml --model local-fast

./bin/nexus chat --config examples/local.yaml --model auto
```

`local-fast` is the configured model alias in the example. `auto` selects from eligible configured models.

### 4. Open the Web UI

Set `NEXUS_API_TOKEN` to a securely generated secret of at least 32 characters, then start the service:

```sh
./bin/nexus serve --config examples/local.yaml
```

With the example configuration, open **http://127.0.0.1:7788/app** and authenticate. The stock daemon listens on loopback. For remote execution, configure the separate trusted remote-routing service using the [secure remote routing guide](docs/secure-remote-routing.md).

## Documentation

| Start here | Guide |
| --- | --- |
| Browser chat, boards, settings, and operations | [Web UI and Workboard operator guide](docs/workboard-operator-guide.md) |
| Model and harness selection, feedback, and adapters | [External harness routing](docs/external-harness-routing.md) |
| Pairing systems and using HTTPS or SSH | [Secure remote routing](docs/secure-remote-routing.md) |
| Embed NexusRouter in a Go application | [Go SDK](sdk/v1/README.md) · [Compilable example](examples/sdk/main.go) |
| Detailed CLI commands and configuration behavior | [Runtime reference](docs/runtime-reference.md) |
| Upgrade an existing DarwinRouter installation | [Name migration guide](docs/nexusrouter-migration.md) |
| Understand the design and requirements | [Architecture](docs/architecture.md) · [Product specification](NexusRouter_PRD.md) |
| Verify behavior and review remaining work | [MVP qualification](docs/mvp-qualification.md) · [Implementation evidence](docs/progress.md) |
| Prepare and qualify a release | [Release packaging](docs/release-packaging.md) · [Release checklist](docs/release-checklist.md) |

## Development

```sh
make check
```

This checks formatting, enforces the 1,000-line limit on handwritten Go files, runs `go vet` and race tests, and builds every package. Race tests require a supported platform toolchain, and the full suite can take substantial time. See [test-suite timing](docs/test-suite-timing.md).

Use `make fmt` to apply Go formatting and `make build` to build the CLI. Follow [AGENTS.md](AGENTS.md) for the project workflow.

| Directory | Purpose |
| --- | --- |
| `cmd/nexus`, `internal/cli` | Executable and command-line interface. |
| `routing`, `harness`, `remote` | Model selection, harness integrations, and remote execution. |
| `providers`, `tools`, `policy` | Provider adapters, tool contracts, and network policy. |
| `memory`, `skills`, `evaluation` | Scoped knowledge, workflows, and outcome evidence. |
| `resources`, `internal/telemetry` | Resource admission and durable runtime history. |
| `sdk/v1`, `examples`, `docs` | Public SDK, examples, and documentation. |

## Project status

NexusRouter is under active development. The repository includes working routing, harness, remote-execution, and Web UI implementations, but development test results do not establish production release readiness.

The v1.0.1 release qualification remains in progress, including native-platform evidence, dependency-notice review, production signing, independent verification, and publication approval. General patch editing, delegated writes, and unattended write approvals also remain unfinished.

See [implementation evidence](docs/progress.md) for specific completed work and remaining limitations, and the [Linear project](https://linear.app/nexusrouter/project/nexusrouter-mvp-fc9fe6d48fda) for the delivery backlog.

## License

[MIT](LICENSE). Release artifacts also carry dependency notices; see [release packaging](docs/release-packaging.md) for how those notices are assembled and reviewed.
