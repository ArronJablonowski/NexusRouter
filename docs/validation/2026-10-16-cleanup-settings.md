# Cleanup Settings controls — 2026-10-16

Implemented authenticated last-run inspection and CSRF-protected preview/run controls in Settings. Manual cleanup requires an exact plan fingerprint, compared under the existing cleanup lock before unlink. Reports expose relative path metadata only, without internal tree fingerprints or file contents. An installed-script digest check binds the service to the reviewed embedded maintenance source. No arbitrary paths/commands are accepted. A started run retains a two-minute bound and survives browser cancellation; uncertain outcomes are not automatically retried. Last-run completion markers distinguish finished runs from interrupted/older evidence.

Qualified utility SHA-256: `0333c4d1e3ebb6a939e58fb9d4ea16bf2ac62f14639cd0d3fea7715c7772daf1`.
Qualified native binary SHA-256: `9b2e39d1560b545febbc0c70e952f8b0306551c4f73732df758a43540f7f84d0`.
Reviewed embedded Web UI asset manifest: `3c692f1b40e5cf994886b63e43d6dca20effe7e536cc7dd94c94046899bfeb0e`.

Validation:

- System Python safety suite: 22 passed, including stale-preview rejection with zero deletion, matching-preview apply, completion journal and relative-path projection.
- Full affected race suites: `go test -count=1 -race -p 1 ./internal/maintenance ./scripts ./internal/webuiapp ./webui` passed (2.476s / 6.206s / 7.600s / 84.891s). Existing Python migration/license maintenance utilities remain covered.
- Real service integration on an isolated temporary home used the qualified Python program and real open-file inventory: five backups reduced to three, an open registered build protected, closed build removed, upload sentinel preserved, and last-run status verified.
- HTTP authority tests cover unauthorized access, missing origin/CSRF, rejected query/unknown fields/actions/tokens, preview success, conflict responses and status. A cancellation regression confirms bounded cleanup continues after browser disconnection.
- Firefox and Chrome cleanup interactions passed at 1440/390/320px: status, preview gating, conflict/repreview, explicit run, unavailable outcome, disabled retry and reload without replay. Final Firefox scoped checks passed 2.016s and Chrome 0.982s. Full affected race suites preceded the final one-minute client deadline and bounded disconnect-continuation adjustment; final targeted backend race tests passed 1.535s after that adjustment.
- Scoped CLI serve/browser-mount race tests passed 1.919s. `go run ./cmd/check`, scoped `go vet`, `git diff --check`, manifest review and native build passed. This is targeted qualification, not full `make check`.

Deployment/installed verification follows. macOS commander only; Linux/Spark cleanup is unavailable rather than falling back to unsafe execution. The existing three-backup/registered-build policy and safety limitations remain unchanged. Browser/UI timeout may report an unavailable outcome while a bounded run continues; use last-run inspection. No unrelated services, settings, model calls or archived campaign evidence are modified. Linear remains subject to the previous reauthentication gap.


## Deployment qualification and first installed test

Source 40f92871 was committed/pushed before deployment. The deployment helper's second idle check initially raced SQLite shutdown-sidecar removal and failed before replacing any artifact. The prior service was restored. Corrected the helper to wait for the exact old commander PID to exit, require an empty/absent WAL, and inspect the stopped database immutably before backup. Removed only the empty aborted staging directory. Deployment then succeeded with the shared maintenance lock held, a private rollback executable/config/state/utility backup, SQLite integrity checks and unchanged schedule/registry/settings. No remote service was restarted.

The first installed Firefox preview correctly skipped the fixture because it shared a directory with the active verification worker. Moved the fixture into its own private disposable directory without weakening the policy, then the installed test passed: last-run read, exact one-file preview, manual deletion, report preservation, status refresh, reload, zero-item repeat preview, disabled run without a new preview, and 1440/390/320px layouts. The owned browser session was logged out and temporary profile removed.

Visual inspection of actual installed screenshots found crowded action buttons and no separation from the next panel. Added a scoped wrapping flex row with 8px gaps and panel spacing, plus a geometry/style regression. Final scoped Firefox interaction checks passed 2.282s; Chrome cleanup/shared 15-page responsive style/shell/manifest race checks passed 4.189s. Source checks, diff checks and the final native build passed. Broader backend suites are reused unchanged; no claim that these final scoped checks were a full suite. Final installed verification follows.

## Final installed verification

Final source `a6980f61` was pushed and deployed to the commander under the shared maintenance lock, with idle submissions/leases/tasks checked and a private rollback/state/config/utility backup. The deployed binary and utility match the qualified hashes above. The daily 03:30 local LaunchAgent was unchanged; older deployment copies remain conservatively protected during the one-hour staging grace period, for subsequent scheduled cleanup.

The final real Firefox test passed on the installed authenticated Settings page at 1440/390/320px, including computed 8px wrapping action gaps and visual inspection of desktop/narrow screenshots. It read the prior last run, previewed exactly one aged registered native fixture, removed only that file via the UI, preserved its Markdown sentinel, refreshed the successful one-removal result, retained the result after reload, and returned an empty repeat preview with Run disabled. The private receipt independently confirms completion, exactly one deletion and zero errors. The owned browser session was logged out and its temporary profile removed. Fixture reports/screenshots remain private; the temporary compiled build was removed after installed-hash verification.

Installed unauthenticated GET and POST cleanup endpoints returned HTTP 401. The commander submission count remained 1065 across these checks: no model work was dispatched. The original retention, upload/history/report protections, schedule and registry remain in place. No Spark restart/deployment was needed for local commander storage controls. All requested controls are deployed; no full-suite or broader retention-policy claim.
