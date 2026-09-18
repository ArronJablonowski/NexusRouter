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
