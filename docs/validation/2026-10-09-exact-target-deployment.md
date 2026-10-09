# Exact host/model assignment deployment — 2026-10-09

Deployed source `cef3150a03f5e6a78a046e222dc5570aeb91487b` to the existing local macOS commander. This is a local development deployment, not a signed release or full-suite qualification.

Reused unchanged scoped race, browser, static and build checks in [feature validation](2026-10-09-exact-host-model.md). Built the installed candidate using `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go build -trimpath`; build metadata reports the source revision above and `vcs.modified=false`. Both installed commander and remote configurations passed `config validate`.

Authenticated local metadata checks found no queued/running submissions and no running task started during the current daemon instance. Historical unfinished records predate that instance and were preserved. Gracefully stopped the commander, confirmed no open database writer, checkpointed WAL, ran SQLite quick_check and verified the copied database checksum and integrity (schema 53). Backed up the previous binary, configuration, launch agent and quiescent commander state privately:

`/Users/aj_lab/.NexusRouter/bin/backups/20261009T152210Z-cef3150a`

Atomically replaced the canonical binary without rewriting configuration, then restarted the existing commander launch agent. The new instance reports ready (PID 53683). Installed binary SHA-256:

`08ca5c8150a511c8f2124803f215771f6acd27cd95c8d17981850dac35d52008`

A temporary approved browser session verified HTTP 200 for `/app/chats`, `/app/api/v1/model-targets`, and the host-mentions/app scripts. The catalogue has version 1, a hostname and valid configured model identities; served script hashes match reviewed source. Collaboration page/API/script and active settings also passed, with collaboration enabled and no restart required. Logged out the verification session afterward. No task submission or model inference was performed.

Central logging retained PID 1188. The already stopped local remote launch agent remains unloaded; its shared executable is updated for its next authorized start. No remote host was contacted or deployed. Exact remote model-name assignment requires peers advertising targeting version 1; paired remote hosts still need this update. Existing browser tabs should reload and may require renewed terminal approval. Physical remote/model behavior and broader release/MVP qualification remain unverified; Linear requirements retrieval remains unavailable pending reauthentication. Private backups, credentials and state contents are excluded from Git. Receipt validation: reviewed documentation diff and `git diff --check`.
