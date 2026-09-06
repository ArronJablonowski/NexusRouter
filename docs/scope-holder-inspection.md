# Resource-scope holder inspection

Inspect existing storage without dispatching models, profiling hardware, probing
process guards, migrating schemas, releasing leases or retrying work:

```sh
darwin resources leases --db path/to/darwin.db --scope workspace
```

The Go SDK exposes `Client.InspectScopeLeases(ctx, scope)`. The authenticated
HTTP API exposes `GET /v1/resources/leases?scope=workspace`; URL-encode other
scope values. Exactly one scope parameter and no request body are accepted.

Version-1 metadata includes the requested scope, observation time, storage and
overlap-policy versions, availability, and task-ID-sorted holders. Each holder
contains live/expired reader/writer counts. **Live means unexpired, not that the
owner is alive.** Expired unreleased leases remain visible. Released leases do
not appear. Neither an empty list nor a successful read grants admission or
release authority; execution must still acquire its lease normally.

Matching uses the admission compatibility policy: exact scope equality, with
`workspace` and all `create_*` scopes additionally overlapping each other.
Other scope names are not normalized. The requested scope and task IDs may be
sensitive. Actual stored alias names, lease tokens, owners, guard paths, prompts,
tool arguments and outputs are not returned.

Scopes must be valid UTF-8, 1–512 bytes, exactly trimmed and free of Unicode
control characters. Schemas 1–2 return `available: false` and an empty holders
array; supported schemas 3–23 return an available observation. Unknown schemas,
invalid metadata and more than 1,000 overlapping unreleased lease rows fail the
whole read, without partial results or pagination. Task-head metadata is checked;
this is not full history replay or acceptance validation.

Reads use a coherent transaction and cooperative five-second deadline. HTTP
shares bounded control capacity and returns generic errors. Read-only SQLite
opens may create ordinary WAL/SHM coordination sidecars, but do not change task
or lease records. No missing database is created.

Tests use synthetic storage and cover aliases, grouping, expiry, released-row
exclusion, legacy schemas, exact/over-limit bounds, corruption, strict requests,
cancellation, privacy and unchanged main database bytes. This bounded diagnostic
does not implement global listing, persistent operator attention, uncertain
write resolution or idempotent reassignment.
