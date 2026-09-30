# CLI-managed daemon lifecycle

With an explicit configuration and `NEXUS_API_TOKEN` already set to at least
32 characters:

```sh
./bin/nexus daemon start --config config.yaml
./bin/nexus daemon status --config config.yaml
./bin/nexus daemon stop --config config.yaml
```

Control requires a literal loopback address or `localhost` and a fixed nonzero
port in `daemon.listen`. The same configuration/environment must identify the
endpoint and token. Commands return versioned JSON containing `instance_id`,
`state` and `started_at`; they never print the token. An unavailable endpoint or
failed authentication is an error, not evidence that no daemon exists.

## Start

On macOS/Linux, start launches the same executable in a detached session with an
absolute configuration path, inherited working directory/environment, and private
random instance identity. Credentials remain in the existing environment, not
arguments. Child standard input/output/error go to the null device; use
`nexus serve --config config.yaml` in the foreground for startup diagnostics.

Any existing listener at the configured address causes refusal. Socket binding
is the authoritative race-safe duplicate gate and occurs before database opening
or migration. Successful start requires an authenticated response carrying the
new launch's exact identity and ready state; a different listener cannot satisfy
the handshake. Readiness means the HTTP service and dispatcher are available,
not that every model/provider has been qualified.

Startup has a ten-second observation deadline. A failed/canceled launch receives
SIGTERM through its owned process handle, then a two-second graceful window,
then a hard kill and reap of that same child if necessary. There are no PID files,
arbitrary PID signals or shell commands. Successful startup survives the invoking
CLI's exit or context cancellation. Existing queued submissions can execute when
the dispatcher starts; this command is not a dry run.

## Status and stop

Authenticated GET `/v1/daemon/status` exposes only instance metadata. A dispatcher
health failure reports `degraded` while retaining identity, so the operator can
still stop it. The CLI reads the current identity and sends exactly one POST
`/v1/daemon/stop` with `{ "instance_id": "..." }`. A replaced instance rejects
the stale request with a conflict. The control invokes only its own cooperative
shutdown callback and is idempotent for that instance.

`stopping` acknowledges a shutdown request, not confirmed process termination.
Existing server shutdown cancels/join tasks and drains HTTP handlers; cancellation
does not undo tool effects. A lost acknowledgement is uncertain and does not
authorize the client to signal a PID or automatically repeat the POST. Inspect
status before deciding a subsequent action.

Control requests use the owned loopback-pinned transport, no proxies or redirects,
bounded deadlines and strict response schemas. API requests retain authentication,
browser-origin denial, control capacity and bounded body parsing. Status/stop do
not create or migrate a database.

## Qualification and remaining work

Process tests build the actual executable, launch a background daemon, verify
identity, reject a duplicate, stop it and restart with a new identity. Other tests
cover unrelated listeners, wrong launch identities, early exits, startup deadlines,
owned-child graceful/hard cleanup, stale stop identities, degraded control, malformed
messages, cancellation, read bounds, and duplicate-bind non-creation of storage.

An additional process test launches the production `serve` command, submits durable
model work, and kills that exact daemon with SIGKILL after a partial provider stream.
After advancing only the owned fixture's claim expiry, a fresh daemon process opens
the same database, becomes ready, and lets the normal dispatcher recovery sweep
close the task and submission as failed. The test requires one immutable recovery
receipt, no second provider call, no partial assistant message, preserved user
context, and a clean cooperative shutdown of the restarted daemon.

This is not an OS login service or automatic crash-restart supervisor. Duplicate
prevention is per endpoint, not a machine-wide database/resource lock. Arbitrary
in-process tools may ignore cancellation; a stopping acknowledgement does not prove
they have exited. Descendant containment, launchd/systemd integration, automatic
restart, configuration reload and complete release qualification remain separate
work. Crash qualification uses a loopback provider fixture and advances the expired
claim in owned test storage; it is not live-provider, wall-clock lease, power-loss,
filesystem-durability, remote worker, or arbitrary side-effect evidence.
