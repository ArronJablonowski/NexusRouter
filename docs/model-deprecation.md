# Evidence-based model deprecation recommendations

The read-only diagnostic identifies configured models whose evaluated attempts
cross an operator-selected failure threshold. It never disables, uninstalls,
unloads or deletes a model, changes routing, or creates an approval.

```sh
./bin/darwin models deprecation --config config.yaml --model local-coder \
  --domain code --profile default --window 50 --minimum-samples 20 \
  --failure-threshold 0.35
```

The JSON report contains `population: evaluated_attempts`, the requested policy,
model/provider/domain/profile attribution, counts, a reproducible evidence digest,
the recommendation reason, and `approval_required: true`. Defaults are a trailing
50-evaluation window, 20 eligible samples and a failure fraction strictly above
0.35. A rate equal to the threshold is not crossing it. Window is 1–1,000;
minimum samples must fit the window; threshold is finite and greater than zero
through one. Unknown models, invalid input, canceled reads and unavailable or
corrupt storage return an error rather than a fabricated empty/successful report.

## Daemon API and Go SDK

Authenticated `POST /v1/models/deprecation` is a read-only inspection operation:

```json
{
  "version": 1,
  "model_id": "local-coder",
  "domain": "code",
  "profile": "default",
  "policy": { "window": 50, "min_samples": 20, "failure_threshold": 0.35 }
}
```

All fields are required. Send `Content-Type: application/json` and the daemon's
Bearer token. The route rejects query parameters, browser origins, chunked or
oversized bodies, invalid UTF-8, duplicate/unknown fields and invalid policy
numbers. One dedicated inspection slot and a cooperative five-second deadline
bound the work. Successful responses are the same versioned report as the CLI;
invalid/unavailable backend reports return a generic error without partial data.
POST supplies a bounded structured policy, not permission to mutate model state.

The versioned SDK exposes the same request and report types:

```go
report, err := client.ModelDeprecation(ctx, sdk.DeprecationRequest{
    Version: 1, ModelID: "local-coder", Domain: "code", Profile: "default",
    Policy: sdk.DeprecationPolicy{Window: 50, MinSamples: 20, FailureThreshold: 0.35},
})
```

Both adapters validate requests and report consistency, including exact failure
rates, count partitions, threshold decisions and digest shape. These checks do
not prove the truth of the underlying evidence. The application remains
responsible for the configured target and secret redaction. Neither adapter
executes inference, initializes storage, or changes model eligibility.

## Evidence semantics

The original evaluation time selects the trailing window, with base evaluation
ID as the deterministic tie-break. Current feedback revisions are resolved in one
read transaction, without moving an older attempt into the recent window or
counting revisions as additional samples.

- Execution failures are independent of subjective quality.
- Failed explicit schema measurements remain failures even with positive user feedback.
- Deterministic checks, tool evidence and user feedback follow the existing evidence precedence.
- Successful attempts with only LLM-judge evidence are excluded from the denominator.
- Separate failure categories can overlap; the total counts each eligible attempt once.

The minimum-sample gate applies to eligible samples, not the number of raw
evaluations read. Model-only opinions therefore cannot manufacture a recommendation.
The digest covers the current selected records, but no raw checks, task IDs,
source text or conversation payload are returned. Known credential values are
redacted from returned model/domain/profile identifiers.

For local models, RAM/VRAM figures are configured residency estimates. They are
not observed freeable memory, disk usage, or proof that a model is loaded. Removing
model files does not necessarily unload a resident model. Operator approval and
a separately implemented lifecycle operation are required for changes.

## Bounds and remaining scope

Reads never create or migrate storage. The application has a cooperative
five-second deadline. Retrieval fails above 10,000 candidate evaluations for the
exact key or 8 MiB of selected evaluation/revision history; each history also uses
the existing revision and row-size guards. No incomplete report is returned.

This is a report over persisted evaluations, **not the overall runtime failure
rate**. Unevaluated requests, separate runtime-validity events and missing feedback
are not silently counted as successes or merged into a different denominator.
Unifying those evidence populations, value/cost thresholds, population-wide
scheduled suggestions and measured residency savings remain
follow-up work. The command is an initial implementation of PRD §9.3, not full
model lifecycle or MVP completion.

Tests use local fixtures and cover time ordering, revision consistency, evidence
precedence, schema failures, judge exclusion, strict threshold boundaries, read-only
behavior, missing-store non-creation, corruption, cancellation and CLI validation.
