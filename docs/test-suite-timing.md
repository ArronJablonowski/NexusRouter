# Full-suite timing evidence

The supported repository-wide verification command is:

```sh
umask 077
make check
```

`make check` runs formatting and the 1,000-line limit, `go vet ./...`, every Go
package under the race detector with an explicit 45-minute package timeout, and
`go build ./...`. Tests are not removed from that command to make the timeout
pass.

To profile the historically slow release-security package without weakening
its coverage, run:

```sh
umask 077
make profile-releasepack
```

The profiling gate runs every ordinary `internal/releasepack` test once with
the race detector and the same 45-minute timeout. It consumes `go test -json`,
rejects a package failure, any unexpected skipped test or subtest, a missing
successful package terminal event, an exhausted timeout margin, and absence of
`TestPostPublicationReceiptRejectsMissingOrTamperedVerificationIdentity`—the
test active when the old implicit ten-minute timeout expired. On success it
prints package elapsed time, declared timeout, remaining margin, the count of
passed top-level tests, and the twelve slowest top-level test groups.

The skip set must contain exactly `TestReleaseQualification`. That test is not
silently omitted: it is the clean-checkout, eight-build campaign required by
`make qualify-release-test` and composed into `make qualify-release`. A newly
skipped ordinary test, a skipped subtest, or disappearance of that explicit
separate-gate boundary makes profiling fail.

This report is timing evidence, not a substitute for `make check`; the full
repository command remains the required verification gate.

`make check` and `make test` run race-enabled packages with `-p=1`. The package
timeout is a per-package safety boundary, so concurrently running the three
SQLite/crash-heavy suites can consume that boundary through host contention even
when each suite has adequate isolated margin. Serial package scheduling keeps
the declared 45-minute limit meaningful; it does not skip, shard, cache-bypass,
or increase any test timeout. Independent package timing remains visible in the
ordinary `go test` output.

## Current evidence

On 2026-09-19, source commit `3507623cb19ba1b295abc551bd96e6d65466fbab`
passed the historical failure-point test independently under the race detector
in 11.345 seconds. The guarded `make profile-releasepack` run then passed all
172 ordinary top-level releasepack tests in 12m54.892s, with exactly the one
declared separate-gate skip and 32m5.108s remaining under the 45-minute timeout.

The slowest groups were:

1. Published-install changed-download/root adversarial checks — 1m33.51s.
2. Clean-commit license freeze and verification — 1m27.73s.
3. Approved-install tamper and unsafe-input checks — 1m2.46s.
4. Publication-adapter boundary mutation — 44.99s.
5. Published-attestation rejection — 35.21s.

Immediately before that focused profile, the same source tree passed the full
`umask 077; make check` gate: application completed in 2,356.462s, telemetry in
2,414.577s, releasepack in 841.883s, SDK in 273.623s, tool-gate in 109.152s,
Web UI in 10.336s, browser BFF in 5.111s, providers in 3.114s, and workers in
47.867s. These measurements characterize that commit and host; later candidates
must rerun both required gates rather than inheriting this timing evidence.
