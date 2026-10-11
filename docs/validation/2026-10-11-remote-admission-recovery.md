# Remote admission and recovery validation

Source parent: f314fe14 (verify final commit in Git). Campaign source remains pinned to 7d4b7aac4203e6f8bdfa95a44763b58ef3eef540.

## Diagnosis

Remote service was alive and serving mTLS on 9443. The original campaign request had a durable reservation with no submission binding, and no submission for its exact destination/caller/key-derived digest. Its admitted-to-uncertain audit duration was 20 seconds, matching the HTTP handler timeout. No inference was admitted for that request. The controller only retried status, which maps unbound reservations to a generic unavailable response indefinitely.

Remote serve constructed a separate SDK service for Submit, while its dispatcher and ReadStatus used an application service with an owned store. Separate-service Submit calls telemetry.Open per request; submission now borrows the dispatcher-owned service and store. Historical internal wait duration is not directly measured; do not claim an observed database deadlock or model crash.

## Recovery contract

POST /v1/remote/tasks/{request}/recover requires inspect permission, mTLS and current model/privacy authority, exact stored task digest and caller ownership. It cannot submit inference. Existing submissions are found by exact idempotent request and configuration; ownership binding repairs are idempotent. Missing exact submission returns a distinct not-admitted error. Missing reservation, conflicts, unsupported backend and storage errors never silently resubmit. Dispatch and recovery serialize within the remote server using a context-bounded admission slot; canceled waiters cannot hang indefinitely behind a stalled admission.

## Validation

Initial `go test ./remote ./sdk/v1 ./internal/remotecli`: remote and remotecli passed; SDK suite failed on existing Request field parity (DisableFallback) and local-resource/route fixture failures. Those unrelated failures are not weakened or claimed fixed. New recovery tests use isolated temporary SQLite and synthetic mTLS backends, no model inference.

Race-enabled remote and remotecli gates passed before the dispatcher-owned submission wiring; final recovery gates listed below. No full-suite pass claimed.

Final scoped checks: `go test -race ./remote ./internal/remotecli` passed after dispatcher-owned service wiring. Final recovery/header compatibility and SDK no-storage guards: `go test -race ./remote ./internal/remotecli ./sdk/v1 -run 'TestRecoverStatus|TestSDKSubmissionGuardsNoStorageCreation'` passed. `go vet ./remote ./internal/remotecli ./sdk/v1` passed. Local and Linux ARM64 CGO-disabled command builds passed. Application and telemetry scoped submission/recovery tests passed; `TestSDKSubmissionRestartAndDispatcherInteroperate` failed with admission_denied in the existing fixture and remains a limitation, not a successful gate.

Private campaign worker recovery logic was exercised with isolated temporary state and mocked CLI results for repaired binding, unavailable/uncertain, and confirmed-not-admitted. All passed; no dispatch or live inference performed in those tests. Full-suite validation not run.

Deployment pending the campaign model's idle boundary. No model service changes, credentials, trust alterations or product inference tests.

Expired-request reconciliation: exact-request historical lookup now preserves the original deadline in its digest while allowing an expired deadline only for read-only inspection. New submission still rejects expired execution. Regression covers existing/missing/changed expired requests. `go test -race ./internal/app ./remote ./internal/remotecli ./sdk/v1 -run 'TestResumeSubmission|TestRecoverStatus|TestSDKSubmissionGuardsNoStorageCreation'`, static checks, and both command builds passed after this change.
