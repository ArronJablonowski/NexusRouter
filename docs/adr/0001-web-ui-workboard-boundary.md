# ADR 0001: Web UI and workboard boundary

- Status: Accepted
- Date: 2026-09-09
- Linear issue: DAR-76
- Contract version: `webui.v1`
- Refined by: DAR-77 through DAR-80

## Context

DarwinRouter needs a browser chat experience and an integrated Kanban for
long-running work without weakening the daemon's existing bearer-authenticated
API or turning browser presentation state into runtime truth. Existing task
events contain internal model and tool payloads, existing task discovery is
metadata-only, and existing SSE routes mix finite durable replay with a separate
request-coupled provisional stream. None is directly safe as a browser view
model.

The workboard also has a different lifecycle from a model turn. It must survive
restarts, support dependency-aware scheduling, separate execution from
acceptance, and make concurrent mutations converge. It therefore cannot be an
array held by the front end or an extension of `runtime.Event`.

## Decision

### Process and trust boundary

The daemon will embed static, version-pinned Web UI assets and a browser-facing
backend-for-frontend (BFF) under `/app`. The BFF calls the same in-process
application services as the CLI, SDK, and native HTTP handlers; it does not make
HTTP calls back into `/v1`. Existing bearer-authenticated `/v1` behavior remains
unchanged.

Browser clients receive only bounded presentation projections. Raw runtime
events, prompts, provider responses, tool arguments/results, credentials,
approval capabilities, and authoritative task or board state are never placed
in browser storage. The server owns all task, session, approval, board, claim,
and acceptance state.

The checked-in Go types, JSON Schema, and fixtures under `webui/` define
contract version 1. Every mutation carries a 16–128-byte idempotency key.
Revision-sensitive mutations also carry the relevant expected revision and use
compare-and-swap. Unknown versions, actions, fields, malformed identifiers, and
oversized values fail closed.

Identifiers use the conservative ASCII form `[A-Za-z0-9][A-Za-z0-9_-]*` and
are at most 128 bytes. Idempotency keys are 16–128 printable non-space ASCII
bytes, matching submission service semantics. Text limits are UTF-8 byte limits;
the JSON Schema publishes them as `x-maxBytes` in addition to advisory
`maxLength` where useful. Both consumers must enforce `x-maxBytes` before use.

### Browser authentication and transport security

Initial setup uses a one-time challenge. The browser requests a short-lived
challenge cookie and displays a code. An operator approves that code through an
already authenticated CLI/native API operation. Atomic challenge consumption
creates a random browser session and invalidates the challenge. The host bearer
token and bootstrap secret never enter a URL, HTML, JavaScript, local storage,
session storage, IndexedDB, logs, or telemetry.

A fixed, non-sensitive bootstrap document and its versioned local assets are
the presentation portion of the `bootstrap_challenge` exception. An
unauthenticated navigation to the exact app root redirects there; no full shell,
application asset, data projection, or native API becomes public. The bootstrap
document can only create and consume a challenge under the controls below.

The approval operation is a new authenticated native command,
`POST /v1/browser-session/challenges/{challenge}/approve`, exposed by
`darwin web approve --config path/to/config.yaml CHALLENGE_ID.DISPLAY_CODE`.
It accepts only the opaque challenge identifier and
display-code proof, is rate limited, and never returns a browser session or host
token to the CLI.

Strict `Host` validation applies to every configured app-path request, including
static files and bootstrap. The session cookie is `HttpOnly`, scoped to that
path, `SameSite=Strict`, and has an explicit
expiry, and is `Secure` whenever TLS is used. Logout revokes the server-side
session. Normal mutating BFF requests require a session-bound CSRF token delivered in a
non-cookie response field and echoed in a custom header. They also require an
exact allowed `Origin`; absent or mismatched origins fail. Read requests require
a valid session. The two bootstrap exceptions are narrow: challenge creation
requires strict Host, exact Origin, same-origin fetch metadata, body/rate limits,
and creates only a challenge cookie; challenge completion requires all of those
plus that original HttpOnly cookie and an atomically approved challenge. Neither
accepts ambient bearer authentication or a normal session CSRF token.
`Access-Control-Allow-Origin` is never emitted. Forwarded host/protocol headers
are rejected until an explicitly configured trusted-proxy mode exists.

Challenge creation and native approval have bounded global rate windows, wrong
proofs have a per-challenge attempt limit, live challenges/sessions are capped,
and the browser handler has a fixed in-flight limit. Capacity responses are
sanitized and include a bounded retry hint. Challenges and browser sessions are
deliberately process-local in version 1: restarting the daemon revokes them.

The CSRF token returned at session completion is held only in JavaScript memory.
After navigation or refresh, an authenticated exact-origin POST rotates it and
returns a replacement. To support multiple open tabs, a session retains at most
eight current page grants and evicts the oldest; every grant remains random,
session-bound, process-local, and memory-only.
This recovery operation cannot create a session and still requires the
HttpOnly/SameSite session cookie, strict Host, exact Origin, same-origin fetch
metadata, and a closed bounded body.

The BFF sets, at minimum:

```text
Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; frame-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; worker-src 'none'; manifest-src 'self'
Referrer-Policy: no-referrer
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
Cross-Origin-Opener-Policy: same-origin
Cache-Control: no-store
```

Static immutable assets may use content-hashed names and private revalidation,
but HTML, API responses, SSE, challenges, and sessions always use `no-store`.
There are no CDN, remote font, analytics, external image, service-worker, raw
HTML, or implicit link-preview fetches. Markdown is parsed with raw HTML
disabled and links are inert until explicit operator action. Fully local mode
uses the existing egress-deny transport policy for every server-side request.

Loopback is the default bind. Remote browser access requires explicit
configuration and TLS at a trusted deployment boundary; trusted-proxy support
is a separate future decision.

### Streaming and recovery

Browser observation uses authenticated `GET` SSE so native `EventSource` can
reconnect with the session cookie and `Last-Event-ID`. Event IDs are opaque,
bounded cursors. The server sends a bounded suffix from durable state, then
follows new presentation events. A gap outside the retention window yields a
sanitized `cursor_expired` error and forces a bounded snapshot reconciliation.
A cursor from another subject, malformed cursor, or cursor ahead of the durable
head is rejected.

`chat.delta` is explicitly provisional and never carries an SSE `id` or contract
cursor. Reconnect starts from the last committed cursor and discards the local
provisional buffer. Snapshots, final messages, lifecycle,
approval, feedback, board, checkpoint, and attention events are committed.
Refresh, connection loss, duplicate delivery, or daemon restart never commits
partial text and never dispatches, cancels, or retries work. Cancellation and
steering are separate idempotent POST operations. Clients reconcile optimistic
updates from committed revisions rather than interpreting connection state as
task state.

DAR-79 implements mutation reconciliation in the primary SQLite/WAL store at
schema 34. Each browser operation is bound to a one-way browser-session subject,
idempotency-key digest, operation kind, and canonical request digest. Its state
is `pending`, `committed`, or `rejected`: an exact retry replays the recorded
terminal result, different-body key reuse conflicts, and replay is evaluated
before a revision-staleness response. Only a definitive domain rejection is
terminalized as a bounded sanitized `rejected` response. A crash or response
ambiguity remains `pending`; the daemon does not infer that effects did or did
not occur. Authenticated recent-operation and submission-status projections let
the current browser session reconcile without resubmitting work.

Pending records are never removed by retention. Committed and rejected records
may be pruned after 30 days and are bounded to the most recent 5,000; all states
share a hard 10,000-record admission cap. Browser sessions and CSRF grants remain
process-local and are revoked on restart, while the operation records survive
normal database backup, restore, and migration. Reauthentication deliberately
does not grant access to another session subject's records.

Subjective feedback uses a separate immutable revision chain in the same
database. It is additive to objective deterministic/tool evidence and advisory
model-audit evidence; revision cannot overwrite those classes, and malformed or
inconsistent feedback history fails closed. Approval projections freshly redact
and bound the requested scope before returning `scope_summary`.

### Read-only inspection

DAR-80 keeps operational inspection behind authenticated `session_read` BFF
routes under the configured app base path. It exposes bounded projections for
configured models, per-task route selection and candidates, per-task usage,
normalized paired tool lifecycles, redacted audit provenance, daemon health,
and host resources. These are GET-only observations. They do not change model
configuration, routing policy, task state, approval state, or any other runtime
authority.

Projection `availability` is explicit. `unavailable` means the source could not
provide the projection. Within an available projection, an omitted optional
measurement means unknown and must be displayed as unknown, not zero.
Historical route selection is not a live health assertion; current health and
resources remain separate projections.

Usage preserves the ledger distinction between routed primary/fallback
execution and auxiliary classifier, summarizer, orchestrator-audit, and
optional-judge operations. Tool rows expose only normalized paired lifecycle
metadata and never arguments or results. Audit rows expose bounded reviewer,
model, provider, domain, rubric, ordered evidence-precedence, sanitized finding,
evidence-reference, and optional auxiliary-usage fields; prompts, provider
responses, tool payloads, and raw runtime records are not representable.

Model catalogs are capped at 256 entries, route snapshots at 256 candidates,
and health snapshots at 512 checks. Tool and audit histories are opaque-cursor
pages of at most 100 items and one MiB; the browser additionally caps aggregate
retention and page traversal. A bound or validation failure closes the view
without turning a partial result into authoritative state.

Event kind selects a closed typed payload schema. Unknown or duplicate fields
fail, and no generic raw-payload escape hatch exists. Events carry a maximum 1
MiB JSON-object payload and responses are bounded by
count, bytes, and catch-up time. Request bodies are at most 1 MiB unless a
narrower operation limit applies. Errors use a stable code, a sanitized
operator-safe message, retryability, and optional current revision/retry delay;
they never echo rejected content or internal errors.

### Workboard domain

`workboard` is a top-level application domain with its own append-only events,
SQLite/WAL repositories, transactional projections, and migration version. Its
canonical states are `backlog`, `ready`, `in_progress`, `blocked`, `review`,
`done`, and `canceled`. A user view may group or hide states but cannot redefine
them.

The service enforces this transition table; generic movement cannot bypass it:

| From | Command | To | Required fence |
| --- | --- | --- | --- |
| `backlog` | `card.move` | `ready` | Dependencies satisfied and board/card CAS |
| `ready` | `card.move` | `backlog` | No active claim and board/card CAS |
| `ready` | `card.claim` | `in_progress` | Atomic claim/attempt with frozen criteria/policy digests |
| `in_progress` | `card.block` | `blocked` | Live claim and reason; ownership stays fenced |
| `blocked` | `card.unblock` | `in_progress` | Same live claim and claim/card CAS |
| `blocked` or `in_progress` | `claim.recover` | `ready` | Immutable stop/task/effect proofs and claim/card CAS |
| `in_progress` | `candidate.submit` | `review` | Claim, attempt, criteria, candidate, and evidence fences |
| `review` | `acceptance.reject` | `ready` | Independent acceptor; a later attempt requires a fresh claim |
| `review` | `acceptance.accept` | `done` | Independent authoritative acceptance |
| Nonterminal | Safe cancel finalization | `canceled` | Stop/effect-resolution proof |

Only backlog/ready ordering uses `card.move`; it cannot enter execution, review,
blocked, completion, or canceled states.

Each card has one monotonic revision and at most one active claim/attempt.
Transitions, dependency edits, claims, heartbeats, checkpoints, candidates,
explicit accept/reject decisions, pause/cancel requests, and block/release
actions append attributed
events. Idempotency receipts and compare-and-swap results are committed in the
same transaction as the event and projection update. Dependency additions lock
the affected graph, reject self-edges and cycles, and atomically recompute
eligibility. Accepted completion atomically unlocks successors. Parent progress
is derived from child state.

Card creation/revision commands cover priority, budgets, parent, assignee,
labels, description, and criteria. Ordering commands bind board revision, layout
revision, card revision, target state, and at most one before/after neighbor
anchor; rank allocation is server-owned. A drag or keyboard move therefore
cannot overwrite concurrent layout changes.

Claims have bounded leases and heartbeats. Expiry or a stale heartbeat emits an
attention condition only; the old ownership fence remains active. Reassignment
requires a separate atomic recovery command and receipt bound to independent
worker-stop/process-death evidence, linked task terminal state, and an effect
resolution proving that no unresolved confirmed or uncertain side effect will
be replayed. Pause and cancel similarly remain requests until safe finalization.
Supervisors discover health from events, claims, leases, checkpoints, task
journals, and runtime observations after restart.

Recovery commands reference server-stored immutable proof and bind task-log,
process-proof, and effect-evidence digests plus card/claim revisions. Their
receipt repeats those fences, the atomic event range, and the resulting `ready`
state. Recovery never records success and never creates or replays a claim.

Workers submit a completion candidate bound to its attempt and criteria revision
to `review`. Acceptance criteria and
policy are frozen for an active attempt. The claimant cannot accept its own
candidate. Deterministic evidence has priority; an authenticated operator
decides subjective acceptance. Model audit is advisory unless independent
objective evidence authorizes the result. `done` is reachable only through a
durable acceptance record. Agent-facing tools use the same application service,
idempotency, revisions, policy checks, inherited denials, and per-card
single-writer lease.

Acceptance criteria are bounded typed records created with a card and may be
revised only for a future attempt. Claim creation freezes their exact digest and
the active policy digest. Evidence records are immutable and type their source
as deterministic validator, explicit user/operator feedback, or advisory model
audit, with pass/fail/abstain outcome and provenance. Candidate and acceptance
commands bind the attempt, candidate digest, criteria revision/digest, evidence
head/set digest, policy digest, and expected card revision. The server attributes
the acceptor and rejects the claimant, worker model, or advisory-only source as
acceptance authority.

Initial hard limits are 32 labels, 64 dependencies per card, 64 reverse
dependents, 32 acceptance criteria, 10,000 cards per board, 10,000 graph visits,
64 graph depth, 128 emitted events and 1 MiB per transaction, 256-byte titles,
64 KiB descriptions/evidence fields, 128-byte identifiers, and 1 MiB
operations/events. Cycle proof, successor unlock, and event construction fail
before mutation if their complete bounded transaction cannot fit. Configuration
may lower these limits and adds WIP, decomposition, attempt, time, token, cost,
rate, and board-count budgets. These are server limits, not client assurances.

Ordinary idempotency receipts are scoped by `(board_id, idempotency_key)` and bind a
digest of the canonical versioned command excluding transport credentials. The
same key and digest returns the exact stored public result, allocated IDs, and
committed event range without repeating effects. The same key with a different
digest conflicts. Receipts remain retained for the lifetime of referenced
cards/attempts and cannot expire while a retry could be ambiguous.
Board creation instead uses `(authenticated_creation_scope, idempotency_key)`;
its stored result contains the allocated board ID, so a lost acknowledgement can
be replayed without the client already knowing that ID.

### UI operation mapping

All browser routes below are owned by the BFF. “Existing” means an application
primitive already has a native HTTP adapter; the BFF still requires new handler
and projection work.

| UI operation | Browser route | Application/native primitive | Status |
| --- | --- | --- | --- |
| Create challenge | `POST /app/api/v1/session/challenges` | Browser-session challenge | Implemented |
| Complete login | `POST /app/api/v1/session` | Approved challenge consumption | Implemented |
| Logout | `POST /app/api/v1/session/logout` | Browser-session revocation | Implemented |
| List chats | `GET /app/api/v1/chats` | Task list plus session projection | Implemented projection |
| Read chat | `GET /app/api/v1/chats/{chat}/messages` | Bounded redacted presentation history | Implemented projection |
| Submit chat | `POST /app/api/v1/chats` | Submission application service | Implemented BFF facade |
| Resume chat | `POST /app/api/v1/chats/{chat}/resume` | Revision-fenced task continuation | Implemented BFF facade |
| Follow chat | `GET /app/api/v1/chats/{chat}/events` | Presentation SSE over durable readers | Implemented projection |
| Steer task | `POST /app/api/v1/tasks/{task}/steering` | Durable task steering | Implemented BFF facade |
| Cancel task | `POST /app/api/v1/tasks/{task}/cancel` | Durable task cancellation | Implemented BFF facade |
| Cancel submission | `POST /app/api/v1/submissions/{submission}/cancel` | Queued-submission cancellation | Implemented BFF facade |
| Inspect task controls | `GET /app/api/v1/tasks/{task}/controls` | Server-derived task capabilities | Implemented projection |
| Inspect feedback | `GET /app/api/v1/tasks/{task}/feedback` | Additive evidence/feedback projection | Implemented projection |
| Record feedback | `POST /app/api/v1/feedback` | Browser revision facade over additive subjective evidence | Implemented BFF facade |
| Revise feedback | `POST /app/api/v1/feedback/revisions` | Browser CAS facade over immutable subjective revisions | Implemented BFF facade |
| List approvals | `GET /app/api/v1/tasks/{task}/approvals` | Redacted bounded approval projection | Implemented projection |
| Decide approval | `POST /app/api/v1/tasks/{task}/approvals/{approval}/decision` | Browser revision facade over native approval command | Implemented BFF facade |
| List recent operations | `GET /app/api/v1/operations` | Session-bound operation journal projection | Implemented projection |
| Inspect submission | `GET /app/api/v1/submissions/{submission}` | Session-bound submission status | Implemented projection |
| List models | `GET /app/api/v1/models` | Configured-model redacted catalog | Implemented projection |
| Inspect route | `GET /app/api/v1/tasks/{task}/route` | Historical route selection and candidates | Implemented projection |
| Inspect task usage | `GET /app/api/v1/tasks/{task}/usage` | Routed and auxiliary usage accounting | Implemented projection |
| List task tools | `GET /app/api/v1/tasks/{task}/tools` | Normalized paired tool lifecycle | Implemented projection |
| List task audits | `GET /app/api/v1/tasks/{task}/audits` | Redacted audit provenance | Implemented projection |
| Inspect health | `GET /app/api/v1/health` | Bounded daemon/provider health projection | Implemented projection |
| Inspect resources | `GET /app/api/v1/resources` | Bounded resource/pressure projection | Implemented projection |
| List boards | `GET /app/api/v1/workboards` | `GET /v1/workboards` | New |
| Create board | `POST /app/api/v1/workboards` | `POST /v1/workboards` | New |
| Read/reconcile board | `GET /app/api/v1/workboards/{board}` | Bounded workboard snapshot | New |
| Mutate board | `POST /app/api/v1/workboards/{board}/operations` | Versioned workboard command service | New |
| Follow board | `GET /app/api/v1/workboards/{board}/events` | Durable workboard presentation SSE | New |

The canonical machine-readable mapping is `webui.Operations()`. Adding a UI
operation requires adding or naming its application primitive and contract test;
the front end may not invent private persistence or bypass the BFF.

The operation map assigns one security class to each route:
`bootstrap_challenge`, `challenge_bound`, `session_csrf_refresh`, `session_read`, or
`session_csrf_mutation`. This makes the two bootstrap exceptions explicit and
prevents their controls from becoming a general CSRF bypass.

JSON Schema enforces representable wire shape and state-specific constraints.
The normative `x-maxBytes`, `x-allowedFieldsByAction`, and `x-invariants`
extensions are enforced by Go contract tests and must be implemented by
front-end validation tooling. Receipt sequence arithmetic must be checked before
a receipt is accepted; `webui/testdata/v1/workboard-errors.json` supplies shared
negative conformance fixtures. Other cross-record identity, digest, authority,
ordering, and timestamp rules remain Go/application-service checks and are
never inferred merely from stock JSON Schema success.

### Accessibility baseline

The implementation target is WCAG 2.2 AA. Chat, approval dialogs, and every
board operation must be keyboard usable with visible focus, semantic names,
logical focus restoration, non-color status cues, sufficient contrast, reduced
motion support, and appropriately throttled live-region announcements. Drag and
drop must always have an equivalent keyboard/menu operation.

## Consequences

This boundary adds browser-session, projection, and workboard packages instead
of reusing raw `/v1` handlers. That is intentional: it prevents bearer-token
exposure, presentation-driven retries, sensitive event leakage, and split-brain
board state. Contract fixtures can be consumed by Go and front-end tests before
the server or UI exists.

DAR-77 through DAR-80 implement the app shell, authentication boundary, chat
presentation and reconciliation, bounded chat mutations, and the read-only
operational inspector described above. They do not implement workboard storage,
workboard endpoints, agent board tools, or Kanban feature UI; those remain in
DAR-81 through DAR-87. Inspection projections do not grant policy mutation or
work-dispatch authority.
