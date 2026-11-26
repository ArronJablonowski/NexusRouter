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
