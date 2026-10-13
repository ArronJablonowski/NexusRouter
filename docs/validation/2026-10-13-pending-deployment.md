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

Final corrected installation and live synchronization verification are recorded below after completion.
