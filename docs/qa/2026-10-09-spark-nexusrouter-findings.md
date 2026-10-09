# NexusRouter repository QA on Spark — campaign started

Snapshot: `7d4b7aac4203e6f8bdfa95a44763b58ef3eef540`.
Host: `spark-9c8a`, paired instance `dgx-spark`. NexusRouter direct remote tasks use its dedicated Ollama runner, with explicit expected model names and private zero-cost admission.

- Muse Glimmer: `muse-glimmer-30b-q4-k-m-dflash` / `muse-glimmer:30b-q4_K_M-dflash`.
- qwen3.8 27B: `qwen3-8-27b` / `qwen3.8:27b` (131072 configured context ceiling).
- Laguna S 2.1: `laguna-s-2-1` / `laguna-s-2.1:q4_K_M`.

## Status and findings

Campaign in progress; no validated bugs recorded yet. The first smaller Muse source/test batch is running. qwen and Laguna follow sequentially; they are not yet confirmed to have completed a code review. 632 primary batches cover tracked UTF-8 text at the pinned commit, including source, tests, requirements and documentation. The two binary image assets are excluded. Models rotate across batches: this is distributed coverage, not three independent full-repository audits. Candidate-bearing reports receive a second-model cross-review, with subsequent caller tracing needed to establish verified defects. Do not equate scheduling or a successful model response with complete coverage.

Live Markdown findings and the coverage manifest are maintained outside the repository:

`/Users/aj_lab/.NexusRouter/resources/qa-2026-10-09/findings.md`

State, pinned source snapshot, numbered batches and durable request receipts are beside that file. Reports will be copied here at reviewed checkpoints. Task envelopes/private transport configuration are excluded from Git.

## Workflow and limitations

Remote task contracts grant no filesystem/tool authority. Code is supplied as numbered text; models cannot run tests or follow unseen callers. Findings are unverified candidates unless independently traced/reproduced, and missing context must be recorded. Tests are review inputs, not a passing test-suite claim. No product fixes are authorized by this QA workflow; operational problems may be corrected to maintain progress.

The worker submits one task at a time, persists intent before dispatch, uses recorded caller-bound request identity, inspects the same request after uncertain delivery, and never automatically replays it. Initial oversized batches failed admission with context_overflow observed; retained those receipts and rebuilt the inputs to fit conservative serialized-byte limits. The replacement source-review request reached running. No model/service/configuration/privacy safeguard was changed. Helper syntax validation passed using Python py_compile; subsequent real task completion remains pending.

Ten-minute monitoring is active in the Codex app (automation `monitor-spark-nexusrouter-qa`) to inspect progress, diagnose workflow failures, validate candidates and record gaps. The worker runs on this Mac and the heartbeat needs Codex available; machine/app downtime can delay checks. The campaign stops when primary and cross-review jobs are terminal and monitoring then pauses, reporting failures/incomplete coverage honestly. No exhaustive-review or model-quality claim is made.
