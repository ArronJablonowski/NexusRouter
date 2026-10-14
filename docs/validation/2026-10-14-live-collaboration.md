# Real Spark collaboration verification — 2026-10-14

A real three-message exchange used local zero-cost Muse Glimmer and Qwen 3.8 27B on paired Spark. Muse asked 17 × 3 with a unique synthetic challenge; Qwen read it and replied 51 with the challenge; Muse read and acknowledged that reply. No message was resent.

Verified **3/3 source messages = 3/3 commander imports = 3/3 actual authenticated Chrome rows**, without duplicates. Compared exact text, sender model, producing host, original timestamps and source sequence mapping. Displayed model names were `muse-glimmer:30b-q4_K_M-dflash` and `qwen3.8:27b`; producing hostname `spark-9c8a`, origin instance `dgx-spark`. Desktop and mobile screenshots and private comparison evidence remain outside Git in the local live-collaboration resource directory.

Topic: `live-collab-20261014-12dd9115`.

## Discovered defect and correction

The real browser adapter rejected direct navigation/reload of a topic-filtered collaboration URL, although form filtering and history updates worked. Added a narrow GET/HEAD shell-route exception accepting only one validated topic, retaining session authorization and rejecting malformed, duplicate or unrelated query fields. No browser assets changed.

Validation: full `go test -race ./internal/webuiapp -count=1`, `go vet ./internal/webuiapp`, `go run ./cmd/check` and `git diff --check` passed on the corrected source. This is scoped validation, not a full-suite pass. Native `go build -trimpath ... ./cmd/nexus` passed; application source committed and pushed as `eecf006d` before installation.

## Limits

Qwen published its reply durably, then its final conversational confirmation exceeded the test's 4096-token context and exited with an error. Delivery was confirmed at the source before proceeding; it was never retried. Muse's final acknowledgement used 8192 tokens and completed successfully. Therefore three messages succeeded, but not all task processes exited successfully. This test does not establish every possible delegation, failure, or power-loss scenario. Existing broader delegation baseline failures and Linear reauthentication gap remain documented.

## Deployment and final live verification

Installed the clean native build of `eecf006d` on the commander at an idle boundary, retaining a private executable/configuration/state rollback backup. Spark router/model services were not restarted for this browser-only change. Startup initially took longer than the browser probe timeout; a later read-only readiness check returned the expected unauthenticated 401 before a fresh authenticated verification session was created. No inference or uncertain delivery was retried.

Actual Chrome direct topic navigation and reload passed after deployment. Recompared all three Spark source records against a fresh commander API response and live DOM: **3/3 exact, no duplicates, Spark synced**. Original messages survived commander restart. Desktop/mobile rendering and mobile overflow check passed. Temporary verification sessions were logged out and owned browser processes removed. Private artifacts: `/Users/aj_lab/.NexusRouter/resources/live-collaboration-2026-10-14`; rollback: `/Users/aj_lab/.NexusRouter/bin/backups/20261014T152006Z-eecf006d`.
