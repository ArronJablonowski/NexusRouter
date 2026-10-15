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

Commander approval source 407688d0 was installed with private rollback backup at an idle boundary. Actual installed manual Allow returned HTTP 200 in about 19 ms, created the exact file once, survived proposal reload, and completed an unmentioned follow-up. Native committed provenance independently confirms Qwen 3.5 9B / mini-ollama for the follow-up. The initial browser observer entered its draft before mention initialization; no submission occurred until explicit local model resolution was confirmed. Crash/restart during a pending proposal, universal model follow-up coverage and universal remote conversation coverage are not claimed; the bounded tested continuation is documented below. No full make check or complete MVP claim. Linear requirements could not be refreshed because its connection requires reauthentication.


## Resumable paired Spark conversations

The commander now retains immutable private conversation snapshots separately from prompt-free route bindings and original-download scopes. A continuation supplies only a new user turn and original parent request ID; the server reconstructs prior provider messages after checking the bound destination, original caller certificate, exact task digest, successful original result/task identity, unchanged private zero-cost model/bounds, and remote conversation capability. Queued, failed, canceled, unknown or rotated-caller parents cannot authorize continuation. Each new turn uses a new request ID; original requests are never redispatched automatically. Changed or omitted original attachments/parent cannot reuse a bound ID. Messages/transport/storage limits and receiver context/memory admission remain in force, with no silent truncation or model fallback.

Status renders bounded user/assistant history with pinned model and host, plus immutable downloads from every retained turn. Saved request reload recovers history without replay. Bare host dropdown assignments also pin the observed concrete model on targeting-capable peers. Older jobs that predate snapshots remain inspectable but do not acquire reconstructed history. Tool/harness conversations are not supported by this direct private continuation feature. Context exhaustion requires a new conversation; snapshots are bounded and no retention/cleanup policy was added.

A real private candidate-to-Spark Qwen 3.8 27B conversation passed: scanned PDF, genuine legacy DOC and PNG in the first turn; full-page reload; plain follow-up with a new text attachment; correct earlier document references, earlier image facts and new attachment fact; four actual browser downloads byte-equal to originals; another reload; desktop/narrow layout checks. Exactly two dispatches, independently verified as spark-ollama / qwen3.8:27b. QA observer selectors initially counted a progress heading and a same-document hash navigation did not reload scripts; those observer issues were corrected and the original known-success request was inspected, never resubmitted.

Full remote/webuiapp/chatfiles race boundaries passed (59.119s / 8.684s / 1.719s). New mTLS integration tests exercise queued-parent rejection, exact model/host binding, retained prior/new uploads, reopening evidence, omitted-original rejection and rotated-caller rejection. Full Web UI race checks include plain continuation payload/no-double-send and existing recovery/CSRF/cancellation boundaries.

## Cloud model configuration UI

Unconfigured catalog cloud models can be registered using an existing configured model on the same provider connection and a bounded conversation limit. The server rechecks catalog identity and template/provider binding, compares the exact configuration digest, validates the complete proposed configuration and atomically publishes it. Existing defaults/providers/credentials remain unchanged. The model-use deny entry is committed before the model configuration: an interruption can leave an unused denial, never an enabled unverified addition. The new entry declares chat only, is disabled, and requires an idle restart before separate enablement. Unsupported bridge models fail validation. No inference, credential test, installation or automatic restart is performed by Configure.

The form explicitly describes inherited routing cost as an estimate and access/billing as unverified. It does not ask the user to enter observed cost. Fresh catalog presence is not entitlement. Providers without an existing compatible cloud template remain a manual configuration case. The UI does not manage credentials, arbitrary provider connections, live model registration or pending-configuration migration.

Full config/webuiapp race suites passed (17.159s / 9.586s); scoped CLI serve/model/config/web/approval race checks passed (4.083s). Tests cover stale digest, duplicate ID, excessive context, configuration symlink, unchanged unrelated settings/defaults, disabled-first publication, CSRF/authority, no automatic retry, browser explicit-save/no-double-save and narrow layout. A private real catalog registration saved disabled with unrelated settings unchanged and no inference. Its first layout check reproduced form grid overflow and collapsed visibility defects; corrected grid spanning and explicit hidden styling pass the final browser gate.

Final full Web UI race suite, source checker, scoped vet, and native/Linux ARM64 builds passed. Earlier failed browser fixture used incomplete HTTP response stubs; corrected it to exercise the real bounded response reader. No full make check claim. Reviewed final embedded manifest: dfba044d20de4a8e89b55726a9f42150693ca82465ca9f0759b4a6883effee7a. Source b3085bfe is installed on the commander. Canonical native SHA-256 is 27fafbd1f0b260c6637c46be8bb0f5bf8e38fbb1ea2217d09070e8b77dc235a3; private rollback/state backup is ~/.NexusRouter/bin/backups/20261015T230747Z-b3085bfe. Configuration was not replaced. Deployment checked idle submissions/resource leases/task timing before stopping and after stop, backed up and integrity-checked storage, and atomically replaced the binary.

The final real-catalog candidate registration passed at 1440 px and 390 px. Independent configuration comparison confirmed unchanged defaults/providers/other models and a disabled policy entry. After an owned idle candidate restart, actual browser inventory showed the new entry configured=true, enabled=false and usable=false. No model inference was performed for registration. This private test did not change production cloud configuration.

Installed commander-to-Spark verification then passed the same two-turn scanned-PDF/legacy-DOC/PNG plus new-text-file case, four byte-exact browser downloads, full reload recovery, model/host labels and desktop/narrow pinned composer checks. Exactly two new dispatches received confirmed responses. Independent Spark native events show qwen3.8:27b / spark-ollama for both successful turns. Spark receiver PID 1422367 remained active with zero restarts; no receiver/model-service update was needed. Browser sessions logged out; the owned idle candidate stopped and its temporary API token was removed.

Both commander starts had an initial browser challenge timeout while the roughly 986 MiB store was still initializing; no chat was dispatched in those attempts. Initialization latency remains a separate limitation. New remote direct conversations recover through their saved request link/control; they are not yet indexed as local sidebar chat sessions. Older requests lack snapshots, tools/harness continuation is excluded, and universal context/model/retention coverage is not claimed.
