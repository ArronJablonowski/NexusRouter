# Immediate cleanup schedule controls — 2026-10-16

Added explicit saved automatic-cleanup on/off, daily/weekly frequency, weekday and time controls in Settings. Local commander timezone is shown. The authenticated status API and CSRF/origin-protected update API bind to a reviewed installed helper, fixed LaunchAgent/executable/log paths, bounded settings and expected state digest. No browser-provided command/path/label is accepted. Only the cleanup job is reloaded; manual cleanup and NexusRouter's own process remain independent.

Helper SHA-256: `69048e72315f8a1dab6a10d51c47aeedda9e70b94028518013d0bf047e385159`.
Native binary SHA-256: `c7b2289c0802e462da1d7f56b10d5cdb74771ae5a17cd8450fe2cfb1dd23f7b8`.
Reviewed Web UI manifest: `1e98edf09aba89c2aeaf9fd22f8fd5428059de056da875f5dd142517feed97bc`.

Verification before deployment:

- System Python 3.9 and Homebrew Python 3.14 safety suites: 37 passed. Existing retention/upload/report/open-file protections remain covered. New cases include daily/weekly/on/off transitions, stale digests, active cleanup/lock contention, invalid fields/plists/symlinks, OS failures, successful rollback, failed rollback, explicit crash recovery, externally disabled overrides and modern scheduler output words.
- Full affected race suites: `go test -count=1 -race -p 1 ./internal/maintenance ./scripts ./internal/webuiapp ./webui` passed (4.094s / 9.302s / 7.677s / 86.079s).
- A separate, owned native macOS QA LaunchAgent passed real status, disable/unload, daily/weekly editing and enable/reload. Its schedule was set away from the current time; an inert marker confirmed no cleanup execution. The job was unloaded and its temporary files removed. Production cleanup scheduling was untouched. A disabled-override entry for the owned QA label is reset to enabled; no unrelated override/domain was reset.
- Native qualification exposed launchctl returning `enabled`/`disabled` instead of boolean words. Added support and a dedicated regression, retaining fail-closed handling for unknown output. Initial Chrome reload test observed a navigation race; clearing the old time value before reload makes the test wait for the new document rather than stale DOM state.
- The owned subprocess-tree cancellation regression passed; a canceled helper cannot leave its test child running and writing later. Transaction intent survives interrupted updates for explicit recovery.
- HTTP tests cover unauthorized reads, missing CSRF/origin, invalid/missing enabled/time fields, bounds, unknown fields, status/query validation, stale saves, and a bounded update surviving browser disconnect. No schedule operation addresses the commander service.
- Chrome and Firefox controls passed off/on, weekly save, reload persistence, 1440/390/320 layouts, conflict/failure draft preservation, explicit recovery messaging and no request replay. Final Firefox combined manual-cleanup/schedule check passed 5.556s.
- Scoped CLI serve/browser-mount race tests passed 1.926s. Source formatting/size checks, scoped vet, `git diff --check`, manifest review, native and Linux ARM64 builds passed. Unsupported Linux schedule execution remains rejected. These are affected checks, not full `make check`.

Recovery policy: persist old state before disabling/unloading, atomically publish the new private plist, activate, verify, then clear intent. Failure restores prior file/loaded/disabled state. Incomplete recovery remains visible; a later explicit Save recovers first and does not silently apply a second request. No active cleanup is interrupted. Schedule changes persist without a NexusRouter restart; no OS reboot/clock change/DST/sleep transition or future timer occurrence is forced during qualification. Prior cleanup policy limitations still apply. Linear remains subject to the prior reauthentication gap.

Installed verification follows.

## Installed verification

Source `715ddd6b` was committed/pushed before an idle-boundary commander deployment. The shared cleanup lock covered shutdown, exact old-PID exit, empty/absent WAL verification, private rollback/config/chat-state/utility/cleanup-plist backups and atomic installation of the qualified binary/helper. Existing cleanup policy, registry and schedule were retained during deployment; no Spark or unrelated service was restarted.

Real authenticated Firefox Settings verification passed on the installed commander: automatic cleanup off with the LaunchAgent unloaded/disabled; manual preview while off; saved weekly day/time while off; re-enabled with the weekly calendar active; reload persistence; 1440/390/320px layouts; and restoration of the original enabled daily 03:30 schedule. A separate wrapper independently checked the commander PID stayed unchanged across every control action, the cleanup journal hash remained unchanged (no cleanup execution), the original calendar/active state were restored, and no transaction marker remained. The wrapper also provided original-schedule restoration on test failure. The owned browser session was logged out and temporary profile removed. Desktop and narrow screenshots were visually reviewed; private evidence is outside Git.

Installed unauthenticated schedule GET and POST returned HTTP 401. An initial short HTTP probe during the known large-store startup warm-up timed out; the long-deadline browser verification completed, and the authenticated and unauthenticated checks passed after startup readiness. This remains the existing startup-performance limitation, not a schedule-save restart requirement. Qualified installed binary/helper hashes match this report. Final OS status reports enabled, loaded, ready, daily 03:30 in the commander's local MDT timezone. Disabling/weekday persistence was verified by saved plist and actual launchd state without rebooting or waiting for a future calendar occurrence.
