# Cleanup Settings controls — 2026-10-16

Implemented authenticated last-run inspection and CSRF-protected preview/run controls in Settings. Manual cleanup requires an exact plan fingerprint, compared under the existing cleanup lock before unlink. Reports expose relative path metadata only, without internal tree fingerprints or file contents. An installed-script digest check binds the service to the reviewed embedded maintenance source. No arbitrary paths/commands are accepted. A started run retains a two-minute bound and survives browser cancellation; uncertain outcomes are not automatically retried. Last-run completion markers distinguish finished runs from interrupted/older evidence.

Qualified utility SHA-256: `0333c4d1e3ebb6a939e58fb9d4ea16bf2ac62f14639cd0d3fea7715c7772daf1`.
Qualified native binary SHA-256: `5613919037cd9df0b3dc41ec41f46c7e52051b465541b6f2c1ceb3800580a4c1`.
Reviewed embedded Web UI asset manifest: `64ac6ae9fed4242a896ef7d2086baa27fbecc28d90a5ab7ab998017cf8302845`.

Validation:

- System Python safety suite: 22 passed, including stale-preview rejection with zero deletion, matching-preview apply, completion journal and relative-path projection.
- Full affected race suites: `go test -count=1 -race -p 1 ./internal/maintenance ./scripts ./internal/webuiapp ./webui` passed (2.476s / 6.206s / 7.600s / 84.891s). Existing Python migration/license maintenance utilities remain covered.
- Real service integration on an isolated temporary home used the qualified Python program and real open-file inventory: five backups reduced to three, an open registered build protected, closed build removed, upload sentinel preserved, and last-run status verified.
- HTTP authority tests cover unauthorized access, missing origin/CSRF, rejected query/unknown fields/actions/tokens, preview success, conflict responses and status. A cancellation regression confirms bounded cleanup continues after browser disconnection.
- Firefox and Chrome cleanup interactions passed at 1440/390/320px: status, preview gating, conflict/repreview, explicit run, unavailable outcome, disabled retry and reload without replay. Final Firefox scoped checks passed 2.016s and Chrome 0.982s. Full affected race suites preceded the final one-minute client deadline and bounded disconnect-continuation adjustment; final targeted backend race tests passed 1.535s after that adjustment.
- Scoped CLI serve/browser-mount race tests passed 1.919s. `go run ./cmd/check`, scoped `go vet`, `git diff --check`, manifest review and native build passed. This is targeted qualification, not full `make check`.

Deployment/installed verification follows. macOS commander only; Linux/Spark cleanup is unavailable rather than falling back to unsafe execution. The existing three-backup/registered-build policy and safety limitations remain unchanged. Browser/UI timeout may report an unavailable outcome while a bounded run continues; use last-run inspection. No unrelated services, settings, model calls or archived campaign evidence are modified. Linear remains subject to the previous reauthentication gap.
