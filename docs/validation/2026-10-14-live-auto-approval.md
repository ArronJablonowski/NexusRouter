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

## Approval inspection recovery

Reusing the database connection removed the repeated integrity scans, but live testing still reproduced a transient 503 when returning to a running chat. A later authenticated GET returned the exact pending proposal with 200 while the page retained its stale-read notice. `loadTaskContext` only reloaded approvals when task controls changed; a missed/failed change event left no read recovery. Added a lifecycle-managed read-only approval watcher and one in-flight request guard. Successful reads reconcile existing rows; failures retain observations and back off through the shared watcher. No POST, decision or task dispatch is retried. Expired proposals from failed tests remain unexecuted.

Chrome regression now deliberately fails the initial approvals GET, then exercises recovery, one-click decisions, preserved rows and refresh behavior. Scoped race run of `TestChromeChatRefreshFailureRetainsContent|TestEmbeddedShellHasNoExternalResourcesOrInlineCode` passed (4.103s). Vet/source checks and both builds passed. Broader Web UI race run (32.812s) reported only a Settings test temporary-profile cleanup error (`directory not empty`); existing helper kills/waits for Chrome's parent before removing a profile that child processes can still write. Behavioral assertions passed; no full package pass claimed for that run. Unchanged Settings browser behavior had passed at the earlier checkpoint. New embedded manifest reviewed: `f50bbbf8a3dae564bef75999ce2b82c782544958dc56dcf20a2f873c509ed587`.

## Installed ON/OFF verification and remaining gap

Installed source `21e243e0`, real Chrome and local `qwen3-5-9b`: chat `6SBJGZMZ4BUMDJAEFZ4Y7E3OPE` requested two files sequentially. OFF initially held the first proposal; saving ON released it, created the exact test content and recorded one consumed `auto_approval_setting` decision. Saving OFF held the second proposal with zero decisions and no second file. The service PID stayed **81363 → 81363** throughout the switches. Both saves returned matching active/saved policy with no restart requirement; final switch was OFF. Private exact proposal/result evidence remains outside Git.

The new read recovery displayed the second proposal, but the subsequent manual click entered the existing unknown-outcome barrier; its durable record still had zero decisions after expiry and no file was created. It was not resubmitted. Thus live immediate ON/OFF behavior is verified; the full manual-click path on this roughly 986 MiB production database remains a verification gap, despite the passing deterministic race/browser tests. Small fixtures do not establish production-scale timing behavior. Read recovery is further limited to failed reads or refreshes queued behind another read, so successful completed chats do not continuously rescan this large database. This final adjustment passed scoped race/browser/manifest checks (4.518s), vet/source checks and native/Linux ARM64 builds. Manifest: `9dea05ef2e9a47446a739028c1e46454288cee58bcd7b8939cf77df32c642f01`.

## Final deployment

Source `6d9b0c73` installed at an idle boundary with private binary/config/database backup; the installed binary matches the verified native candidate. Final real Chrome inspection confirmed saved auto-approval OFF, active policy OFF, and the immediate-save help text. The inspection made no configuration changes or tool submissions and logged its owned browser session out. GitHub backup pushed on `codex/chat-files`. The live ON/OFF unchanged-process evidence above applies to the unchanged controller; final UI refinement only limits when failed/missed reads reconcile. The production manual-click completion gap remains open and is not reported as passed.
