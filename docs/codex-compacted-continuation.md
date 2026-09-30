# Reviewed compacted history for Sol continuation

The experimental signed-in `codex_app_server` path accepts explicit continuation
with either a manual operator-supplied compaction or a currently approved stored
summary. This connects [Sol summary drafting](codex-session-summaries.md) to a
shorter coordinator history. It does not activate a draft automatically, replace
the source journal, resume an in-flight turn, or use native automatic compaction.

## Use

First inspect the draft against its complete source, then record your review and
continue with the configured exact Sol model ID:

```sh
./bin/nexus summaries show --db path/to/darwin.db --id SUMMARY_ATTEMPT_ID
./bin/nexus summary-review --config path/to/config.yaml \
  --attempt SUMMARY_ATTEMPT_ID --decision approved \
  --note "Compared requirements, decisions, pending work and effects with source"
./bin/nexus run --config path/to/config.yaml --model COORDINATOR_MODEL_ID \
  --continue-task SOURCE_TASK_ID --summary-attempt SUMMARY_ATTEMPT_ID < followup.txt
```

For an explicitly operator-supplied summary file, use `--compact-keep` and
`--compact-summary` instead of `--summary-attempt`. The existing NexusRouter-native
task API and SDK `Request` compaction/summary fields reach the same application
admission path. Public callers cannot supply the private resolved checkpoint.

## Admission and provenance

The source must be completed, cloud-eligible, and replayable without pending
tools, interrupted turns or uncertain effects. Recovered-delegation history
can still use ordinary explicit continuation, but cannot use compaction while
its source task is failed. A known model context window is required. Local-only
mode and private/unknown source privacy remain denied even if a private prefix
would be removed by compaction.

The host reconstructs the canonical compacted conversation from the source.
Stored drafts require an exact immutable checkpoint match and current approval.
The native gate also checks that the private resolved checkpoint matches the
requested source and summary IDs. It does not treat request flags as evidence.
The TaskStarted transaction rechecks the exact review head before committing
the new input and checkpoint, including source sequence/digest, retained boundary
and review attribution. The source journal is never rewritten.

Approval's boundary is durable task start, not later model streaming. Rejection
committed before that start blocks history import. Rejection afterward blocks
future direct reuse, but does not cancel the admitted task or erase its copied
context. CLI startup may happen before task start; startup alone receives no
source conversation. Neither a loaded CLI nor a historical approval receipt
overrides the transaction's current-review check.

## Native import

The [official Codex App Server reference](https://learn.chatgpt.com/docs/app-server)
documents `thread/inject_items` for adding model-visible history without starting
generation. NexusRouter uses the existing checked import protocol, not a native
`thread/compact/start` request.

Original system messages retain their roles. A fixed host warning marks the
structured summary as reference data; the summary body itself is a user-role
JSON message, not a system instruction. The recent suffix expands as necessary
to retain complete tool-call/result groups. Those items remain historical typed
calls/results and never execute just because they were imported. Only the new
prompt enters `turn/start`, after the import's checked acknowledgement. New tool
proposals still require the current catalog and normal permission rules.

Retained original tool JSON is checked before context-engine or map-based
redaction can collapse duplicate argument keys. Native structured-output
redaction also rejects malformed/ambiguous tool result JSON and redacted-key
collisions. Current configured credentials are removed during assembly; an
approved summary changed by redaction requires a new draft and review.

All previous bounds remain: one complete injection frame must fit 1 MiB, summary
content is at most 64 KiB with at most 128 nonblank entries per category, and
actual inference must fit its context budget. Compaction may expand a tiny
conversation; no size reduction or accuracy is inferred merely from its name.
No silent truncation, automatic retries or weaker privacy mode is introduced.

## Evidence and limits

Fixture tests exercise canonical session compaction through the application,
SQLite and native bridge: manual/approved imports, exact checkpoint attribution,
original-system and untrusted-summary roles, paired historical tools, unchanged
source, stale/foreign/unapproved/rejected summaries, rotated-secret denial,
revocation on both sides of durable admission, import acknowledgement failures
and exact/overflow frame bounds.

The explicitly gated `TestLiveCodexCompactedContinuation` passed in 4.89 seconds
(race package 6.351 seconds) using one real signed-in Sol continuation. A
synthetic source and loopback-generated draft were approved by a trusted test
host. Sol recalled a synthetic marker present only in the approved summary,
not in the removed raw prefix or new prompt. The new checkpoint exactly matched
source, attempt and review, source events were unchanged, and the owned CLI
session/directory were closed/removed. The provider observer saw one launch and
one stream; it did not count individual import RPCs. No raw user content was used.

```sh
DARWIN_CODEX_LIVE_COMPACTION=1 go test -race ./internal/app \
  -run '^TestLiveCodexCompactedContinuation$' -count=1 -v
```

The live test is skipped by ordinary CI. It is not live local-model delegation,
semantic-summary certification or crash/power-loss recovery qualification.
Automatic compaction/accuracy validation, native in-flight steering, full
isolation and full PRD acceptance remain open.
