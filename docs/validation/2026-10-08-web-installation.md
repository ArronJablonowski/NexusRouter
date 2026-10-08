# Installed browser approval — 2026-10-08

Base commit: `f4ab25b7022f7a9c2383f0ba9cadb4f3efba1758`. Verification applies to the source changes committed with this report. Requirements: repository AGENTS.md and the PRD's terminal-approved, loopback-only browser session boundary. Current Linear requirements remain unavailable following the connector's reauthentication failure in this review session.

## Defect and change

The browser successfully loaded `/app/bootstrap`, but the copied approval command returned “Web UI configuration unavailable.” All three legacy discovery paths were absent. The installed user service is named `com.nexusrouter.commander` and supplies `~/.NexusRouter/config/commander-config.json` through its `serve --config` arguments. The CLI was hard-coded to `live-test` directories and service labels.

The CLI now discovers running current-user macOS services in the NexusRouter/DarwinRouter namespaces, filters for the `serve` command, resolves relative paths against the service working directory, and keeps configuration environment overrides and credentials bound to that same service. Only exact API-token environment keys can provide authority. Service-manager output is bounded to 1 MiB per command and discovery to five seconds/32 matching jobs; raw output and tokens never enter diagnostics. Other programs, remote-worker commands and stopped jobs are excluded. Multiple eligible daemons require explicit selection; there is no trial approval across daemons.

Explicit `--config` wins over `NEXUS_CONFIG`/legacy `DARWIN_CONFIG`. An explicit configuration plus terminal API token bypasses service-manager discovery. `--service` selects one matching running macOS service. Standard user and legacy configuration paths are fallback options; missing home directories, invalid files and ambiguity fail closed. Archives and task databases are not searched. The browser command and the existing authenticated loopback approval endpoint remain unchanged.

## Checks and results

All Go commands used `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`; the final CLI regression run used a fresh temporary process-owner directory.

- `go test -race ./internal/cli -run '^Test(Web|DaemonControl|Rename)' -count=1`: passed, final run 1.911 seconds. Covers end-to-end synthetic HTTP approval through discovered service configuration and environment overrides; custom JSON/YAML paths containing spaces; canonical and legacy locations; multiple services/files; explicit path, service and environment precedence; missing paths; token non-disclosure; unrelated environment entries; and bounded output. Existing malformed/leading-hyphen challenge and control boundary checks remain intact.
- `go test -race ./internal/cli ./internal/api ./internal/browserauth -run 'Test(Web|Browser|NativeBrowser|DaemonControl|Rename)' -count=1`: CLI and API passed. Browser-auth had no matching tests under that selector and was subsequently run in full.
- `go test -race ./internal/api ./internal/browserauth -count=1`: browser-auth passed in full (1.104 seconds); API failed in task-execution integration fixtures as detailed below.
- `go run ./cmd/check`, `go vet ./internal/cli`, `git diff --check`: passed.
- `go build -trimpath -o <temporary>/nexus ./cmd/nexus`: passed on macOS arm64.
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o <temporary>/nexus-linux-amd64 ./cmd/nexus`: passed. This is cross-compilation, not Linux runtime qualification.
- The new binary's `config validate --config <installed commander path>` returned `Configuration valid (version 1)`. No configuration values or credentials were printed.

## Broader API failures

The broad API suite failed these tests: `TestHTTPDurableCancellationWithFullExecutionCapacity`, `TestHTTPCompactedContinuationPersistsBeforeProvider`, `TestHTTPEventReplayDisconnectLeavesActiveTaskRunning`, `TestHTTPEventReplayReconnectDoesNotExecute`, `TestHTTPTaskToProviderAndDurableInspection`, `TestMetricsSurviveServiceRestartWithoutExecution`, `TestNativeTaskDurableRetrySurvivesDisconnectedWaiter`, both `TestOpenAIUsageRealProviderTwoTurnDurableIntegration` subtests, `TestDurableTaskStreamFallbackUsesOneGlobalReplayOrder`, `TestDurableTaskStreamTaskFailureReplaysAfterRestartWithoutRedispatch`, `TestDurableTaskStreamDisconnectResumeAndTerminalReplay`, and `TestDetachedHTTPSubmissionSurvivesClientAndRetries`.

Observed errors included unavailable local resource data, admission-denied results and provider/task-start expectations not reached. On a pristine archive of the base commit, with an isolated process-owner directory, the following representative failures reproduced identically:

```sh
go test -race ./internal/api \
  -run '^(TestHTTPEventReplayReconnectDoesNotExecute|TestMetricsSurviveServiceRestartWithoutExecution)$' -count=1
```

These two failures are confirmed pre-existing. The remaining broad-suite failures were not individually requalified or fixed in this browser-installation change. Passing focused approval/control checks are not reported as a full API-suite pass.

## Limits

No real model inference, remote-router calls, private task database/log inspection, service restart or configuration mutation was required. Validation used synthetic HTTP fixtures; it did not consume or approve the user's browser challenge. Automatic service credential discovery is macOS-only and limited to the current user's namespaced launchd jobs; other platforms/service managers use an explicit configuration and trusted terminal token. Multiple installations need explicit selection. Linux runtime and other macOS versions remain untested. No `make check` or exhaustive installation qualification is claimed.
