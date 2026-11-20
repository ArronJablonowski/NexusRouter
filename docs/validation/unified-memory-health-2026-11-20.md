# Unified-memory health validation — 2026-11-20

The accelerator resource check previously required independent VRAM counters even when the resource profiler explicitly reported unified memory. Apple silicon intentionally leaves those counters absent. The health check now uses the measured host RAM capacity and its configured RAM threshold, including thermal/swap pressure. Dedicated VRAM fields stay absent; this is shared-memory capacity health, not a claim of GPU allocation/utilization telemetry. Contradictory unified/VRAM observations remain unknown.

## Change-scoped validation

- `go test -race ./internal/app -run "^(TestHealth|TestScopedHealth)" -count=1`: passed, including shared-memory pressure/invalid-input cases, complete healthy-report integration, configured GPU binding and scoped provider discovery.
- `go test -race ./resources -count=1`: passed.
- `go test -race ./resources ./health ./internal/app ./internal/cli ./daemon ./internal/webuiapp -run "Test(.*Health|Doctor|.*Inspection|.*ResourceThreshold)" -count=1`: health, CLI and web UI checks passed; the application package also selected an unrelated pre-existing dispatcher failure. Resources and daemon had no matches in this selection; resources were tested in full separately.
- `go vet ./internal/app ./health ./resources`: passed.
- `gofmt` on changed Go files and `git diff --check`: passed.
- `go build -o <private staged service executable> ./cmd/nexus`: passed; native configuration validation and live resource profiling passed.

`TestDispatcherHealthRenewalDuringBlockedProvider` reports `provider not entered`. A Go build overlay restoring both health source/test files from base commit `1099afbb` reproduces the same failure without this change. No assertion was weakened or dispatcher behavior changed. This remains an unqualified dispatcher fixture; these checks are not a full-suite pass or a complete release qualification.

## Runtime verification scope

The build was activated after backups and an idle-work check. Authenticated readiness, every enabled health check, lease attention, provider discovery and a deliberately submitted local inference were inspected. Operational backups, owner-lock proof and task identifiers remain private and outside Git. This patch changes no scheduler settings, task dispatch authority, tool permissions or lease recovery rules.

## Diagnostic deadline follow-up

Live activation of the shared-memory patch produced an authenticated `healthy`, ready report with no degraded/unknown checks, and an operator-labeled local submission returned `NEXUS_HEALTH_OK`. The separate `nexus doctor` client inherited daemon control's three-second HTTP timeout despite a seven-second context and a server health budget of five seconds. A timed-out client could therefore report unavailable while direct health inspection succeeded.

Doctor now uses a seven-second HTTP timeout matching its existing diagnostic context. Fast daemon status/stop controls retain their original timeout. A loopback test waits 3.1 seconds before returning an authenticated, validated healthy response; the doctor must accept it. `go test -race ./internal/cli -run "^Test(Doctor|DaemonControl)" -count=1` and `go vet ./internal/cli` passed. This change requires no new inference qualification because dispatch code is unchanged; final native doctor inspection verified the activated executable.

## Final live result

Deployed code commit: `a30005d93440c79013b44c62cd7378390c806006` (the shared-memory patch and doctor deadline follow-up). Native `nexus doctor --config <installed commander configuration>` exited 0 with `status=healthy`, `ready=true`, and no degraded or unknown checks. A local verification through the commander returned `NEXUS_HEALTH_OK`; the subsequent change touched only the doctor client, so inference was not repeated.

Final inspection found zero running tasks, zero queued/running submissions, zero unreleased leases and zero open lease-attention records. SQLite quick-check passed and foreign-key inspection found no violations. The original source-host owner-lock evidence and later reboot supported an explicit operator release of the historical local benchmark lease; automatic writer recovery rules were not broadened, and no old tool results or task history were rewritten. Spark log collection and the ComfyUI tunnel remained operational. Workboard scheduling remains disabled. Runtime evidence and rollback binaries are retained privately outside the repository.

These are scoped regression and live operational results, not a full-suite or release-qualification claim; the independently reproduced original-source dispatcher fixture failure above remains recorded.
