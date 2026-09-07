# Durable task discovery

DarwinRouter can discover saved task IDs without loading their conversation or
tool payloads:

```sh
./bin/darwin task list --db ./data/darwin.db --limit 25
curl -H "Authorization: Bearer $DARWIN_API_TOKEN" \
  'http://127.0.0.1:7788/v1/tasks?state=completed&limit=25'
```

The Go SDK uses `Client.ListTasks(ctx, TaskListOptions)`. Interactive chat uses
`/tasks`; when another page exists it prints an opaque cursor accepted by
`/tasks CURSOR`. None of these operations select history. `/resume TASK_ID`
still performs the existing exact continuation-readiness check before retaining
the source for a later prompt.

Each version-one item contains only `task_id`, `session_id`, current `state`,
head `sequence`, and `started_at`. There are no prompts, messages, model outputs,
tool arguments/results, route details, provider endpoints, or credentials. The
configured application/SDK/HTTP/chat boundary checks both initial and freshly
resolved credential values before returning a page; an identity collision fails
closed. The explicit `task list --db` command has no configured secret resolver,
so its metadata should still be handled as sensitive.

Pages contain at most 100 items and one MiB of compact JSON. They are ordered by
newest durable insertion. The first page freezes a high-water mark in its opaque
cursor, so later-created tasks do not appear in subsequent pages. A state filter
is bound into that cursor, but state membership is live: a task can transition
between page requests. Start and current-head event envelopes are checked for
bounded types, task/session correlation, sequence, and state consistency before
an item is returned. Any corrupt item fails the whole page without partial data.
This metadata check does not replay or validate the complete journal.

Listing is read-only and performs no migration, history repair, inference,
continuation, cancellation, lease action, or provider discovery. A missing
configured database yields an empty first page without creating a file; a raw
database CLI request for a missing path fails. Running state does not prove a
live owner, and completed state alone does not prove continuation eligibility.
