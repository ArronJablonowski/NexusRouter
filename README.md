# DarwinRouter

A Go-based, local-first agent runtime with adaptive model routing. The product specification is in [DarwinRouter_PRD.md](DarwinRouter_PRD.md).

## Development status

The executable supports layered configuration, automatic or explicit-model headless tasks, and an authenticated loopback HTTP service with durable SQLite/WAL history. Provider calls use an allowlisted transport, with loopback-only enforcement for local models. Operator memory and skill commands and opt-in local read-only file tools are available. Write tools and interactive streaming remain unfinished. See the implementation evidence for remaining work; this is not a released MVP.

Application tasks reject empty or whitespace-only final answers with a durable deterministic failure; tool-only intermediate messages remain valid. Independent model audits can run manually or automatically and remain advisory. Explicit user revisions of subjective evaluation records preserve history and avoid duplicate fitness samples.

Automatic routing separately tracks nonempty-output validity from the latest 100 checked terminal attempts per model/provider/domain/profile. It verifies each check against saved final output and task status. Failures discount the quality component using sample confidence and recency decay; passing this check never proves semantic quality or creates cost/latency measurements. This objective signal remains active when LLM judging is disabled. Legacy tasks without a check contribute no assumed outcome. Compiler/test validation and broader objective checks remain unfinished.

For Go-generation tasks, opt into `darwin run --config path --model auto --validate go_source < prompt.txt`, or supply `"validation":"go_source"` to native `POST /v1/tasks`. Ask for a raw complete Go source file: prose and Markdown fences are rejected, not extracted. The check parses at most 1 MiB of UTF-8 source without loading imports, compiling, or executing anything. Invalid syntax fails the task and records objective evidence; syntax-valid code can still have type errors, missing dependencies, security bugs or failing tests. Validation uses the redacted output that is persisted and delivered. Validity populations are separated by requested validation mode; the OpenAI-compatible endpoint does not expose this extension.

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
SQLite database used by `darwin memory` commands. Retrieval inspects up to
`max_facts` unexpired facts in ID order and includes only whole facts fitting the
serialized message budget; this is not semantic search. Known credentials are
redacted before model dispatch. Context includes fact ID, revision, provenance,
and confidence in a JSON data envelope, preceded by a fixed instruction that
memory is untrusted factual context, never tool or policy authority.

With `local_only: true`, nonempty memory context pins automatic hybrid tasks to
local models. Explicit cloud tasks and cloud-only mode omit memory. With
`local_only: false`, cloud-eligible automatic routing loads only shareable facts;
explicit local models may also retrieve private facts. Context admission and
execution share one snapshot. A deletion after that snapshot does not recall an
in-flight request; the next fresh task retrieves current facts. Continuations
retain historical messages, including previous memory snapshots: deleting a fact
or disabling retrieval does not erase its copies from session history. Memory
retrieval currently neither updates last-use timestamps nor creates/corrects
facts automatically. Prompt separation is not proof of injection immunity;
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
activates or revises a skill. Automatic skill generation, production workflow
validators, semantic relevance selection and regression-triggered rollback are
still unfinished. As with memory, old skill snapshots remain in continuation
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

The temporary local module path is `darwinrouter`. Migrate it to `github.com/ArronJablonowski/DarwinRouter` when publishing the Go SDK.

## Configuration

```sh
./bin/darwin config validate --config examples/local.yaml
./bin/darwin config show --config examples/local.yaml --set workers.max_in_process=1
DARWIN__MODE=local_only ./bin/darwin config validate
```

Precedence: defaults → OS user config directory `/darwinrouter/config.yaml` → working-directory `config.yaml` → `DARWIN__SECTION__FIELD` environment variables → repeated `--set section.field=value` flags. `--user-config` and `--config` select explicit files; missing explicit paths are errors. Nested mappings merge; arrays replace wholesale. Environment and CLI overrides address scalar settings only. Unknown fields, duplicate keys, aliases, nulls, and multi-document YAML are rejected. Configuration files are limited to 1 MiB.

Integer fields (counts, token limits, byte sizes, and schema version) require
unquoted integer values in YAML: `max_facts: 8`, not `8.5` or `"8"`. Values must
fit their integer type and pass the field's limits. Environment/CLI overrides
still use text such as `--set memory.max_facts=8`, but fractional or overflowing
values are rejected. Type-invalid lower-precedence files or environment values
are rejected even if a later layer overrides them. Genuine floating-point
settings, such as estimated cost and routing weights, remain supported.

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

`--continue-task` starts a new task from a completed task's saved conversation in the same database and session. The source remains immutable, and the new task records its parent. Missing, unfinished or uncertain-effect histories are rejected. Histories created on local models (and legacy histories without a privacy marker) cannot be continued on cloud models. This is completed-session continuation, not interrupted-task recovery. Combined input is limited to 4 MiB and configured per-model context admission still applies.

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

Generation makes one auxiliary call without tools or retries. Application admission enforces deployment mode, source privacy, resource reservation and cost metadata, then persists a `started` attempt before dispatch. Configured credentials are redacted from input and draft content. Success atomically stores the proposal with `drafted` status; failure stores a generic code. Cancellation cleanup is bounded independently. A crash or unavailable store can leave `started` indeterminate—it is not proof that a summarizer is still running. Inspection opens storage read-only and supports up to 100 records per page, with optional task filtering and an exclusive `--after` ID cursor.

Drafting never modifies the source, starts a continuation or affects fitness. Inspect the full proposal and verify its accuracy before using its `Draft.Request.summary` object as an operator-reviewed summary file. Source provenance refers to unchanged durable history, even when the auxiliary input was redacted. Estimates are operator estimates, not billing guarantees; summaries and inspection output can contain sensitive session information. Automatic application, semantic validation, crash reconciliation, HTTP summary endpoints and mid-task compaction remain unfinished.

## Local HTTP service

Set `DARWIN_API_TOKEN` to a securely generated secret of at least 32 characters, then run `darwin serve --config examples/local.yaml`. The configured daemon address must be loopback. This foreground process stops on SIGINT/SIGTERM and cancels active requests during shutdown. It is not yet an installed operating-system service.

All endpoints require `Authorization: Bearer <token>`:

- `GET /health`: application/database health; provider health is explicitly not checked yet.
- `POST /v1/tasks`: JSON `{"model_id":"local-fast","prompt":"Hello"}` with optional `continue_task_id`. With a continuation, optional `compaction` accepts `{"keep":6,"summary":{"decisions":["Retain existing API"]}}` using the same safety checks as the CLI. This initial endpoint waits for durable completion before returning HTTP 201 with `task_id`, `text`, and `turns`.
- `GET /v1/tasks/{id}`: reconstructed task/session state.
- `POST /v1/feedback`: JSON `{"task_id":"TASK_ID","outcome":"accepted","attempt_cost":0}` (or `rejected`). Requires an observed final-attempt cost. Identical retries return 200 without adding samples; conflicts return 409, and ineligible task histories return 422. The body limit is 4 KiB and feedback shares daemon admission capacity with tasks.
- `GET /v1/feedback/{task_id}`: original final-attempt evaluation followed by its revision history.
- `POST /v1/feedback/revisions`: JSON `{"task_id":"TASK_ID","expected_id":"EVALUATION_ID","outcome":"rejected"}` (or `accepted`). Uses the same subjective-only correction policy as the CLI, with a 4 KiB body limit and shared capacity. Identical retries return 200; stale/conflicting corrections return 409; evidence-policy denials return 422. Execution measurements cannot be changed through this endpoint.

`POST /v1/chat/completions` accepts `model`, text-only system/user/assistant `messages`, and optional `stream`. Other OpenAI parameters are rejected. SSE is buffered until durable completion and labeled `X-Darwin-Stream-Mode: buffered`; this is not live token streaming. Usage is omitted when unavailable.

Requests are bounded by configured worker concurrency, a 1 MiB JSON body limit, and a five-minute execution deadline. Duplicate and unknown JSON fields, browser-origin requests, and unauthenticated requests are rejected. API token text is included in application credential redaction. Async submission, idempotency keys, live SSE, separate cancellation, full provider health, and service installation are unfinished. Do not automatically retry a timed-out submission; a durable task may already exist.

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

This allows local models to read UTF-8 regular files up to 64 KiB within that directory. Relative paths and symlinks cannot escape the configured root. Do not include credentials or other files the model should not see in this scope. Enabling file tools excludes cloud execution; explicit cloud selection is denied. Tool results become sensitive durable session content. Tools default off; write tools, interactive approvals and general delegation remain unfinished. Tool-enabled models require `context_tokens` metadata. Each turn checks serialized context including tools and schemas plus a 1,024-token reserve; overflow ends the task without discarding durable tool results. Tokenizer-based accounting, automatic compaction and budget-exhaustion recovery remain unfinished.

Use `--model auto` (or API model `auto`) to select an eligible model using durable domain fitness. Configure each model's `context_tokens`, `estimated_cost`, and local `ram_bytes`; missing metadata fails closed. Context admission currently estimates serialized input bytes plus a 1,024-token reserve. Cost and memory estimates are trusted operator inputs, not measured guarantees. Successful model discovery is cached for up to five seconds per provider/endpoint/credential/privacy identity; execution failures invalidate it. Failed discovery is not cached. Automatic local reservations and discovery caches are shared within one daemon, not across separate processes.

Automatic routing defaults to a zero-cost ceiling. CLI routing controls are `--domain`, `--profile`, repeated `--capability`, `--context-tokens`, `--max-cost`, and `--local-required`. Native task JSON exposes corresponding `domain`, `profile`, `capabilities`, `context_tokens`, `max_cost`, and `local_required` fields. Explicit selection bypasses ranking and automatic resource reservations; zero cost preserves its legacy operator override, while a positive cost ceiling and requested capabilities/context are enforced.

Automatic execution permits one fallback after a provider-declared retryable first-turn failure with no text/tool proposals and a successfully persisted failure. It rechecks the preselected alternative's eligibility and remaining estimated cost budget. Partial output, validation failure, tool activity, cancellation and persistence failure do not authorize retries. Local-task privacy remains local on fallback. Each attempt has its own durable task ID with retry lineage; CLI/native task responses include previous attempt IDs. Returned text/usage belong to the final attempt, not aggregate billing. Explicit model requests do not auto-fallback. Broader recovery, validation-driven fallback and adaptive retry policies remain unfinished.

`darwin memory list|show|put|delete --db path --scope scope` inspects and maintains factual memory. Put reads a complete fact record as JSON from stdin; corrections and deletion require an expected revision. Deletion is logical, not secure erasure of WAL or backups.

`darwin skills list|show|history|draft|rollback --root path --scope scope` maintains procedural skills. Draft reads strict JSON from stdin; rollback requires `--name` and `--expected-version`. Inspection never initializes stores. Activation still requires a trusted programmatic validator; these commands do not enable automatic skill mutation. Treat memory and skill exports as sensitive.

`darwin audit --config path --task TASK_ID --reviewer MODEL_ID --max-cost 0` reviews saved output with an independent configured model. The reviewer requires context and cost metadata, plus memory estimates for local execution. Local history cannot be reviewed in the cloud. Shared services reserve local resources during review; separate CLI processes do not share reservations. Review calls have no tools and no automatic retries. Configured credentials are redacted from review inputs and findings; other sensitive content still requires operator care.

To audit successful tasks automatically, set `evaluation.auto_review_model` to a configured independent model ID and `evaluation.auto_review_max_cost` to an estimated cost ceiling (default zero). `evaluation.llm_judge_enabled: false` disables manual and automatic review. Automatic review runs synchronously after task completion, adds up to a minute within the request deadline, and reports `audit_id`/`audit_status` through native task responses and CLI stderr. A failed review does not change the completed candidate task. OpenAI-compatible responses do not expose these native audit fields. Model estimates are not billing guarantees.

`darwin audits list --db path --task TASK_ID` and `darwin audits show --db path --id AUDIT_ID` inspect immutable advisory audits. `darwin audits attempts --db path --task TASK_ID` inspects admitted review lifecycles, including failures. Lists accept `--after` and `--limit` (1–100); inspection never creates storage. Audit records are separate from fitness and retain reported usage rather than fabricated dollar costs. Findings may contain sensitive content, so protect exports.

Review execution persists `started` before calling the reviewer, then records the validated audit and `completed` status in one transaction, or `failed` with a generic code. Cancellation cleanup has an independent five-second storage deadline. Admission denials do not create attempts. A crash or storage failure can leave an attempt `started`; this means indeterminate, not proof that a review is still running. Automatic reconciliation is not implemented. Failed reviews never become candidate performance evidence. The standalone audit-storage API remains available for imported records; only `CompleteReview`, used by application execution, guarantees atomic audit/lifecycle persistence.

## Next sprints

1. Connect authorized tools and bounded delegation to application execution.
2. Expand safe fallback qualification and automatic validated outcome updates.
3. Add automatic context summarization and knowledge maintenance beyond current operator-compacted continuation, scoped factual memory and validated procedural-skill retrieval.
4. Add live events, recovery and cross-provider qualification.

See [implementation evidence](docs/progress.md) for completed local work and remaining checks by Linear issue.

See the [Linear project](https://linear.app/darwinrouter/project/darwinrouter-mvp-fc9fe6d48fda) for the full delivery backlog.
