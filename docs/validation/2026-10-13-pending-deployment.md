# Pending feature deployment — 2026-10-13

The user authorized deployment of all pending ComfyUI and collaboration changes. Source checkpoint `67bfe9fa` includes the verified feature changes and is backed up on GitHub. Reused its unchanged scoped checks, built clean trimpath native and Linux ARM64 executables, and validated both candidate configurations before installation. This is a development deployment, not full-suite or MVP qualification.

## Preconditions and backups

Both hosts had zero queued/running submissions. Spark had no running task heads or unreleased leases; ComfyUI queue was empty. The commander had five historical running task records dated September and one expired September benchmark lease, with no recent running task or live lease. Those historical records were not deleted or replayed. Idle state was checked again after stopping each router before replacement.

Retained previous binaries, configurations and service definitions in private backups. Copied router state while stopped and checked copied SQLite databases with `quick_check`. Used SQLite backup for shared local configuration usage databases while retaining the existing central logging process. Private backup paths:

- Commander: `/Users/aj_lab/.NexusRouter/bin/backups/20261013T191738Z-67bfe9fa`
- Spark: `/home/arron_spark/.local/share/nexusrouter/deployment-backups/20261013T191622Z-67bfe9fa`

No private state, credentials or backup contents are committed. Existing trust files and model services are preserved.

## Activation and startup diagnosis

Enabled ComfyUI with private receipt directories and enabled collaboration capture on Spark; the commander already had collaboration enabled. Added a loopback SSH forward from the commander to Spark ComfyUI using existing authentication, strict host-key verification, keepalive, and launch-agent automatic reconnect. No unauthenticated ComfyUI port was exposed. The original central logger and stopped local remote-host service were preserved.

The commander started successfully and its real authenticated browser routes served the exact reviewed ComfyUI/collaboration assets. A normal CLI-approved temporary browser session completed an isolated 48×24 image graph through Spark, retrieved its PNG through the authenticated image route, and verified same-key replay without another dispatch. Logout returned its expected empty response; the verification helper initially attempted to parse it as JSON and was corrected.

Spark's first candidate exited with `task admission failed`: its auxiliary SDK client loaded local ComfyUI write capability without an operator approval presenter. The prior working binary/configuration were restored while diagnosing this confirmed failure. The fix gives that read-only history/identity client a ComfyUI-disabled view, retaining the serving application's original settings and the mandatory approval guard. No remote caller acquires ComfyUI write authority. Collaboration provenance was also kept internal rather than adding a new SDK result field.

Regression: the unapproved SDK still rejects enabled ComfyUI; the scoped remote inspection SDK now constructs successfully; the original serving configuration is unchanged. Full race-enabled remote CLI tests passed. Related remote/SDK recovery and storage guards passed; scoped final app/bridge/remote CLI collaboration, commander, remote-execution and ComfyUI gates passed. Vet and source formatting/line-limit checks passed. The preexisting broader regression gaps documented in the collaboration and ComfyUI reports remain unresolved.

## Completed installation and live verification

Final application source `d1a1166943d0259bd42fff6d8da07d7dde15615b` was committed and pushed before rebuilding/installing. Both trimpath artifacts identify this clean revision (`vcs.modified=false`). Installed SHA-256:

- Commander native: `1d81176d306cfcdba63017c578fd88f403fb1006f49e848082fd588f26a08940`
- Spark Linux ARM64: `eccff876a35fc94510353f722943516ef01c3a1c88ed69b99175f12c09026e66`

Final rollback copies and consistent private state backups:

- Commander: `/Users/aj_lab/.NexusRouter/bin/backups/20261013T192806Z-d1a11669`
- Spark: `/home/arron_spark/.local/share/nexusrouter/deployment-backups/20261013T192736Z-d1a11669`

Canonical `nexus` and separate `nexus-comfy` commands were updated on both hosts. Only the existing commander and Spark remote-router processes were restarted. Local remote-host service remains stopped. The central logging process retained PID 1188; Spark Ollama retained PID 2187152 and ComfyUI retained PID 2421. Spark's corrected router is active with zero automatic restarts after this installation. Existing trust and credential files match their original private backups.

Both active configurations enable collaboration and ComfyUI. The commander uses a supervised loopback SSH tunnel on 18188 to Spark's existing loopback 8188; the launch agent uses strict existing host-key trust and automatic reconnect. Verified recovery by terminating only the owned idle tunnel process: its PID changed and queue reads resumed, with no submitted task resent or running model interrupted.

Live paired mTLS info reports `dgx-spark`, hostname `spark-9c8a`, available=true. The commander background collector reports that peer `synced`, with advancing check timestamps. This verifies physical empty-journal collection through the new export endpoint and existing bilateral history grants. Saved collaboration message count remains zero: no real LLM collaboration or synthetic message was fabricated for deployment verification. Nonempty import/provenance/deduplication remains covered by the recorded transport/storage fixtures.

A browser verification request initially timed out while the restarted commander was settling. Nonmutating readiness inspection subsequently returned HTTP 200, then the normal browser challenge/CLI approval/session flow succeeded. Reverified authenticated ComfyUI/collaboration pages, exact source-matching served scripts, enabled collaboration projection, healthy Spark sync status, owned receipt list, a new isolated 48×24 image workflow, PNG retrieval and same-key replay. The test session was explicitly logged out. Original uncertain task requests were never resent; the final smoke workflow was a distinct new test after its earlier predecessor had completed.

Deployment is complete for the pending ComfyUI and collaboration application changes. Existing browser sessions are revoked by restart and may need normal sign-in again. Broader preexisting regression failures, live LLM behavior, historical delegation backfill, heavy diffusion/GPU scheduling, power-loss recovery and signed-release qualification remain outside this deployment's verified scope. Private deployment scripts, logs, receipts, databases, credentials and backups remain outside Git.
