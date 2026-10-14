# Context correction and longer Spark collaboration — 2026-10-14

Root cause: the preceding live test explicitly allocated 4096 tokens. After Qwen read a full message and sent a reply, its accumulated serialized tool definitions, calls and full send receipt exceeded the conservative byte-based context estimate before final confirmation. Provider usage (943 input tokens on its second turn) is a tokenizer measurement, not this conservative runtime bound. The confirmed send must not be replayed to recover that final confirmation.

Changed collaboration_send to return a small durable `{sent, sequence}` receipt instead of re-echoing all text/internal provenance. Full records remain unchanged in the journal/export/Web UI. Added collaboration_read limit (1–50, default 10) and lossless before/next_before paging. Existing task admission, conservative estimation, privacy and no-uncertain-replay safeguards remain intact. Explicit context allocation is not silently increased.

Validation: full race-enabled internal/agentchat, runtime and providers suites passed (runtime 212.845s); race-enabled app tests matching TestModelCollaboration, Test.*ContextOverflow, Test.*ContextAllocation and TestExplicitCompaction passed. New regression covers compact receipts, same scoped call returning the original receipt without duplicate append, bounded default/explicit reads, lossless pagination, trusted provenance and invalid limits. go vet ./internal/app, go run ./cmd/check, native and Linux ARM64 builds passed. Scoped validation, not full make check.

## Deployment and physical qualification

Clean source `5b22cfbc` was built for native commander and Linux ARM64 Spark after the verified source checkpoint was pushed to GitHub. Installed canonical commands on both hosts at checked idle boundaries, preserving configuration and private rollback backups. Spark router is active with zero automatic restarts; Ollama PID2187152 and ComfyUI PID2421 were preserved. Commander startup took longer than the first readiness timeout; subsequent read-only readiness returned expected 401 and authenticated browser verification succeeded. No inference was retried for startup readiness.

Rollback backups:
- Commander: `/Users/aj_lab/.NexusRouter/bin/backups/20261014T185145Z-5b22cfbc`
- Spark: `/home/arron_spark/.local/share/nexusrouter/deployment-backups/20261014T185144Z-5b22cfbc`

Ran **12 alternating messages** (six Muse Glimmer and six Qwen 3.8 27B) on Spark, under topic `long-collab-20261014-1ac96dc4`. Each model read the latest addressed message with limit1, copied its unique synthetic challenge and advanced a running value by7, ending at84. Eleven actual read tool executions and twelve confirmed compact send receipts were verified from owned task events. Each task used an explicitly admitted 16384-token window and at most six turns; this is a multi-task conversation, not an unlimited single context. The private test configuration disabled ComfyUI only for unattended CLI execution; live configuration/approval requirements were preserved.

An authenticated live Chrome observer saw message counts advance through the exchange to12 without manual refresh. Final comparison proved **12 source records = 12 commander records = 12 actual DOM rows**, exact text/provenance/original timestamps and source sequence mapping, with no duplicate imports. Both displayed model names (`muse-glimmer:30b-q4_K_M-dflash`, `qwen3.8:27b`) and hostname `spark-9c8a` were correct; source instance `dgx-spark` reported synced. Direct topic navigation/reload of saved messages, desktop/mobile rendering and mobile overflow checks passed. Temporary browser session was logged out and owned Chrome removed.

## Dispositions and limits

**Zero context overflows** occurred across the12 tasks. Eleven task processes completed normally. At step6 Qwen durably sent the correct message, then returned no visible final text (`empty_output`, with a final provider turn reporting usage but no normal text). The driver stopped, source delivery and Web UI receipt were inspected, and execution resumed explicitly at step7 after validating all six existing records; step6 was never replayed. Remaining tasks requested a visible final sentence and completed. This model-output failure remains a separate qualification gap, not a context overflow or lost message; no safety checks were relaxed to turn it into a success.

The compact receipt and paging changes reduce avoidable context growth; they do not guarantee arbitrary text fits any fixed window or automatically replay failed side effects. Existing broader delegation baseline/Linear access gaps remain. Full `make check` was not run. All private prompts, task events, test configuration, raw messages, screenshots, results and field comparisons stay outside Git in `/Users/aj_lab/.NexusRouter/resources/long-collaboration-2026-10-14` (Spark has its matching private directory).
