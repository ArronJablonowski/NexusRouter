# DarwinRouter

A Go-based, local-first agent runtime with adaptive model routing. The product specification is in [DarwinRouter_PRD.md](DarwinRouter_PRD.md).

## Development status

The executable supports layered configuration, automatic or explicit-model headless tasks, and an authenticated loopback HTTP service with durable SQLite/WAL history. Provider calls use an allowlisted transport, with loopback-only enforcement for local models. Operator memory and skill commands and opt-in local read-only file tools are available. Write tools and interactive streaming remain unfinished. See the implementation evidence for remaining work; this is not a released MVP.

Application tasks reject empty or whitespace-only final answers with a durable deterministic failure; tool-only intermediate messages remain valid. Independent model audits can run manually or automatically and remain advisory. Explicit user revisions of subjective evaluation records preserve history and avoid duplicate fitness samples.

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

The temporary local module path is `darwinrouter`. Migrate it to `github.com/ArronJablonowski/DarwinRouter` when publishing the Go SDK.

## Configuration

```sh
./bin/darwin config validate --config examples/local.yaml
./bin/darwin config show --config examples/local.yaml --set workers.max_in_process=1
DARWIN__MODE=local_only ./bin/darwin config validate
```

Precedence: defaults → OS user config directory `/darwinrouter/config.yaml` → working-directory `config.yaml` → `DARWIN__SECTION__FIELD` environment variables → repeated `--set section.field=value` flags. `--user-config` and `--config` select explicit files; missing explicit paths are errors. Nested mappings merge; arrays replace wholesale. Environment and CLI overrides address scalar settings only. Unknown fields, duplicate keys, aliases, nulls, and multi-document YAML are rejected. Configuration files are limited to 1 MiB.

Provider keys are referenced by `api_key_env`; the loader never resolves credential values. Configuration text is literal (shell `${...}` expansion is not performed). Set concrete endpoint/database values in files or override scalar settings through the environment. The display redacts endpoints and database paths. Provider execution uses an owned transport enforcing loopback-only destinations in local-only mode; this is not an operating-system sandbox for arbitrary future tools.

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
- `POST /v1/feedback`: JSON `{"task_id":"TASK_ID","outcome":"accepted","attempt_cost":0}` (or `rejected`). Requires an observed final-attempt cost. Identical retries return 200 without adding samples; conflicts return 409, and ineligible task histories return 422. The body limit is 4 KiB and feedback shares daemon admission capacity with tasks.

`POST /v1/chat/completions` accepts `model`, text-only system/user/assistant `messages`, and optional `stream`. Other OpenAI parameters are rejected. SSE is buffered until durable completion and labeled `X-Darwin-Stream-Mode: buffered`; this is not live token streaming. Usage is omitted when unavailable.

Requests are bounded by configured worker concurrency, a 1 MiB JSON body limit, and a five-minute execution deadline. Duplicate and unknown JSON fields, browser-origin requests, and unauthenticated requests are rejected. API token text is included in application credential redaction. Async submission, idempotency keys, live SSE, separate cancellation, full provider health, and service installation are unfinished. Do not automatically retry a timed-out submission; a durable task may already exist.

## Automatic routing and local knowledge

Record operator feedback on a completed task with `darwin feedback --db path --task TASK_ID --outcome accepted --attempt-cost 0` (or `rejected`). Supply the observed final model-attempt cost explicitly; zero is appropriate only when known. This updates immutable user-feedback evidence and domain fitness atomically. Identical retries do not add samples; conflicting initial feedback or a pre-existing evaluation requires the explicit revision workflow below. Feedback covers the final attempt, not every preceding tool/model turn. No model tool can invoke this adapter. CLI submission output contains no task contents. The authenticated HTTP submission endpoint uses the same initial-record rules.

Explicit subjective corrections are available through `darwin feedback show --db path --task TASK_ID`, then `darwin feedback revise --db path --task TASK_ID --expected EVALUATION_ID --outcome accepted` (or `rejected`). Revisions preserve original evidence and execution measurements, adjust only the quality contribution, and retain one sample per attempt. A user assessment can supersede subjective judge/user evidence, not objective test/tool evidence. Stale revisions conflict; identical retries are idempotent. The revision chain is capped at 100 revisions. HTTP revision support and separate objective/subjective fitness dimensions remain unfinished; stored advisory audits are not automatically converted into judge fitness contributions.

Opt in to the built-in `read_file` tool with a narrow workspace directory:

```yaml
tools:
  enabled: true
  read_root: /absolute/path/to/workspace
  max_turns: 8
```

This allows local models to read UTF-8 regular files up to 64 KiB within that directory. Relative paths and symlinks cannot escape the configured root. Do not include credentials or other files the model should not see in this scope. Enabling file tools excludes cloud execution; explicit cloud selection is denied. Tool results become sensitive durable session content. Tools default off; write tools, interactive approvals and general delegation remain unfinished. Tool-enabled models require `context_tokens` metadata. Each turn checks serialized context including tools and schemas plus a 1,024-token reserve; overflow ends the task without discarding durable tool results. Tokenizer-based accounting, automatic compaction and budget-exhaustion recovery remain unfinished.

Use `--model auto` (or API model `auto`) to select an eligible model using durable domain fitness. Configure each model's `context_tokens`, `estimated_cost`, and local `ram_bytes`; missing metadata fails closed. Context admission currently estimates serialized input bytes plus a 1,024-token reserve. Cost and memory estimates are trusted operator inputs, not measured guarantees. Model discovery is checked live. Automatic local reservations are shared within one daemon, not across separate processes.

Automatic routing defaults to a zero-cost ceiling. CLI routing controls are `--domain`, `--profile`, repeated `--capability`, `--context-tokens`, `--max-cost`, and `--local-required`. Native task JSON exposes corresponding `domain`, `profile`, `capabilities`, `context_tokens`, `max_cost`, and `local_required` fields. Explicit selection bypasses ranking and automatic resource reservations; zero cost preserves its legacy operator override, while a positive cost ceiling and requested capabilities/context are enforced. Execution fallback is not yet implemented.

`darwin memory list|show|put|delete --db path --scope scope` inspects and maintains factual memory. Put reads a complete fact record as JSON from stdin; corrections and deletion require an expected revision. Deletion is logical, not secure erasure of WAL or backups.

`darwin skills list|show|history|draft|rollback --root path --scope scope` maintains procedural skills. Draft reads strict JSON from stdin; rollback requires `--name` and `--expected-version`. Inspection never initializes stores. Activation still requires a trusted programmatic validator; these commands do not enable automatic skill mutation. Treat memory and skill exports as sensitive.

`darwin audit --config path --task TASK_ID --reviewer MODEL_ID --max-cost 0` reviews saved output with an independent configured model. The reviewer requires context and cost metadata, plus memory estimates for local execution. Local history cannot be reviewed in the cloud. Shared services reserve local resources during review; separate CLI processes do not share reservations. Review calls have no tools and no automatic retries. Configured credentials are redacted from review inputs and findings; other sensitive content still requires operator care.

To audit successful tasks automatically, set `evaluation.auto_review_model` to a configured independent model ID and `evaluation.auto_review_max_cost` to an estimated cost ceiling (default zero). `evaluation.llm_judge_enabled: false` disables manual and automatic review. Automatic review runs synchronously after task completion, adds up to a minute within the request deadline, and reports `audit_id`/`audit_status` through native task responses and CLI stderr. A failed review does not change the completed candidate task. OpenAI-compatible responses do not expose these native audit fields. Model estimates are not billing guarantees.

`darwin audits list --db path --task TASK_ID` and `darwin audits show --db path --id AUDIT_ID` inspect immutable advisory audits. Lists accept `--after` and `--limit` (1–100); inspection never creates storage. Audit records are separate from fitness and retain reported usage rather than fabricated dollar costs. Findings may contain sensitive content, so protect exports. Failed review invocations do not yet have their own durable failure record.

## Next sprints

1. Connect authorized tools and bounded delegation to application execution.
2. Add safe execution fallback and automatic validated outcome updates.
3. Integrate context compaction, factual memory and procedural skills into prompts.
4. Add live events, recovery and cross-provider qualification.

See [implementation evidence](docs/progress.md) for completed local work and remaining checks by Linear issue.

See the [Linear project](https://linear.app/darwinrouter/project/darwinrouter-mvp-fc9fe6d48fda) for the full delivery backlog.
