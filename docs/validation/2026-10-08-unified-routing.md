# Unified routing validation — 2026-10-08

Source: working tree based on GitHub/local `052eb93b`, containing the unified
routing, remote text-conversation/direct-provider boundary, top-three failover,
and Web UI changes committed with this report. No deployment is part of this
source-validation turn. Linear requirements could not be refreshed because its
connection requires reauthentication.

## Isolated regression evidence

- `go test -race ./internal/app -run '^TestUnified' -count=1`: passed after correcting the initial fixture to use the public message-only request shape.
- `go test -race ./internal/app ./internal/gridroute ./remote -run '^(TestUnified.*|TestTopThree.*|TestDirectInference.*|TestRemoteSDKTextConversationPreservesPriorTurns|TestRecordedCancellation.*|TestBridge.*|TestFederatedProvider.*)$' -count=1`: passed (13.276s / 1.660s / 9.835s).
- Current-head, withdrawal, identity/profile isolation and advisory-confidence projection checks passed under race in `./harness`.
- Authenticated mTLS bridge fixture proves host-advertised score 1.0 cannot replace a caller-owned failed evaluation, verifies bound conversation dispatch, and rejects a revoked cached peer without further network traffic.
- Actual SDK intake/dispatcher/provider fixtures preserve prior conversation turns and exercise the built-in provider route. A reused blocking fixture initially retained conversation messages, so its replacement prompt was not used; clearing messages on that separate blocking request restored its intended cancellation check.
- Browser routing order, stale refresh/backoff, safe text, yellow remote names/hostname, keyboard disclosure, empty/failure state and mobile width checks passed. The height check now waits for ResizeObserver reconciliation after expansion rather than reading before the browser's layout update.
- Desktop and mobile screenshots were inspected from disposable Chrome fixture captures. Remote origin is indicated by text and yellow styling; evidence details remain inside the card at mobile width.
- Embedded v1 asset manifest reviewed: `8c9f828d0cbdc792536c459fcedb6a27ef2860763da094e260bb95d9ad8ba968`.

## Broader validation

Final checks completed on 2026-10-09. Source is the commit containing this report;
the final remote-card CSS sizing, provider failure assertions, and ranked-caller
certificate pinning changed after the broader gate and received scoped checks.

- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go run ./cmd/check`: passed, including the final source state.
- `go vet ./...`: passed in the broader gate.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go build ./...`: passed separately after the broader gate.
- `make check` with `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`, `DARWIN_REQUIRE_CHROME=1`, and live/native opt-ins explicitly disabled: **failed/incomplete**. Formatting and full vet passed. The serial race run completed harness/adapters, gridroute, configuration, browser authority, and other packages, but failed release-input, API, application and CLI tests. Common failures included unavailable local resource data, admission denial, and subprocess audit initialization. After roughly 58 minutes, the validation's Go runner and releasepack test were interrupted while release packaging was still running; downstream packages and the gate's build step were not completed. This is not a full-suite pass.
- Representative baseline checks were run in a disposable `git archive HEAD` checkout at untouched `052eb93b`: `go test -race ./internal/app -run '^(TestNativeHarnessCapacityUsesContextOverheadAndLiveReservations|TestNativeHarnessReadinessObservesPrerequisitesWithoutInference|TestPressureExplicitCapacityRetryAndProfileFailure)$' -count=1` reproduced all three failures (2.174s). Prior evidence also reproduces release-input, representative API and hybrid-delegation failures on `663e1572`. **Not every broader failure has been independently baseline-reproduced or diagnosed**; remaining regression uncertainty is retained.
- `NEXUS_REMOTE_LIVE_MODEL=0 NEXUS_REMOTE_SSH_NATIVE=0 DARWIN_REQUIRE_CHROME=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -race -timeout=10m ./remote ./routing ./webui ./internal/webuiapp`: all four full package suites passed (45.805s, 1.149s, 29.348s, 6.728s). This separate boundary check completed after the broader gate was stopped.
- After final CSS/test-only edits: `DARWIN_REQUIRE_CHROME=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -race ./internal/gridroute ./webui -run '^(TestFederatedProvider.*|TestChromeRoutingRemotePaths|TestRoutingMap.*|Test.*Shell.*)$' -count=1`: passed (1.418s / 2.738s). Explicit assertions distinguish confirmed cancellation from unresolved delivery and prevent retry after user cancellation or delivered output. Remote evidence facts use one column in narrow cards to avoid crowded labels.
- Final fixture screenshots were captured again and reviewed after the CSS sizing change. Browser execution is fixture-backed, not a live deployment.
- Final ranked-caller binding: `go test -race -timeout=10m ./remote ./internal/gridroute ./internal/app -run '^(TestRecorded.*|TestBridge.*|TestFederatedProvider.*|TestUnified.*|TestTopThree.*|TestDirectInference.*|TestRemoteSDKTextConversationPreservesPriorTurns)$' -count=1` with live/SSH opt-ins disabled and offline Go dependencies: passed (10.029s / 1.666s / 15.105s). The dispatch boundary rejects a changed caller fingerprint before binding or sending, and fresh admission must match the ranked caller. Full vet, full build and formatting were repeated successfully after this change.
- `git diff --check`: passed. GitHub `main` remained `052eb93b` before the backup; no remote changes were overwritten.

These checks support a change-scoped source checkpoint. They do not establish
production deployment, full recovery qualification, or a clean full repository
gate.

## Limits

No real routers, real model inference, operator credentials/private runtime
stores/logs, configuration mutation or service changes were used. Opt-in physical
host/native SSH/live inference tests are excluded. Remote work requires upgraded
peers; direct identity without a configured weight digest identifies configuration
rather than attested weights. Tool portability, incremental remote tokens,
automatic interrupted-parent remote reconciliation, specialized legacy automatic
APIs and generic SDK host wiring remain outside this implementation. Passing
fixtures does not complete MVP or production release qualification.
