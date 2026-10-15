# Chat reliability qualification — 2026-10-15

## Confirmed defects and changes

Manual approval reads and decisions reopened the database and repeated full integrity scans within the browser mutation time budget. The commander database is approximately 986 MiB. Service approval inspection, listing, execution evidence, proposal snapshots and decisions now borrow the dispatcher’s validated store. Every borrow retains file/schema/journal identity checks and closed/replaced handles fail closed. Standalone controls retain their existing integrity validation and cannot create or migrate storage. Atomic task, revision, scope and single-consumption checks remain in place.

A second, reproduced browser race hid a late proposal: a successful empty approval read after tool start cleared the refresh flag, and no later event necessarily triggered another read. Running chats now continue bounded read-only approval observation. There is no automatic decision or mutation retry. A deterministic Chrome regression failed before the change and passes afterward.

The receipt-loss recovery fixture now installs its schema before the dispatcher pins store identity and toggles fault data afterward. It still proves one file effect, one durable approval and recovery of the original operation after receipt failure.

## Validation

Source state: f31104cb plus the approval-store and late-proposal changes in this checkpoint. Private synthetic evidence is outside Git in chat-reliability-2026-10-15. No production database contents were copied into the synthetic candidate.

- Scoped application/CLI race regressions (approval, browser chat/model/resume and active storage): PASS, 41.931s / 16.934s.
- Full approvals, webuiapp and Web UI race boundaries: PASS, 1.389s / 9.974s / 36.840s before the final browser-only correction.
- Complete Web UI race suite after correction: PASS, 34.424s; includes the late-proposal regression, shell/accessibility/interaction coverage and reviewed embedded assets.
- Telemetry approval/store-identity/existing-control race boundaries: PASS, 118.665s.
- Source formatting/size checker, scoped vet and native/Linux ARM64 builds: PASS.
- Reviewed embedded manifest: 50e56aac74419158493097e07a2d3058fce7e569deb7e22308ac73f985adc8a3.

A running isolated candidate with an approximately 983 MiB synthetic database and a real local Qwen 3.5 9B request passed pending-proposal reload, exact file preview, one manual Allow click (confirmed HTTP response around 5 ms), one exact file effect and a plain follow-up. Saved native provenance confirms the follow-up retained Qwen 3.5 9B through mini-ollama. Separate real Deny and Cancel cases reached terminal states without file effects. Deny recorded one denied decision; Cancel did not grant an approval. The denial observer initially waited for an ineligible continuation; native evidence independently verified denial and terminal state before the observer was stopped.

Earlier fixture failures are retained privately: missing synthetic output root was rejected before inference; a subsequent pending proposal expired during the reproduced observation race. Neither uncertain delivery nor an expired request was replayed. Auto-approval stayed off.

## Remaining scope

Installed commander verification and deployment are pending this source checkpoint. Crash/restart during a pending proposal, universal model follow-up coverage and Spark remote conversation continuation are not claimed. No full make check or complete MVP claim. Linear requirements could not be refreshed because its connection requires reauthentication.
