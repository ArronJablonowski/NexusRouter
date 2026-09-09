# Durable session branching

DarwinRouter can queue a new direct child from one exact completed task head.
This is a stricter operation than an ordinary continuation: it never waits for
a running source and never treats recovered failed history as completed.

Obtain a `TaskHeadFence` from `ListSessionTasks` or
`GET /v1/sessions/{session_id}/tasks`. The content-free fence binds the source
task and session IDs, head sequence, and canonical terminal event ID. Submit it
with one new prompt through:

- Go SDK: `Client.SubmitBranch(ctx, key, fence, request)`
- CLI: `darwin branch --config path --key key --task task --session session --sequence n --event event --model model < prompt.txt`
- HTTP: authenticated `POST /v1/tasks/{task}/branches` with an
  `Idempotency-Key` and strict `{version, source, request}` JSON body

Admission replays the complete bounded source history inside the same writer
transaction that creates the queued submission. The durable canonical request
contains a versioned internal fence over the source history digest and privacy
ceiling. The dispatcher rechecks that fence after claiming the submission, and
the event store checks it again in the transaction that appends every new
`task.started` record. Rejection creates no child task and occurs before the
selected execution provider or tool is constructed.

The source must be canonically replayable and completed without pending tools,
uncertain effects, interruption, worker ownership, or delegation origin.
Worker and delegated output must first pass their existing parent acceptance
protocol; it cannot itself become branch authority. A successful branch keeps
the source session ID and names the source as its direct parent. Independent
sibling branches import only their common ancestor history and their own new
prompt.

Automatic model fallback remains possible after branch admission. Each later
root attempt must name the immediately preceding safe, output-free,
provider-retryable attempt. Internal accepted worker descendants remain scoped
to the same submission and session. A local-only source or request can never be
routed to a cloud task; a cloud-allowed source may still be routed more strictly
to a local task.

The idempotency key is part of the branch contract. Retrying the exact key,
fence, request, and effective configuration returns the original submission.
Changing any of them conflicts. After uncertain delivery, inspect the returned
submission or retry the exact operation; do not invent a new key, and never
automatically retry a tool effect whose state is confirmed or uncertain.

This operation creates a branch; it does not merge siblings, choose a preferred
leaf, compact history, resume an interrupted turn, mutate its source, or relax
normal provider, resource, budget, credential, memory, skill, and tool policy.
