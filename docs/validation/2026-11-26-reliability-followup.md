# Reliability follow-up — 2026-11-26

The SDK now forwards DisableFallback through its shared request conversion.
Native harness registration and execution use the smaller of the provider
request timeout and the native adapter's 15-minute maximum. This fixes startup
rejection when the provider uses the default 30-minute HTTP deadline and
preserves shorter configured deadlines.

Test fixtures now supply zero swap usage where the synthetic host is meant to
have complete telemetry. Free-memory tests select strict admission, and child
process tests use an isolated home. Browser tests wait for the full navigation
state and exact downloaded contents. Tests still check successful admission,
privacy boundaries, durable recovery, and download integrity.

The SDK race suite, full SDK suite, full Chromium and Firefox suite, targeted
application and SDK race regressions, source checks, all-package vet and build,
and Linux ARM64 remote CLI build passed on macOS ARM64 with Go 1.27.4. The
native install rehearsal passed, including schema migration and rollback copy.
Release qualification with external artifacts and live Telegram delivery were
outside this test run.

The complete application suite passed using the normal test entry point with a 12-minute package budget. The SDK and application suites retain their established opt-in live tests.

The follow-up application race selection covered fallback, delegation, recovery,
and Workboard journal integration. The releasepack run passed 169 top-level
tests and skipped the opt-in release qualification. Seven signing/trust tests
failed because the fixture hardcoded Go 1.27.1 in the manifest while its SBOM
used the running Go version. After aligning the fixture metadata, the signing
and trust-record suites passed with the race detector. The archive and
signature verification rules are unchanged.

Commander deployment used code commit 4430abed. The initial launchd bootstrap
returned exit 5, so the previous binary was restored and its readiness
verified. Restarting the registered job then activated the candidate with
verified binary digest and a quick-checked SQLite rollback backup. After the
existing database startup validation completed, authenticated doctor reported
ready and retained all previously healthy checks. Existing provider/model
availability still produces a degraded overall status. The Web UI bootstrap
returned HTTP 200. No active tasks, submissions, unattended lease attention,
or Workboard claims were present at the service boundary. The subsequent
releasepack fix affects test fixtures only.

## Spark deployment and local model test

Deployed the clean Linux ARM64 build at source commit `ca1310a0`. The
executable SHA-256 was verified before and after installation. The previous
executable, runtime configuration, unit definition, and verified SQLite
snapshots for tasks and remote receipts were saved in the private Spark
deployment backup. The task database remained at schema 53. Only the
remote router service was restarted, at an idle boundary. The dedicated
Ollama process retained its PID and the configuration and unit digests
matched their pre-deployment values.

Authenticated inspection of the paired `dgx-spark` instance returned
available on `spark-9c8a`, with measured resources and 33 of 34 models
usable, matching the pre-deployment inventory. One private, zero-cost
request ran through NexusRouter on `qwen3:8b` using `spark-ollama`.
It returned the expected `SPARK_SMOKE=49`. The committed start event
confirmed the model, provider, and local-only privacy. The task completed
without fallback and reported 41 input tokens and 242 output tokens.

Post-test checks confirmed the router was active with zero automatic
restarts and no nonterminal tasks, pending submissions, or unreleased
leases. This validates the paired local inference path.

## Spark thermal startup investigation

Spark on DGX OS 7.6.0, kernel 7.0.0-1025-nvidia and BIOS
5.36_0ACUM028 exposed seven ACPI thermal zones. All seven had readable
temperatures and a 104.8 °C critical shutdown trip, with no passive trip
or cooling-device binding. NVIDIA documents ACPI passive trips as the
threshold for CPU software throttling on Grace. Reading below the critical
trip alone therefore leaves host thermal headroom unknown.
[NVIDIA Grace thermal documentation](https://docs.nvidia.com/dccpu/grace-perf-tuning-guide/power-thermals.html#cpu-temperature-management)

The GPU driver reported a positive T.Limit margin and inactive thermal
slowdown flags. These readings establish GPU conditions at the time of
sampling. The CPU driver exposed `perf_limited` on all 20 CPU policies,
each reading zero. Its sticky bits record delivered-performance excursions
from thermal, power or current limits and cannot establish thermal headroom
before a limit is reached. These readings do not fill the missing host
thermal evidence. No registers were cleared. The configured firmware
inventory reported no applicable update.
[NVIDIA GPU thermal fields](https://docs.nvidia.com/deploy/nvidia-smi/#temperature)
[Linux CPU telemetry ABI](https://github.com/torvalds/linux/blob/master/Documentation/ABI/testing/sysfs-devices-system-cpu)

The installed `nexus resources` command still reported unknown thermal
pressure with enough RAM to start the runner. Authenticated paired
inspection confirmed Spark available and 33 of 34 models usable.
`spark-vllm-qwen3-8b` was the sole unavailable model. The vLLM service
remained inactive. This was a read-only investigation. No product fix was
deployed, no service settings changed, and no vLLM inference was dispatched.
