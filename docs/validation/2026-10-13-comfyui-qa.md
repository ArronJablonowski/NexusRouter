# ComfyUI extensive QA and fixes — 2026-10-13

Source: changes on `a156d42d` on `codex/comfyui-connector`. This report covers the connector, approved chat tools and authenticated browser controls. Logs, receipts, binaries and generated images remain private outside Git.

## Reproduced issues and fixes

1. **Lost-submission warning could be cleared by viewing another job.** The browser derived uncertainty from the selected job rather than retaining the original submission identity. A real Chrome reproduction failed before the fix. It now retains unresolved job references independently, restores prepared/uncertain records from the server list, and blocks another submission until their saved state resolves. An absent record does not prove non-delivery.
2. **Cancellation could target the previous job after editing the visible reference.** Selection and cancellation authority were separate. Editing invalidates the confirmed target; the action requires an accepted status for the exact visible reference. Chrome verifies cancellation becomes disabled when that reference changes.
3. **Polling rebuilt unchanged output previews.** Every successful status check replaced images and download links, causing repeat retrieval and potential focus loss. Preview identity now follows the selected job and output state; unchanged status retains the existing DOM. Chrome verifies the same image element survives refresh. Selecting another job clears its predecessor's images.
4. **Unstored submissions could be reported as prepared.** Receipt data was constructed before exclusive file creation, then returned on creation failure. The browser treated its nonempty state as saved. Creation failure now returns no receipt state; a removed-directory reproduction verifies no HTTP dispatch and no phantom prepared job.
5. **Receipt loading accepted invalid state and identity and read before enforcing size bounds.** Loading now bounds allocation, requires a private regular file, rejects unknown fields/trailing JSON, checks origin, digest, client and prompt identities, and permits only the defined state/identity combinations. Invalid state, missing accepted ID, traversal ID, credential origin, extra field, oversized/public/symlink receipts fail closed. Directory listing also reads at most 1,001 entries before enforcing its 1,000-entry limit.
6. **Malformed nonempty cancellation acknowledgements appeared successful.** They now report uncertain delivery without retry. The first strict fix rejected Spark's valid empty HTTP 200; live QA caught this compatibility regression. Inspection of Spark's installed `server.py` confirmed `/queue` returns an empty response, and the final fix accepts that response plus JSON-object adapter responses. Both malformed-body and native-empty regressions pass. An acknowledgement means removal was requested, not proof a running or completed job was canceled.

## Validation evidence

Passed unless explicitly noted:

- Initial failure reproductions: `go test ./connectors/comfyui -run '^TestQA' -count=1`; Chrome reproduced the lost-submission fence bug before repair.
- `DARWIN_REQUIRE_CHROME=1 go test -race ./connectors/comfyui ./internal/config ./internal/webuiapp ./webui ./tools`: full suites for these packages, including real Chrome, desktop/narrow layouts, auth/CSRF, owned outputs, replay protection, malformed responses and concurrent receipt ownership.
- `go test -race ./internal/app ./internal/cli -run 'Comfy|Approval|Browser.*Proposal' -count=1`: chat dispatch approval, durable browser proposals, denial, local/cloud restrictions, terminal preview and related approval regressions.
- Additional receipt restart test: recreate the service after ambiguous delivery, observe the original uncertain receipt, no second HTTP request and no unbound cancellation.
- `go test -race ./connectors/comfyui ./internal/webuiapp -count=1`: final boundary suites after cancellation compatibility refinement and extra recovery tests.
- `go test ./connectors/comfyui -run '^$' -fuzz '^FuzzParseWorkflowQA$' -fuzztime=15s -parallel=2`: 597,554 executions, no parser/canonical round-trip failures.
- `NEXUS_COMFY_LIVE_ENDPOINT=http://127.0.0.1:18188 go test -race ./internal/webuiapp -run 'ComfyBrowserSpark' -count=1`: authenticated 48×24 PNG submission/retrieval, same-key replay, retained validation rejection, an 8×8 image job, and exact completed test-ID removal request. The intermediate strict cancellation implementation failed; the final implementation passed after diagnosing native empty acknowledgement. No uncertain request was resent.
- `go run ./cmd/check`: source formatting and existing 1,000-line limit passed. Reviewed embedded digest: `2b88d80a14bd08bcbb9b9dfa1bb257de78ce9ef3fb760f470545f84560e9492a`.
- `go vet` for connector, app, CLI, browser adapter, config, web UI and tools; final connector/browser-adapter vet after refinement. Native and CGO-disabled Linux ARM64 command builds passed, repeated after the final production change. `git diff --check` passed.

Successful unaffected gates were reused after final test additions and the localized cancellation refinement. Production changes were not made while their validation was running.

## Coverage and limitations

This is extensive change-scoped QA, not a full `make check` or complete MVP qualification. Earlier unrelated delegation/dispatcher baseline failures documented in the chat/web report remain gaps; their safeguards were not weakened. Chat model behavior used deterministic model-loop fixtures, not a live LLM evaluation. Diffusion-model performance, shared GPU admission, Windows, process-kill/power-loss durability, hostile same-user filesystem replacement, and an actual pending-versus-running cancellation race on Spark were not exercised. Live removal used our completed test ID to preserve unrelated work. Lost delivery remains conservatively unresolved without external evidence; it is never automatically replayed.

Spark's existing ComfyUI service was active and queue empty before live work. Tests used isolated `NexusRouterTests` output prefixes, private temporary receipts and an owned SSH tunnel. No model services, original NexusRouter daemons, credentials or trust were changed. Web/chat activation in the existing daemon remains opt-in and requires the separate idle deployment step described in `docs/comfyui.md`. Linear remains unavailable pending connector reauthentication.

## Installation checkpoint

Verified source checkpoint `e5ec71d0` was pushed before installation. Updated the separate workstation and Spark `nexus-comfy` commands, retaining previous executables privately. Spark's additive command was unused and ComfyUI queue empty at the boundary. Verified transferred Linux ARM64 SHA-256 `6c62fd941476c81d9a63abc213175f104781a48e730c4706d86b522dbd829e8d` before atomic replacement; the installed command successfully inspected the empty queue. Existing NexusRouter daemon binaries and configuration were not replaced. The owned QA tunnel was closed. The draft PR description now includes this QA evidence.
