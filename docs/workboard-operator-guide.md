# Web UI and Workboard operator guide

This guide covers the embedded browser UI and durable Workboard/Kanban surface
in the stock `darwin` daemon. Commands and keys below match configuration
version 1. Start with `examples/local.yaml`, replace placeholder model limits,
and validate before opening an operator database.

## Configure and start

Set a private daemon bearer token in the environment through a trusted secret
source. It must contain at least 32 characters and must not be written into
YAML, shell history, logs, or a browser:

```sh
go run ./cmd/nexus config validate --config examples/local.yaml
go run ./cmd/nexus serve --config examples/local.yaml
```

Use `nexus daemon start --config examples/local.yaml` instead of `serve` for a
detached daemon, and use `nexus daemon status|stop --config ...` for its
authenticated lifecycle. Foreground `serve` is preferable while diagnosing
startup. Readiness proves that the HTTP service and configured supervisors
started; it does not prove model quality or external-provider availability.

The stock daemon accepts only a literal loopback listener or `localhost` with a
fixed nonzero port. `web_ui.path_prefix` must be one path segment of 1–32 safe
characters, such as `/app`; `/v1`, `/health`, nested paths, query strings, and
fragments are rejected. With the sample, open:

```text
http://127.0.0.1:7788/app/
```

Do not expose this HTTP listener through a non-loopback bind or an unreviewed
reverse proxy. TLS termination, forwarded hosts, and remote-browser deployment
are not supported by the stock daemon.

## Specialist routing preferences

The Command grid compares eligible configured models with paired remote models
for each displayed evidence scope. A remote row shows its model name and hostname
in yellow, with **Remote** text so color is not the only cue. Expand the row to
inspect its paired instance, original model ID and evaluation evidence. Unknown
hostnames use a labeled instance fallback. The configured Router Commander stays
an explicit model choice; this preview does not override it.

Unified automatic selection requires paired client/trust configuration and the
remote dispatch/evidence directories. Upgrade participating peers to support
conversation version 1. Supported local providers expose a built-in text-only
route without an external harness; external harness routes still require explicit
permission. Model availability and admission do not establish accuracy. Neither
opening the grid nor refreshing it runs model inference.

Confirmed remote failures before a delivered answer may retry the next model
from the original top three, within remaining budget and attempt/time limits.
Unconfirmed cancellation/delivery, user cancellation or sink/persistence failure
stops automatic failover. Shared filesystem/tools and unsupported conversation
shapes remain outside remote eligibility. See [accuracy-first routing](accuracy-first-routing.md)
for exact preview constraints, retry ownership and remaining qualification gaps.

## Rename and pin chats

Use the three-dot button beside a chat title to choose **Rename** or **Pin**.
Pinned chats appear before unpinned chats, including older chats outside the first
page. Choose **Unpin** to return a chat to its normal recency position. Within
each group, the existing newest-first ordering is preserved. A renamed chat uses
its saved name in both the list and conversation heading.

Names and pins are shared by authenticated browsers connected to this daemon
and survive refresh/restart. Renaming accepts a nonempty name of up to 100
characters. If another browser changes the same chat while a rename is open,
the save reports a conflict and preserves the draft. Close the dialog and refresh
before applying it again. Failed or uncertain writes are never automatically
replayed. Keyboard users can open the menu with Enter/Space, use arrow keys to
move between actions, and press Escape to return focus to the three-dot button.

Include `<telemetry.database>.chat-preferences.json` in backups/restores alongside
the task database. This private file contains the custom titles, pins and edit
revisions; it does not change task history or execution state. Writes use a
shared file lock and atomic replacement with file/directory sync. The file is
bounded to 1 MiB and 1,000 customized chats; missing files mean default names and
no pins, while corrupt files fail closed. This feature requires the updated
daemon and a newly loaded Web UI document; replacing CSS alone does not load
new JavaScript.

## Authorize a browser

An unauthenticated browser receives a short-lived one-time challenge and shows
an approval value in `CHALLENGE_ID.DISPLAY_CODE` form. In a trusted terminal on
the same host, copy the complete value into:

```sh
go run ./cmd/nexus web approve --config examples/local.yaml CHALLENGE_ID.DISPLAY_CODE
```

For an installed macOS service, the browser's `nexus web approve CODE`
command discovers the current user's running `com.nexusrouter.*` (or legacy
`com.darwinrouter.*`) service executing `nexus serve`. It uses that service's
actual configuration path, configuration environment overrides and API token,
including custom paths and paths containing spaces. It does not start or restart
services. If several daemons are running, select one explicitly with
`nexus web approve --service com.nexusrouter.commander CODE`, or `--config`.
Service discovery does not inspect archives or task databases and never prints
the token or raw service environment.

On every platform, `--config` overrides `NEXUS_CONFIG` (legacy `DARWIN_CONFIG`
is also accepted). With an explicit configuration and `NEXUS_API_TOKEN` in the
trusted terminal environment, no service discovery is needed. Linux/systemd and
other service managers require that explicit environment setup; automatic
credential retrieval is currently macOS-only. Without an explicit path or a
running macOS match, approval checks the standard `~/.NexusRouter/config`
configuration files, the OS user configuration directories for `nexusrouter`
and `darwinrouter`, and legacy `live-test` locations. Multiple eligible files
require explicit selection. An invalid explicit configuration never falls back
to another daemon. Example for a custom installation:

```sh
export NEXUS_CONFIG="/absolute/private/location/router.yaml"
# Supply NEXUS_API_TOKEN through your trusted secret-management setup.
nexus web approve CHALLENGE_ID.DISPLAY_CODE
```

Verify the browser and terminal display the same value before approving. The
command uses `NEXUS_API_TOKEN` to contact the loopback daemon; the token never
enters the browser. A successful challenge creates an HttpOnly,
SameSite=Strict browser cookie. Browser sessions expire according to
`web_ui.browser_session_ttl` (5 minutes through 24 hours), are process-local,
and require a new challenge after daemon restart. Durable board and operation
records survive restart; browser authority does not.

An empty `web_ui.allowed_origins` derives the origin from `daemon.listen`. Each
configured additional origin must be an exact `http` loopback origin using the
same port, with no credentials, path, query, or fragment. It adds a validated
Host/origin spelling; it does not enable CORS or remote access. Mutations require
the exact Origin, same-origin Fetch Metadata, the session cookie, and the
session's CSRF value. Forwarding headers and bearer tokens on browser routes are
rejected.

The shell sends no CDN, analytics, remote font, service-worker, or browser-
storage traffic. Its fixed CSP allows same-origin scripts, styles, fonts,
connections, forms, and manifests, plus same-origin or data-URL images; it
disables objects, frames, workers, foreign framing, and base-URL rewriting.
No manifest is currently loaded. This policy is not configurable.

## Workboard lifecycle

Boards are durable and either active or archived. Archival makes a board
read-only and is refused while it has active claims. Cards follow the durable
lifecycle below:

```text
Backlog -> Ready -> In progress -> Review -> Done
              |          |           |
              |          +-> Blocked +-> Ready after rejection
              +--------------------------> Canceled (proof-gated finalization)
```

- Backlog is not dispatchable. Dependencies must be satisfied before a card
  can remain Ready.
- A scheduler atomically claims a Ready card and binds an attempt, worker,
  task, resource reservation, lease, and configured policy.
- Pause and cancellation controls record requests. “Pause requested” is not a
  pause acknowledgement; a worker acknowledges only at a safe boundary.
- A worker candidate enters Review with immutable criterion-bound evidence.
  Required deterministic failure rejects it to Ready. All required objective
  criteria passing can accept it to Done. Required subjective criteria remain
  in Review until authenticated user feedback decides them. Advisory model
  audit cannot accept or reject by itself.
- Acceptance unblocks dependent cards transactionally. Rejection creates no
  automatic retry authority; normal eligibility and attempt budgets apply.
- Expired leases and missing owners are attention signals. Supervisors re-read
  durable facts; an attention label alone never authorizes redispatch.

Every browser mutation uses an idempotency key plus exact board, layout, graph,
card, claim, criteria, or evidence revisions as applicable. A stale revision is
a definitive rejection followed by authoritative refresh. A lost or ambiguous
acknowledgement is reconciled through the operation journal and is never
automatically resubmitted.

### Agent decomposition limits and audit evidence

Model-authored parent/child placement uses this versioned configuration:

```yaml
workboard:
  decomposition:
    version: 1
    max_depth: 4
    max_children_per_parent: 8
```

Depth counts hierarchy nodes, so a top-level card is depth 1. The child limit is
the number of direct children of one parent; dependency edges use separate graph
limits. Both settings accept 1–64. The defaults are 4 and 8. They are visible in
the redacted effective configuration and contribute to its digest.

These values are host policy, not model input. The root create-card schema
rejects `decomposition`, configuration/policy digests, and admission identifiers.
Before mutation, the application binds the exact effective policy and config
digest. SQLite then checks authoritative depth and direct-child count inside the
single-writer transaction, before card allocation, revision movement, operation
receipt, or event insertion. Concurrent child proposals therefore cannot exceed
the same parent allowance.

A successful model-authored placement creates an immutable decomposition
admission. Workboard event inspection, including the Web UI activity projection,
shows the admission reference, configuration and policy digests, effective
limits, resulting depth, and direct-child count. It does not expose prompts,
card content, raw tool arguments, credentials, or idempotency keys. Use these
fields to audit which effective policy admitted a card.

An exact retry under the same policy returns the original committed result. If
configuration changes, reuse of the old idempotency key conflicts because its
request remains bound to the original config digest. Refresh the board and use a
fresh operation key to request evaluation under the new policy; NexusRouter
does not reinterpret the earlier admission. Delegated workers and Workboard
execution children receive no Workboard write tools or execution authority. A
parent `ask` policy cannot weaken that denial.

## Browser operations and approvals

The Web UI supports board create/edit/archive; card create/edit; Backlog/Ready
movement; same-lane ordering; dependency changes; pause/resume/cancel requests;
candidate inspection; and evidence-based acceptance/rejection when the current
state authorizes them. It also exposes bounded task chats, steering,
cancellation, subjective feedback, pending tool approvals, model/route/resource
inspection, and sanitized tool/audit lifecycle metadata.

Browser connection approval and model tool approval are separate authorities.
Connecting a browser does not pre-approve tools. Side-effecting tool requests
are exact, one-use grants bound to the tool, arguments, resource scope, actor,
and request digest. Review the displayed scope and warnings. Denial or revocation
does not itself undo an effect. A committed or uncertain effect is never
automatically retried, even when its operation is otherwise idempotent.

The interactive `nexus chat` command can present approval prompts in a trusted
terminal. Headless queued submissions do not gain a hidden approval presenter.
Do not enable agent write tools until an operator will actively supervise that
interactive path.

## Agent-facing Workboard tools

The following configuration gates are independent of browser access:

```yaml
tools:
  enabled: false                 # read_file; unrelated to Workboard reads
  workboard_read_enabled: true   # workboard_list, workboard_read
  workboard_write_enabled: false # requires workboard_read_enabled
  max_turns: 8
security:
  default_tool_policy: ask
```

`workboard_list` reads at most 100 boards per page. `workboard_read` reads at
most 100 cards per page and accepts bounded state, assignee, claim-owner, and
claim-state filters. Both return browser-safe/local projections rather than raw
prompts, provider responses, tool arguments, or tool results.

When `workboard_write_enabled` is true, a local root model may propose these
approval-backed tools:

- `workboard_create_board`, `workboard_revise_board`,
  `workboard_archive_board`;
- `workboard_create_card`, `workboard_update_card`,
  `workboard_transition_card`, `workboard_reorder_card`;
- `workboard_add_dependency`, `workboard_remove_dependency`;
- `workboard_request_pause`, `workboard_request_resume`,
  `workboard_request_cancel`;
- `workboard_propose_criteria`,
  `workboard_request_candidate_decision`.

Writes are confined to `workboards` for board creation and
`workboard:<board_id>` for all per-board actions. Child workers do not inherit
these root mutation tools and cannot invoke them by emitting an unadvertised
borrowed-registry tool name. Agent transition tools intentionally move cards only
between Backlog and Ready; claim, heartbeat, safe-boundary acknowledgement,
candidate submission, cancellation finalization, and recovery remain trusted
host lifecycle operations. Criteria and candidate-decision tools are advisory
two-party proposals: the model freezes the exact revisions and digests, while
only the authenticated operator approval can apply the resulting mutation or
become the acceptance actor. Every write is schema-closed, revision-fenced,
single-writer scoped, and approval-backed. Proposal arguments are canonicalized
before both journaling and authorization so the durable approval digest can be
reproduced after restart; duplicate JSON members are rejected.

## Optional unattended scheduling

`workboard.scheduler.enabled` defaults to false. Enabling it causes the daemon
to discover and dispatch eligible Ready cards without waiting for a browser
click. Configuration validation requires:

- `worker_model` naming a mode-compatible model with `chat` capability,
  positive context size, an explicit nonnegative cost estimate, and an Ollama
  or OpenAI-compatible provider that enforces the output-token ceiling;
- `acceptance_judge.enabled: true`, global
  `evaluation.llm_judge_enabled: true`, and a different configured local model
  with `audit` capability;
- positive `max_cost`, `max_input_tokens`, and `max_output_tokens`, a timeout
  from 100 ms through 5 minutes, and a total reviewer reservation within its
  context window;
- `max_active_claims` no greater than `workers.max_in_process`.

The reviewer is tool-free and advisory. Deterministic validators and explicit
user feedback remain authoritative. The initial unattended worker path is
effect-free; uncertain side effects are not scheduler retry authority. Observe
daemon health and the Kanban attention state after enabling this feature.

## Backup, restore, and rollback

The SQLite database is the durable source for tasks, sessions, Workboards,
attempts, evidence, browser operation reconciliation, fitness, memory, and
skill learning/selection state. Skill version bodies under a configured
`skills.root` are separate files and require a coordinated backup with their
database state. Browser cookies are deliberately absent. A database-file copy
taken from a running WAL writer is not a qualified backup.

1. Stop the daemon and every other process that can write this database. Treat
   an unavailable status response as uncertainty, not proof that no writer is
   running.
2. With trusted SQLite tooling, run `PRAGMA wal_checkpoint(TRUNCATE);` followed
   by `PRAGMA quick_check;` and require the single result `ok`.
3. Copy the quiescent database to a newly created, access-restricted backup;
   retain its SHA-256, schema version (`PRAGMA user_version`), NexusRouter
   binary version/commit, configuration digest, time, and operator identity.
4. Test restoration by copying the immutable backup to a different path,
   verifying its digest before opening it, and starting the matching binary
   against that disposable path. Never test by overwriting the only backup.

For rollback, treat the binary, configuration, and matching pre-upgrade database
backup as one reviewed unit. Older binaries must not open a database already
migrated by a newer schema. Stop all writers, preserve the failed/current files,
restore the verified backup to a new path, point the matching prior configuration
at it, validate, and start exactly one writer. Rollback discards all records
committed after the backup; record and approve that loss explicitly.

`nexus memory export` is not a database backup. Copying only the SQLite main
file while WAL writers are active is not a database backup. The repository's
installation/migration rehearsal and release rollback documents add release-
artifact evidence requirements; they do not perform an operator's production
backup automatically.

## Explicit limits

- Stock HTTP service: loopback only; no built-in TLS, remote bind, reverse proxy,
  multi-host cluster, or cross-origin browser deployment.
- Browser support and manual qualification are recorded in
  [Web UI and Kanban qualification](webui-qualification.md) and
  [Web UI accessibility](web-ui-accessibility.md).
- Browser projections are bounded and sanitized. They are not raw database or
  telemetry viewers and cannot be used to reconstruct hidden prompts/tool data.
- The Kanban is not distributed consensus. A single SQLite/WAL database and its
  compare-and-swap records coordinate cooperating processes on one host.
- In-process workers are bounded but not sandboxed. Subprocess, worktree,
  container, SSH, and remote worker backends are not part of v1.
- Provider fixtures and passing health checks do not prove model correctness,
  cost accuracy, thermal stability, or external availability.
- Fully local mode enforces an egress-deny transport policy for NexusRouter's
  configured transports, but cannot firewall arbitrary third-party processes or
  untrusted extension code outside the runtime.
- Automatic model disabling/removal, destructive tool actions, policy changes,
  criteria changes, and subjective acceptance require operator authority.

For detailed daemon behavior, see [CLI-managed daemon lifecycle](daemon-control.md).
For release-grade migration evidence, see
[Installation, migration and rollback rehearsal](install-migration-rehearsal.md).
