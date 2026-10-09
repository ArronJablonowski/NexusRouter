# DGX Spark deployment — 2026-10-09

Deployed source `52cfe465c510e0cc1dd4a345c1e6ce2f456eff39` to paired instance `dgx-spark`, hostname `spark-9c8a`. This source differs from the local `cef3150a` deployment only by its documentation receipt; application code is identical.

Reused the unchanged scoped race/browser/static checks in [exact targeting validation](2026-10-09-exact-host-model.md). Cross-built with `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go build -trimpath`. Build metadata identifies the revision above with `vcs.modified=false`. Transferred candidate checksum matched before installation, and the candidate validated the existing runtime configuration on Spark.

Read aggregate maintenance state only: no running tasks, queued/running submissions or unreleased task leases; scheduler disabled. Rechecked before gracefully stopping the existing user service `nexusrouter-remote.service`. Backed up previous executable, private configuration and unit file; confirmed no open task/journal database writer, checkpointed WAL and verified original/copied database checksums and SQLite quick_check. Task schema 53; remote control journal user_version 0. Private backup:

`/home/arron_spark/.local/share/nexusrouter/deployment-backups/20261009T155807Z-52cfe465`

Atomically replaced `/home/arron_spark/.local/bin/nexus` and started the same service. Installed SHA-256:

`45ecb5f6ee41819e5cc1fb0887e94e7ae5d3b1df9ef55c3b3006a42e7891fde0`

The user service is active. Paired SSH/mTLS `remote info` reports instance `dgx-spark`, hostname `spark-9c8a`, available=true, targeting_version=1, conversation_version=1 and 34 advertised models. An approved temporary browser session also verified the commander's actual `/app/api/v1/remote-inspection` path returns that available instance and exact-targeting capability. Logged out that session afterward. Local commander, central logging and previously stopped Mac remote-service states remain unchanged. Spark's dedicated Ollama process retained its PID; runtime configuration checksum is unchanged.

No task was submitted, model inference performed, model service restarted, trust changed or private prompt/result/log contents inspected. Private backups and credentials are outside Git. Native Linux inference, exact assignment end-to-end execution and physical failure/recovery qualification remain untested; this does not establish a full-suite, signed-release or MVP pass. Documentation receipt validation: reviewed staged diff and `git diff --check`.
