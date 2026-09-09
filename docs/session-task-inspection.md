# Session task inspection

DarwinRouter exposes a content-free, read-only view of the task graph inside one
durable session. It is available through:

- Go SDK: `Client.ListSessionTasks(ctx, sessionID, options)`
- CLI: `darwin session tasks --db path --session id`
- authenticated HTTP: `GET /v1/sessions/{session_id}/tasks`

Each item contains only the version, task and session IDs, optional parent and
retry task IDs, current durable state, head sequence, start time, and a
content-free exact-head fence containing those task/session identities plus the
canonical head event ID. Prompts,
answers, tool arguments/results, route payloads, provider endpoints, summaries,
and credentials are not part of this projection.

Pages contain 1–100 items and are ordered newest-first by durable task insertion.
Use `limit` and the opaque `after` cursor to continue. The cursor is canonical,
bound to the exact session, and freezes the insertion high-water mark: tasks
inserted after the first page do not appear in that traversal. Current task
state can still change between reads, so a page is an observation rather than a
transaction spanning all pages.

The reader validates each task's canonical start event and durable head before
returning the page. Parent links, when present, must identify an earlier,
validated task in the same session. Retry links may cross sessions because each
automatic route attempt owns its own session when it is not itself a
continuation; every predecessor must still be earlier and match the bounded,
output-free retryable-provider-failure lifecycle. Missing events, mismatched
task/session metadata, malformed or unsafe lineage, duplicate tasks, invalid
heads, noncanonical event bytes, or oversized records fail the complete page
without returning partial data.

An empty page means no task in the inspected database matched that session at
the read boundary. It does not establish that the identifier has never existed
elsewhere. Listing does not replay or repair work, select a latest leaf, prove
owner liveness, authorize continuation, or bypass provider, privacy, resource,
budget, or tool policy. Inspect a selected task's continuation status separately
before requesting an ordinary continuation. A completed task's exact-head fence
can instead be presented to the strict branch operation described in
[durable session branching](session-branching.md); the writer still replays and
revalidates the source transactionally, so listing alone grants no authority.

The raw CLI opens only an existing database and has no configured credential
resolver, so operators should still handle its identifiers as sensitive. The
application, SDK, and daemon paths recheck configured secret collisions before
returning metadata.
