# NexusRouter

A local-first agent runtime that routes tasks to models and external agent harnesses, learns from evaluated results, and supports secure execution across trusted systems.

Built in Go, with a CLI, an authenticated Web UI, an HTTP API, and an embeddable Go SDK.

[Quick start](#quick-start) · [Documentation](#documentation) · [Development](#development) · [Project status](#project-status)

## What it does

- **Accuracy-first automatic routing:** Rank eligible models by evidence-supported task accuracy. Lower cost or latency cannot outweigh better accuracy; explicit model selections remain pinned. Task requirements, resource limits, privacy, and authorization still constrain eligibility.
- **External harnesses:** Integrate Hermes Agent, OpenClaw, Pi Agent, Goose, and OpenHands. Model and harness versions are tracked together so feedback applies to the combination that produced the result.
- **Secure remote execution:** Dispatch, inspect, and cancel tasks on trusted NexusRouter instances through HTTPS or SSH, with explicit pairing and scoped authorization.
- **Web UI and Kanban:** Chat, rename or pin chats from their three-dot menus, inspect models and routing decisions, manage durable work, and review task evidence in an authenticated browser interface. Saved names and pins survive refresh and daemon restart.
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

`local-fast` is the configured model alias in the example. `auto` selects from eligible configured models using recorded task-quality evidence, adjusted for confidence and recency. Unknown models retain a neutral prior; model names and sizes do not establish accuracy. Ordinary requests do not explore weaker alternatives. An explicit model choice, including a configured commander default, remains pinned under the existing explicit fallback rules.

Configured-provider routing and paired-remote automatic dispatch currently use separate candidate pools. An ordinary chat does not yet compare every model on every paired host. See [accuracy-first routing](docs/accuracy-first-routing.md) for the implemented behavior and remaining integration work.

### 4. Open the Web UI

Set `NEXUS_API_TOKEN` to a securely generated secret of at least 32 characters, then start the service:

```sh
./bin/nexus serve --config examples/local.yaml
```

With the example configuration, open **http://127.0.0.1:7788/app**. Approve the browser's one-time challenge in a trusted terminal on the same host, with the daemon's `NEXUS_API_TOKEN` available in that terminal environment:

```sh
./bin/nexus web approve --config examples/local.yaml CHALLENGE_ID.DISPLAY_CODE
```

Replace `CHALLENGE_ID.DISPLAY_CODE` with the complete value shown in the browser. The API token stays in the trusted terminal/service environment and must never be pasted into the browser. Browser sessions expire and require a new approval after daemon restart.

For an installed macOS service, `nexus web approve CHALLENGE_ID.DISPLAY_CODE` discovers the running user's service configuration and credential. If multiple services match, select one with `--service com.nexusrouter.commander` or provide its actual `--config` path. On Linux and other platforms, use an explicit configuration and token environment; automatic service credential retrieval is currently macOS-only. See [browser authorization](docs/workboard-operator-guide.md#authorize-a-browser) for custom installations.

Use the three-dot menu beside a chat title to **Rename**, **Pin**, or **Unpin** it. Pinned chats stay above unpinned chats, with newest-first ordering within each group. Preferences are shared by authenticated browsers on the same daemon. Include `<telemetry.database>.chat-preferences.json` alongside the task database in backups; see [chat preferences](docs/workboard-operator-guide.md#rename-and-pin-chats).

The stock daemon listens on loopback. For remote execution, configure the separate trusted remote-routing service using the [secure remote routing guide](docs/secure-remote-routing.md).

## Documentation

| Start here | Guide |
| --- | --- |
| Browser chat, boards, settings, and operations | [Web UI and Workboard operator guide](docs/workboard-operator-guide.md) |
| Accuracy-first selection and current cross-host limits | [Automatic routing](docs/accuracy-first-routing.md) |
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

For routine changes, use the change-scoped validation in [AGENTS.md](AGENTS.md); a full `make check` is required for major releases, broad refactors, uncertain cross-system impact, or an explicit request for full validation. Documentation-only changes need diff review and `git diff --check`. Targeted checks do not establish a full-suite pass.

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

The v1.0.1 release qualification remains in progress, including native-platform evidence, dependency-notice review, production signing, independent verification, and publication approval. General patch editing, delegated writes, unattended write approvals, and unified automatic selection across configured and paired-remote models also remain unfinished.

See [implementation evidence](docs/progress.md) for specific completed work and remaining limitations, and the [Linear project](https://linear.app/nexusrouter/project/nexusrouter-mvp-fc9fe6d48fda) for the delivery backlog.

## License

[MIT](LICENSE). Release artifacts also carry dependency notices; see [release packaging](docs/release-packaging.md) for how those notices are assembled and reviewed.
