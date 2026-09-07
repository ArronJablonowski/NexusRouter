# Route explanation inspection

DarwinRouter persists an automatic routing decision before provider inference.
The decision can be inspected without loading the task's conversation:

```sh
./bin/darwin task route --db ./data/darwin.db --task TASK_ID
curl -H "Authorization: Bearer $DARWIN_API_TOKEN" \
  http://127.0.0.1:9786/v1/tasks/TASK_ID/route
```

Embedded hosts use `Client.InspectRouteExplanation(ctx, taskID)`.

The version-one record includes task/session/route identity, event sequence and
time, the configuration SHA-256, domain/profile, selected provider/model route,
candidate constraint snapshots, normalized ranked scores, closed exclusion
reason classes, ordered fallbacks, policy weights and whether bounded
exploration selected a non-leading route. Candidate credential fields are only
booleans used by routing; no credential value or environment-variable name is
stored in this record.

The reader returns exactly the first two-event boundary and separately validates
the task's current head without returning its content. It requires `task.started`
followed immediately by `route.selected`, validates the event page, then
validates the complete explanation: finite normalized values,
unit-sum weights, unique candidate routes, complete ranked/excluded partition,
known exclusion reasons, deterministic rank order, exact primary membership,
complete non-primary fallbacks, exploration consistency and selected identity.
Malformed storage or a malformed host callback returns no partial record.

The response contains no messages, prompt, model output, provider endpoint,
credential value/reference, tool argument or tool result. Unlike full task
inspection, it is suitable for routing audits that do not require session
content. The local CLI and SDK open SQLite read-only and never create a missing
database or run migrations. HTTP inspection uses a control slot independent of
task execution capacity and retains bearer authentication, browser-origin
denial, query denial and a five-second deadline.

An explicitly selected model has no `route.selected` event and therefore no
route explanation. A 404 or inspection error does not imply that the task is
missing. Recorded health, capacity, credential availability and cost are the
admission-time snapshot—not current state, a billing record, a quality verdict,
or authority to retry, resume, disable, or remove a model. The record validates
internal consistency but cannot recompute historical external conditions.
