# Accuracy-first automatic routing validation — 2026-10-08

## Source and behavior

Base: `663e1572aae26fd6a3d47a75ad00b712048e2e99`, plus the accuracy-first scorer,
regressions and documentation in this commit. No application sources or tests
were edited while validation was running. Documentation was finalized afterward.

New/default routing policies set `AccuracyFirst: true`. Their score is the
existing confidence/decay-adjusted task quality with advisory bounds and objective
invalidity penalties. Price and speed cannot outweigh a more accurate candidate.
Ordinary requests do not explore a weaker alternative; the pure routing API
requires explicit evaluation permission for exploration. Explicit model choices
and operator-configured commander pins remain unchanged. Historical stored
policies omit the new flag and retain their original weighted representation.

Paired-remote model/harness ranking already orders correctness before quality and
confidence. Its new regression selects a stronger remote model over a cheaper
local one and rejects the remote candidate when authorization is absent.

**Unimplemented scope:** this commit does not unify ordinary configured-model
routing with paired-remote candidate discovery for every chat. Global candidate
unification remains a documented product requirement. See
[scope and remaining work](../accuracy-first-routing.md).

## Regression evidence

Before the production change, both new regressions failed:

- `TestAutomaticAccuracyOutranksLatencyCostAndLocality`: the lower-accuracy local
  model scored 0.9475 versus approximately 0.7852 for the more accurate remote
  model because speed and cost contributed to ranking.
- `TestOrdinaryRequestsNeverExploreAwayFromBestAccuracy`: an ordinary draw chose
  the weaker alternative.

Both pass after the change. The application-level durable-domain test now uses
an exploration rate of 0.25 with draw 0, proves the automatic winner remains the
better-evidenced model, confirms an explicit weaker-model pin is honored, and
checks that the new policy is recorded in the durable explanation.

## Successful checks

Go checks used `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`, with fresh temporary
`DARWIN_PROCESS_OWNER_DIR` directories for execution/store tests.

- `go test -race ./routing`: passed (1.262s). Later additions to the regression
  file passed in the targeted routing/harness run; production scorer unchanged.
- Targeted routing and harness accuracy, ordinary-request, historical-policy and
  scoped-remote regressions: passed (1.202s routing, 1.279s harness).
- `go test -race ./internal/app -run '^TestAutomaticUsesDurableDomainFitnessAndAuditsBeforeTurn$' -count=1`:
  passed (4.763s).
- Full harness race suite in the broad check: passed (1.414s).
- `go test -race ./runtime ./sessions ./remote`: passed (219.933s, 17.501s and
  37.031s respectively).
- Selected telemetry route/legacy boundaries: passed (21.751s).
- Selected application automatic/routing/ranking/fallback/explicit/privacy/MVP
  boundaries: all selected cases other than the hybrid fixture below passed;
  the package command exited nonzero because of that fixture.
- `go run ./cmd/check`, `go vet ./...`, `go build ./...`, and `git diff --check`:
  passed. No embedded web assets changed.

The application/telemetry selector was:

```text
^Test(Automatic|Auto|Routing|Browser.*Ranking|BrowserChatUsesConfiguredDefaultModel|CrossProvider|Fallback|ExplicitTaskEndToEnd|LocalOnlyBlocks|MVP|Hybrid|RouteExplanation|.*LegacyRoute)
```

It used `-count=1` and excluded the already-passing durable-domain regression
with `-skip '^TestAutomaticUsesDurableDomainFitnessAndAuditsBeforeTurn$'`.
Successful relevant results were reused; this is not a complete application or
telemetry suite pass.

## Broader verification limitations

An initial `make check` stopped at vet because the cached copy of the already
pinned `golang.org/x/mod v0.37.0` dependency was missing while module lookup was
disabled. Downloaded that exact version using the public Go module proxy and the
existing `go.sum`; dependency declarations were not changed. The next broad
check passed formatting and full vet, then failed:

- `cmd/verify-release/TestTrustRecordCLIEndToEnd`: `invalid release input`.
- Twelve `internal/api` admission/stream fixture tests, including
  `TestHTTPTaskToProviderAndDurableInspection` (`admission_denied`) and
  `TestNativeTaskDurableRetrySurvivesDisconnectedWaiter` (provider did not start).

The broad check was interrupted while the remaining application tests were
running. It did not complete all packages or its final build step; a separate
full-package build passed afterward. No full `make check` pass is claimed.

The selected application command (207.561s) failed
`TestHybridSolCoordinatorDelegatesToIsolatedOllama/success` and `/worker_empty`:
the pinned coordinator completed, but the worker fixture received zero calls.

An untouched source archive of `663e1572` independently reproduced:

- The release fixture failure (0.815s).
- The two named API failures (16.803s).
- Both hybrid subcase failures (3.400s).

Only those named failures were independently baseline-checked; this report does
not claim that every broader-suite failure has been explained or that unrelated
MVP/release gates pass. Assertions were not weakened and unrelated fixtures were
not changed.

## Operational limits

Tests used synthetic providers and temporary stores. No live model inference,
router calls, service restart, deployment, runtime configuration update or private
runtime database/log inspection was used for this change. Linear requirements
could not be refreshed because its connection requires reauthentication. The PRD,
user's accuracy-first instruction and manual-pin clarification were used instead.
