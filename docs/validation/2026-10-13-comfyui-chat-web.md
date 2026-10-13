# ComfyUI chat and web validation — 2026-10-13

Source: additive integration on parent `3b9bb5e7`, retaining the existing standalone connector. New operator configuration pins the endpoint and a private receipt directory. Disabled default serialization omits the new block to preserve existing configuration identities. No credentials, trust configuration or model services are changed.

## Verified behavior

- Real application/model-loop fixtures: submit requires exact operator approval even under an allow policy; denial sends no request; cloud coordinators are rejected. Existing file-write approvals remain gated.
- Real queued dispatcher/browser-chat fixture: a workflow tool call creates a durable pending approval, the browser projection includes its exact graph, no POST occurs while waiting, and a recorded operator decision allows exactly one submission.
- Terminal preview: exact ASCII-quoted arguments, digest verification, duplicate-field rejection and configured-secret rejection. Read/write tool registration is bounded before approval; worker and host-bound effect-free children cannot inherit execution authority.
- Browser adapter: signed-in session, same-origin/CSRF checks, owned-job lookup, same-key replay observation and conflict protection. No caller-selected endpoint/path or global interrupt is exposed.
- Chrome: ComfyUI route/navigation, workflow selection, saved-job lookup, image preview/download controls, uncertain delivery reference retention, no automatic dispatch replay, desktop/narrow layout, and exact chat-proposal preview helper. Inspected the 390-pixel screenshot; no horizontal page overflow.
- Live Spark: authenticated browser adapter submitted an isolated 48×24 workflow, waited on its original record and retrieved/decoded its PNG through the browser image endpoint. Repeating the request returned a replayed record rather than creating another job. ComfyUI queue was empty before the test. No diffusion model was loaded for it.
- Published approval JSON schema and runtime agree on valid ComfyUI proposals and invalid paths, missing node definitions and unknown arguments. Existing workboard proposal shapes remain unchanged.

## Checks

Passed on the relevant source states:

- Full race-enabled connector, tools, configuration and browser-adapter package suites during integration; targeted compatibility checks after the disabled-configuration encoding refinement.
- Full `go test -race ./webui -count=1` with required real Chrome, followed by focused Chrome/embedded-asset/source-limit/schema checks after final preview/download refinements.
- Race-enabled application/CLI approval, ComfyUI, browser projection, command and related file-write checks; the new queued ComfyUI dispatcher approval test passed.
- `NEXUS_COMFY_LIVE_ENDPOINT` explicit live browser-adapter test against a private Spark SSH tunnel.
- `go vet` for connectors/comfyui, tools, internal/config, internal/app, internal/webuiapp, internal/cli and webui; native and CGO-disabled Linux ARM64 cmd/nexus builds; formatting and `git diff --check`.

Final reviewed embedded asset digest: `61d4a972e6bc528e234775be166288c0fbae55d0a5b683ad6c567004bf48fca8`. The main app script remains below the existing 1,000-line limit; ComfyUI-specific preview logic is in its own module. The manifest assertion was updated after review, not bypassed.

## Limits and existing regression failures

This is change-scoped validation, not full `make check`. Two broader existing tests failed and also reproduced from an isolated archive of unchanged parent `3b9bb5e7`: `TestDelegateReadToolsInheritedScopeAndNoRecursion` lacks resource metadata needed by backend-managed memory admission; `TestDispatcherQueuedRestartExecutesIdempotentSubmissionOnce` did not reach its expected successful terminal state. Their assertions and production admission safeguards were not relaxed. Other affected scoped gates passed. These remain repository verification gaps.

Chat model responses were deterministic local fixtures, not a live LLM quality evaluation. Windows, diffusion-model performance, shared GPU admission, uploads, audio/video preview and automatic uncertain-delivery reconciliation remain unverified or unimplemented. Existing daemon processes were not restarted; applying opt-in configuration to an existing deployment requires a verified idle restart with the updated executable. Linear requirements remain unavailable pending connector reauthentication.

Private receipts, graphs, logs, screenshots and baseline archives are outside Git. Only sanitized test evidence is committed.
