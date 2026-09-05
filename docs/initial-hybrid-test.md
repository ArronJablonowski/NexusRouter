# Initial supervised hybrid test

The first live test uses **GPT-5.6 Sol (`gpt-5.6-sol`) as coordinator** and an
operator-selected installed Ollama model as the local worker. This is a
supervised test profile. One real coordinator → local worker → coordinator
round trip passed on September 5, 2026; broader qualification remains open.

OpenAI documents streaming and function calling for the exact requested model:
[GPT-5.6 Sol](https://developers.openai.com/api/docs/models/gpt-5.6-sol).
The preferred coordinator connection is now **Codex CLI using the existing
ChatGPT login**, as requested by the operator. `codex login status` reported
ChatGPT authentication, and an ephemeral read-only `codex exec` invocation using
`--model gpt-5.6-sol` returned `DARWIN_SOL_READY` successfully. No saved
credentials were extracted and no separate API key was used.

## Initial scope

1. Sol receives a small, non-sensitive task and delegates a bounded subtask.
2. One local worker at a time returns its result through DarwinRouter.
3. Sol reviews the worker result, identifies omissions or suspected defects,
   and produces the final answer. Review opinions are not test execution proof.
4. Inspect the parent/worker event history and provide explicit user feedback.
5. Repeat with an invalid or incomplete worker result, cancellation, and a
   clean daemon restart. Confirm failures are visible and no duplicate work
   is silently dispatched.

Start without filesystem tools, autonomous writes, automatic skill activation,
or background learning. Use a small text/code task; raw Go syntax validation
does not compile or execute code. Cloud coordination means the submitted task
and returned worker content reach OpenAI: local inference is not end-to-end
local-only privacy.

## Prerequisites and readiness evidence

- For the preferred CLI route, a working authenticated Codex CLI. The experimental
  native adapter is wired for explicit fresh tasks and has passed the small
  live test below. It remains experimental, not production-qualified.
- For the optional direct-HTTP route only, a locally configured
  `OPENAI_API_KEY`; never paste the credential into chat or commit it.
- A hybrid configuration with Sol pinned as the parent and an explicit local
  `workers.delegate_model`, conservative RAM/context estimates, bounded call
  and cost limits, and capacity for the parent plus one child.
- A reachable Ollama endpoint and measured worker memory use. On inspection,
  the default local endpoint was reachable and reported installed models;
  the host reported 48 GiB of memory. Model fit and latency remain unqualified.
- A successful real parent-to-child-to-parent run with durable event inspection.

## Verified live checks

- Sol through Codex CLI: returned the requested marker and exited successfully.
- Local worker through DarwinRouter: `gemma4:12b-it-q4_K_M` returned
  `DARWIN_LOCAL_READY`; the task and nonempty validation were persisted. The
  observed one-turn cold-start run took about seven seconds. Ollama subsequently
  reported approximately8.1GB loaded memory; this is not a peak-memory or
  sustained-performance qualification.
- Automated HTTP-adapter fixtures cover a simulated Sol coordinator delegating
  to an Ollama worker, result return, rejection and privacy/credential isolation.
  These are not live cloud calls or a Codex CLI adapter test.

`examples/sol-local-smoke.yaml` is the optional direct-HTTP profile; it is not a
Codex CLI configuration. Its local worker can be checked independently:

```sh
printf '%s\n' 'Reply with exactly DARWIN_LOCAL_READY. Do not call tools.' |
  go run ./cmd/darwin run --config examples/sol-local-smoke.yaml \
    --model local-worker --set workers.delegate_model= --local-required \
    --domain smoke --json
```

Cost fields are admission estimates, not hard billing or output-token caps.
The CLI coordinator uses the signed-in account's Codex usage limits.
CLI adapter safety/cancellation and sustained qualification remain unfinished.
Full MVP work continues separately from these initial checks.

## Experimental CLI profile and first integrated attempt

`examples/sol-codex-local-smoke.yaml` uses `codex_app_server`, the existing
ChatGPT login, and an absolute CLI executable path (adjust it on another host).
No HTTP endpoint or API-key setting belongs on this provider. Only explicit
fresh tasks are supported initially: select `--model coordinator`. Automatic
health/routing discovery, auxiliary judging, history import and compaction are
not implemented for this provider yet. Memory and skills remain off in the sample.

The first CLI-backed application attempt created durable task
`BQJR6VQWJRF4GCVVMDL3G4R4SR` in the ignored sample database and failed before
any local delegation. A separate protocol diagnostic confirmed that Codex accepted
thread and turn creation, then sent startup deprecation/warning notifications
which the strict adapter rejected. Diagnostics associated the notices with
`use_legacy_landlock`, `web_search_cached`, `web_search_request`,
`skip_host_skill_discovery`, and `code_mode_host`. This is not proof those warnings
were harmless. That initial failure was resolved in the checkpoint below.
Raw warning payloads and credentials are not persisted.

## First successful end-to-end checkpoint

Task `6BE4WNSJV2MLSJMAFQSSOJKTKU` completed on September 5, 2026 in about
12 seconds from the persisted start to completion. This is one observation,
not a latency benchmark. It used the existing ChatGPT login, with no API key.

- Sol emitted exactly one `delegate` call requesting raw Go source.
- Local execution `XKZTM6QHSJDNICFUU2VYI3XLOV` ran
  `gemma4:12b-it-q4_K_M`, with a `local_only` child privacy policy.
- The child returned `package answer` and `func Answer() int { return 42 }`.
  Darwin recorded accepted `deterministic.go_syntax.v1` evidence.
- The persisted tool result carried the child output as `untrusted_output`.
- Sol received that result, reviewed the package/signature/return value, and
  the parent completed. Its review is model feedback, not compiler/test evidence.

Reproduce from the repository root with this non-sensitive prompt:

```sh
printf '%s\n' 'Use only darwin.delegate exactly once. Ask the worker to output raw Go source: package answer with func Answer() int returning 42. Set validation to go_source. Review the result. Do not use other tools or read files.' |
  go run ./cmd/darwin run --config examples/sol-codex-local-smoke.yaml \
    --model coordinator --domain code --json
```

The pinned CLI profile enables its installed local `code_mode_host` to support
Sol's tool invocation path. Shell and other feature controls stay disabled;
extension/skill/hook inventories are still checked. The host-disabled diagnostic
completed a model turn but produced no delegation and is not counted as success.
Only exact known deprecation/unstable-feature notices and bounded, discarded
account-rate metadata are accepted; unknown warnings still fail closed.

Next qualification: negative worker results, cancellation while delegated work
is active, restart behavior, and helper-process cleanup/containment. These are
not established by one successful prompt. Background learning remains disabled.
