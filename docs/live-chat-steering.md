# Supervised Sol / Ollama chat steering

This opt-in qualification exercises the production line-oriented chat entry point
through an owned subprocess and real stdin/stdout pipes. It uses the signed-in
Codex CLI with `gpt-5.6-sol` and the installed Ollama model from
`examples/sol-codex-local-smoke.yaml`. It is not a synthetic provider replay.

## Before running

- Confirm `codex login status` reports the intended ChatGPT login. Do not copy
  credential files or paste keys into the configuration.
- Start the local Ollama service and install the model deliberately beforehand.
  The qualification does not pull models or change their installation.
- Review the sample's explicit local model, endpoint, context/RAM estimates and
  delegation limits. The current sample selects `gemma4:12b-it-q4_K_M`, one local
  model at a time, and one delegated call. Hardware admission remains active.
- Expect cloud inference usage and local CPU/GPU/memory use. Only the synthetic
  test prompt, guidance and generated worker output are submitted. Local worker
  inference does not make a cloud-coordinated task end-to-end private.

Run from the repository root:

```sh
DARWIN_CHAT_CODEX_LIVE_STEERING=1 go test -race ./internal/cli -run '^TestChatCodexLiveSteering$' -count=1 -v
```

Default checks skip this test. Do not enable it globally in CI or a shell profile.
Each invocation performs a fresh task; do not automatically retry failures as if
they were free or proof that no inference occurred.

## Verified run

On September 6, 2026, the first live run passed on macOS ARM64 using signed-in
Codex CLI 0.153.4 / `gpt-5.6-sol` and real Ollama `gemma4:12b-it-q4_K_M`.
It completed in 22.02s (test package 23.546s). This is one supervised run, not a
latency benchmark. The owned database contained exactly three completed tasks:
the coordinator, delegated work record and local execution. The checks below
passed, including exact final coordinator marker and no repeated delegation.

The subprocess used the real CLI entry point in the test executable, without
service hooks or provider fixtures. It used pipes rather than a keyboard/TTY;
this does not independently qualify a packaged release binary or terminal UX.

## What the qualification checks

The test clones the reviewed sample into disposable task storage, leaving the
sample and any existing task database unchanged. It asks Sol to delegate a small
Go source task. Once the CLI displays the trusted delegate-start status, it sends
`/steer` guidance asking for a revised final marker without repeating work.

The test checks the rendered queue/application/completion statuses and reopened
durable records. The parent, work and execution records must have correct lineage,
model attribution, paired tool identity and output agreement. Steering must have
been queued during delegation and applied after its result, before subsequent
coordinator inference. The local execution must pass the existing deterministic
syntax validator and worker acceptance, and complete exactly once.

Only bounded test metadata should appear in diagnostic output. Model text,
subprocess stderr and credential values are not needed to report qualification.
The exact recorded run outcome and limitations belong in `docs/progress.md`.

## Scope and limits

This uses the real chat command loop with pipes, not a manual keyboard/TTY or
full-screen usability test. A revised marker is narrow instruction-following
evidence, not general semantic correctness. Go syntax validation does not compile
or execute the generated code. The test does not qualify all cancellation/crash
windows, model versions, resource pressure, long sessions or automatic recovery.

Worker execution retains its normal bounded deadline; cold model loading can
cause failure even when the outer test allows more time. Investigate the actual
failed boundary before retrying or changing budgets. Do not increase permissions,
disable admission checks or accept a stale result to make the test pass.

For normal operator use, run `darwin chat` with a reviewed configuration and use
`/status`, `/steer TEXT` and `/cancel` during active work. See
[interactive streaming](interactive-streaming.md) and
[Codex steering](codex-steering.md) for delivery, privacy and failure semantics.
