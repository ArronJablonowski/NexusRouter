# Additional cloud models — 2026-10-15

## Change and configuration

The Models page discovered seven additional models through the existing `codex-coordinator` provider, but configuration and checked-launch admission hardcoded `gpt-5.6-sol`. Both now share an explicit supported-model list containing the existing model and the seven discovered names. Unknown names remain rejected. Existing cloud privacy, executable/environment validation, tool isolation, exact-model response checks, context and budget admission remain unchanged.

Prepared additive private commander configuration using the existing Codex provider/login, retaining every existing model and the commander/fallback selections. New entries declare chat/code/orchestration and medium reasoning. Current non-secret Codex catalog metadata supplies 500,000 context tokens for `gpt-6.1-sol`, and 272,000 for each of `gpt-6-astra`, `gpt-6-sol`, `gpt-6-luna`, `gpt-5.6-terra`, `gpt-5.6-luna`, and `gpt-daybreak-blue-latest`. These are route recommendations, not separately tested maximum capacities. Image support is not declared for this transport.

New entries inherit the existing cloud routing planning estimate of 0.1. This is not verified per-request billing or a token price; subscription allowance and actual monetary cost remain unverified. No API credentials were added or exposed. Configuration load/validation/round-trip checks preserve original semantics (empty versus nil allowed-origins slices normalized only for comparison).

## Verification

Full race package checks passed: config 13.484s, codexbridge 10.543s, codexrpc 6.517s. Added config and checked-launch cases cover every new model; existing unknown-model, privacy, environment, lifecycle and model-mismatch tests remain. Formatting/1,000-line gate, scoped vet, native and Linux ARM64 builds and diff checks passed. No Web UI assets changed.

Real NexusRouter Service.Run checks used isolated task storage, disabled tools/memory/skills/collaboration and explicit model IDs with fallback disabled. All seven completed exactly `CLOUD_MODEL_OK`; committed task-start events match the requested model and `codex-coordinator`, with terminal task-completed records. The synthetic prompt contained no repository/user content. These verify text access, not general model quality or maximum context. First diagnostic attempts used an insufficient explicit 2048-token window and failed context admission before a model turn; those terminal histories were retained separately. Qualification used 16,384 tokens and separate requests; no uncertain request was replayed.

Broader application/CLI race selection covered Codex/cloud/exact-model/model-use/inventory boundaries. CLI passed (1.755s); app selection had one failure (263.946s) in the OpenAI-compatible cloud-to-local delegation count fixture: expected two child calls, observed zero, while parent returned its canned answer. That fixture does not use the changed Codex transport. Baseline investigation is recorded below. No full-suite or MVP pass is claimed.

[Official Codex app-server documentation](https://developers.openai.com/siwc/token-sharing-open-source/codex-app-server) distinguishes catalog discovery from entitlement; successful inference was used here to verify each selected model. Private configuration, state, logs and login data remain outside Git. Linear authentication remains unavailable.

The delegation-count failure reproduced unchanged on clean archived baseline `0697b167` (2.042s, same parent=2/child=0 result). It is therefore retained as an existing regression/environment gap, not weakened or treated as a new-model pass. Other selected tests completed without reported failures. Configuration/bridge/RPC suites and all seven real model checks passed independently.

## Deployment result

Installed code `ac2f0556` and additive private configuration at an idle commander boundary. Existing configuration, runtime data and binary backed up to `~/.NexusRouter/bin/backups/20261015T205631Z-cloud-models`; binary/config replacements are atomic and the deployment script restores both on failure. Canonical native binary SHA-256: `f241e94c79b83e4571527bfbea76d78c910eb4e0a2d315463fc88bf7ad825e76`. Commander restarted successfully; Spark/model services were not restarted.

Authenticated live inventory confirms all seven additional cloud models configured, enabled and usable, with complete catalog discovery. Actual installed Models page browser check found eight cloud cards (seven new plus existing commander), all seven new switches checked and enabled, and no configure-before-enabling warning. Initial read-only browser check had a missing helper in the private test harness; corrected the harness and reran the observation without dispatching inference. Verification sessions logged out. Existing commander remains `gpt-5.6-sol`; new models are available for explicit selection and normal routing. Source changes and sanitized evidence backed up on GitHub; private configuration and account state were not committed.
