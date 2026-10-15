# Live automatic tool approval

Saving the automatic approval switch applies to the running commander without restarting it. A supported browser tool proposal already awaiting approval is reconsidered within the existing 100 ms polling interval. A successful OFF save waits for any in-flight durable automatic decision; it prevents new automatic decisions but does not revoke decisions already recorded. The normal gate still validates task liveness, expiry, exact proposal, configured permissions, lease ownership and one-use consumption.

Only the automatic approval field is live. Other settings still require restart. Invalid/stale/failed config saves never activate the requested live value. Loading Settings does not silently apply external file edits. Enabled auto-approval persists across normal restarts through the existing config; startup OFF remains the default.

## Verification before installation

- Real application integration under race detection: OFF holds the first `create_file`; switching ON completes it with one consumed `auto_approval_setting` decision; switching OFF holds the second file in the same model loop; an exact manual decision completes it with a distinct actor. Cancellation unblocks the live presenter and never creates a file.
- All config, toolgate, approvals, and Web UI race suites passed. The Web UI suite includes real Chrome interaction, reload, narrow-layout checks, CSRF/approval behavior and reviewed embedded manifest.
- Focused CLI race suite passed: `go test -race ./internal/cli -run 'TestBrowser|TestServe|TestSettingsMenu|TestCLIApproval|TestApprovalExecution' -count=1` (12.359s).
- Source formatting/size check, scoped vet, native build and Linux ARM64 build passed.
- Broader CLI fixture gaps: daemon subprocess helper omits HOME required by existing audit startup; lifecycle browser helper assumes <=64 KiB HTML but HEAD before this change already contains 72,445 bytes. Isolated lifecycle reproduction confirms its response bound. No full-suite pass claimed.

Live installation, unchanged-process ON/OFF testing, and final switch state will be recorded after deployment. Private chat/session data and generated verification files remain outside Git.

## Initial live findings and corrective checkpoint

The first installed test proved ON unblocks an existing proposal and records consumed automatic authority, with OFF immediately reflected in active settings. Its follow-up was not admitted: the browser resumes with its default coordinator rather than the explicitly selected local model; the existing local-history privacy check rejected admission. This is a separate tracked follow-up routing gap; privacy/model settings were not changed.

Two-call live runs then proved OFF leaves the next proposal pending without a decision, but approval/history reads returned HTTP 503 while waiting, preventing the manual button test before expiry. The new controller had opened the approval database inside every 100 ms poll; `OpenApprovalControl` runs `PRAGMA quick_check` across the database on opening. This repeated integrity work is inappropriate for the much larger production database. The correction opens/validates once per pending proposal and closes when it completes or is canceled. Small fixture tests alone did not reveal this scale issue. Expired proposals are preserved and never replayed.

Corrective source passed the focused CLI race selection again (12.355s), scoped vet/source checks and native/Linux ARM64 builds. Unchanged configuration, gate, approval and Web UI results above remain applicable. Live verification of the correction follows installation.
