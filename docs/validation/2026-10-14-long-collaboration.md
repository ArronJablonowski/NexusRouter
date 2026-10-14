# Context correction and longer Spark collaboration — 2026-10-14

Root cause: the preceding live test explicitly allocated 4096 tokens. After Qwen read a full message and sent a reply, its accumulated serialized tool definitions, calls and full send receipt exceeded the conservative byte-based context estimate before final confirmation. Provider usage (943 input tokens on its second turn) is a tokenizer measurement, not this conservative runtime bound. The confirmed send must not be replayed to recover that final confirmation.

Changed collaboration_send to return a small durable `{sent, sequence}` receipt instead of re-echoing all text/internal provenance. Full records remain unchanged in the journal/export/Web UI. Added collaboration_read limit (1–50, default 10) and lossless before/next_before paging. Existing task admission, conservative estimation, privacy and no-uncertain-replay safeguards remain intact. Explicit context allocation is not silently increased.

Validation: full race-enabled internal/agentchat, runtime and providers suites passed (runtime 212.845s); race-enabled app tests matching TestModelCollaboration, Test.*ContextOverflow, Test.*ContextAllocation and TestExplicitCompaction passed. New regression covers compact receipts, same scoped call returning the original receipt without duplicate append, bounded default/explicit reads, lossless pagination, trusted provenance and invalid limits. go vet ./internal/app, go run ./cmd/check, native and Linux ARM64 builds passed. Scoped validation, not full make check.

Longer live exchange/deployment verification pending. Private evidence will remain outside Git.
