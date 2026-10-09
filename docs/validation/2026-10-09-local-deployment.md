# Local commander deployment — 2026-10-09

Deployed application source `d8a3007a8085e70bdeeebcc2214abcaf30867a22` to the
existing macOS commander service. This is a local development deployment, not a
signed release, full-suite pass or physical model qualification.

## Preconditions and backup

Reused the successful scoped race/browser/static checks documented in the model
collaboration report. Built a new trimpath binary with offline Go dependencies;
Go build metadata identifies `d8a3007a` with `vcs.modified=false`. Both installed
configurations validated. Rechecked the commander through authenticated local
status and content-free task/submission APIs: no queued or admitted work, and the
five unfinished historical task records predate the current process by weeks.
The local remote-host store's maintenance state counts contained no queued or
running work. No task prompts/results or private log contents were inspected.

The commander was gracefully stopped before copying state. Confirmed its task
store had no open writer, performed WAL checkpoint and `quick_check`, and copied
the quiescent commander directory into a newly created private backup. Verified
the copied database checksum and integrity; storage schema is 53. Preserved the
previous binary, configuration and launch-agent file privately. Backup:

`/Users/aj_lab/.NexusRouter/bin/backups/20261009T140348Z-d8a3007a`

Private configuration/state copies and their content checksums remain outside
Git. No backup was overwritten and no historical task was deleted or replayed.

## Installation and verification

Atomically replaced the canonical installed binary and commander configuration;
enabled only `tools.collaboration_enabled`, then registered the existing launch
agent again. The new commander reports `ready` with a new process instance.
Installed binary SHA-256:

`e0439a832340c3e31c99b2f75841094ffb473c9e624000fd9fb208b343cd1ef5`

A temporary verification browser session exercised the real local approval
boundary without printing credentials/cookies. The first bootstrap attempt lacked
required Fetch Metadata and was correctly rejected; supplying the existing
same-origin headers completed the normal authorization flow. Confirmed:

- Authenticated `/app/collaboration`: 200, new shell and script included.
- Served collaboration script: 200, SHA-256 matches reviewed source.
- Collaboration API: 200, enabled, empty journal before any model message.
- Active settings: enabled and `restart_required=false`.
- Verification session explicitly logged out afterward.

Central logging retained its original process. The local remote-host launch agent
was already not loaded and remains stopped; the shared on-disk executable is
updated for its next explicitly requested start. No remote system was contacted
by deployment verification, and no model inference or message task was started.
Browser sessions expire on restart: existing tabs need a reload and terminal
approval if the connection page appears. Cross-host collaboration remains outside
this deployment. Documentation-only receipt verification uses `git diff --check`.
