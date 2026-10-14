# Commanding-host collaboration collection — 2026-10-13

Source: changes on parent `a69711d6` in `codex/collaboration-sync`. The PR is based on the existing ComfyUI feature branch so its review contains this feature's changes only. Requirements: the PRD model-to-model communication and commander history sections, plus the user's request for collection and model/host labels. Linear project retrieval was attempted and requires reauthentication.

## Implemented behavior

- Existing model message-board history is collected into the commander's private SQLite/WAL journal. Serving processes collect without an open browser; background work follows daemon cancellation and starts no model/task.
- Explicit new local/remote commander delegation assignments and replies are captured, including worker exchanges inside remote commander execution. Sender metadata is host-issued. Known configured secrets are redacted. Long text is split on UTF-8 boundaries rather than truncated. Ordinary user chats and hidden reasoning are not included; old delegation transcripts are not backfilled.
- The narrow collaboration export endpoint uses the existing mTLS transport and explicit instance-wide `logs` grant in both directions. Ordinary inspection, cloud restrictions, delegation limits and remote mailbox/tool isolation are unchanged. Missing grants are not silently added.
- Durable stream/cursor records, stable import identities, source timestamp preservation, commit-before-cursor ordering and conditional cursor updates permit safe recovery without duplicate records or backward cursor movement. Import/export exclusion prevents echo loops. Source reset/truncation, conflicting records and storage caps block advancement.
- Fair bounded polling reports synced, catching-up and unavailable checks with timestamps. The browser preserves saved messages on failures and renders LLM names, producing hosts, source instances and original times as safe text. Remote task references do not point into unrelated local chat stores.
- Additive journal metadata tables are created when opening an existing collaboration store. Source record limits remain unchanged; import allows additional provenance overhead. Private inference inbox filtering applies to imported records too.

## Verification

Passed on the relevant source states:

- Full `go test -race` suites for `internal/agentchat`, `internal/gridcollab`, `remote`, `webui` and `internal/webuiapp`. Real Chrome was required. Reviewed desktop/narrow screenshots and safe rendering, refresh row preservation, filtering, pagination, unavailable-host display and remote source-reference behavior.
- New real mTLS client/server test: ordinary inspect denied; outbound grant alone denied; bilateral history grant exports actual stored messages; cursor resumes; receiver revocation denies collection.
- New sync tests: offline recovery, process/store reopen, exact deduplication after simulated append-before-cursor interruption, source stream conflict, truncation, no echo, private-message exclusion from cloud inboxes, and valid near-limit source records with added provenance.
- New delegation executor test: actual supervised callback records the exact redacted assignment and reply with distinct commander and Spark model/host identities. UTF-8 multi-chunk recording is idempotent. Existing real model message loops and commander consultation tests pass.
- Focused application race gates covering collaboration, commander binding, remote execution, delegate safety/isolation/admission, batch handling, rejection, panic recovery and audit boundaries passed. Application/CLI approval and ComfyUI checks were included in the broader run; CLI suite selection passed.
- Broader application selection `Collaboration|Commander|Delegate|RemoteExecution|Approval|Comfy` failed seven existing top-level delegation tests. All seven reproduced from an isolated archive of unchanged `a69711d6`: batch integration, real-loop integration, delegated read-tools, cloud coordinator worker count, owner-process kill recovery, delegation-tree recovery and hybrid coordinator delegation. Most report unavailable resource data; related cases observe zero worker execution or invalid helper acknowledgement. Their assertions and production admission rules were not weakened. Focused affected boundaries passed after isolating these baseline failures.
- `go run ./cmd/check`: formatting and existing 1,000-line cap passed. `go vet` for journal, sync bridge, remote transport, Web UI, app, CLI, remote CLI and browser adapter passed; final journal/app vet repeated after localized bound/projection refinements.
- Native and CGO-disabled Linux ARM64 command builds passed, repeated after the final production refinements. Reviewed embedded digest: `ce05267026bc54a0857a3b5e4811ad28e557e047fbfac931bc23797967909cf1`. `git diff --check` passed.

## Verification and deployment gaps

This is change-scoped validation, not a full `make check` or complete MVP claim. The existing baseline failures remain unresolved. Live physical Spark/commander collection, real LLM collaboration, power-loss durability, journal replacement reconciliation and Windows were not qualified. Tests used isolated journals and local mTLS fixtures; no production private conversation was read, dispatched or modified. Existing daemons, trust, credentials and configuration were not changed. Activation requires updated source and commanding hosts, enabled collaboration and existing bilateral history-export grants, followed by verified idle deployment with backups. Private logs, screenshots, source archives and binaries remain outside Git.
