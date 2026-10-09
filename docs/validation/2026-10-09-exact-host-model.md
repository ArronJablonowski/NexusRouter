# Exact host/model assignment validation — 2026-10-09

Source: working tree based on `b0b4dccc5f8a2c7f331c8fedadaab8ec4d8c1e36`.
The implementation and this report are committed together. Requirements use the
checked-in PRD and direct user instruction; Linear retrieval required reconnect.

## Evidence

- Isolated exact pin/browser-mutation/autocomplete checks passed under race in app, browser handler and Web UI packages. Fixtures verify stored `disable_fallback`, unchanged idempotency identity, no commander fallback after a retryable exact-model failure, remote explicit no-fallback, host/model keyboard completion, unique-name/ID resolution, ambiguous/reserved forms, bound expected model name and uncertain-request recovery without replay.
- Full `go test -race -timeout=10m ./remote` with live/SSH opt-ins disabled and offline dependencies passed (50.948s). New real SDK/mTLS fixture rejects a renamed model under the same alias with zero provider calls, advertises the targeting capability, and completes the correctly pinned task through existing durable lifecycle/event/cancellation checks. Initial test compilation missed an `errors` import; corrected without production/test assertion changes.
- `go test -race ./internal/app -run '^(TestExactModel.*|TestBrowserExplicitModel.*|TestBrowserChatUsesConfiguredDefaultModel|TestAutomatic.*Fallback.*|TestUnified.*|TestTopThree.*|TestRemoteExecution.*)$' -count=1`: passed (51.691s). This includes the existing automatic fallback chains and top-three unified behavior, not just the new explicit path.
- Full browser-handler race package passed (6.226s). Full Web UI race runs passed behavior/browser checks but failed the main script source limit at 1001, then 1000 lines. The guard requires strictly fewer than 1000. Consolidated only the added declarations to meet the existing threshold; assertions were not weakened. Successful unaffected package results were retained.
- Final source formatting/vet/build, shell/manifest and affected browser checks are recorded below. Chrome tests use disposable authenticated shell/provider metadata fixtures; no actual models or routers are contacted.

The local catalogue is authenticated and content-free. Backend model-name checks
apply at the destination SDK's immutable configured model boundary; catalogues
are previews, not permission or health grants. Native configurations, task journals,
caller certificate pinning and admission remain authoritative.

## Limits

No deployment, physical inference, private runtime database/log/credential reads,
service/configuration changes or external router traffic were performed. Fake
provider factories exercise runtime loops. This is scoped verification, not a
full repository `make check` or release/MVP qualification. The new remote form
requires upgraded peers advertising targeting version 1. Multiple assignments,
automatic selectors, CLI/SDK text parsing and paid/cloud remote composer policy
remain unimplemented. See the README/assignment guide for actual scope.

## Final checkpoint

- `DARWIN_REQUIRE_CHROME=1 ... go test -race ./webui -run '^(TestEmbedded.*|TestChromeHostMentionsRouting|Test.*Shell.*|TestModelTargets.*|TestPublishedModelTargetsSchema)$' -count=1`: passed (2.989s). Includes the strictly-below-1000 guard, embedded JavaScript syntax, reviewed asset manifest, new catalogue schema, two-stage keyboard selection, optional hostname fallback, ambiguous names, explicit name-bound dispatch and recovery. Refresh/input changes preserve an uncertain-delivery notice; the independent remote-task button appears only for an existing remote request.
- Fixture screenshots inspected at 1440 and 390 pixels with the assignment controls in view. The destination and model labels wrap without horizontal overflow. Chat/session error messages in these captures reflect deliberately absent chat APIs in the fixture, not a production service check.
- Reviewed v1 embedded asset digest: `34236316c1f04a4c7fb9a2804c13f51e56f16071f4d4d08e8868179fc7169510`.
- Final `go run ./cmd/check`, `go vet ./...`, `go build ./...` and `git diff --check`: passed. Offline dependencies used (`GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`). Documentation changes reviewed without rerunning unchanged application checks.
- GitHub main remained `b0b4dccc` before the normal backup push. Only code/tests/docs were staged; no credentials, binaries, private state or screenshots were committed.
