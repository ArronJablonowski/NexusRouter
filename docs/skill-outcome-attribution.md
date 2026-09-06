# Skill context attribution and task outcomes

DarwinRouter records which validated skill versions the host freshly admitted
into a task's initial context. This is the evidence needed to study outcomes
after a revision, not proof that a model followed the workflow or that the skill
caused success or failure. Statistical comparison and automatic outcome-driven
rollback are not implemented by this inspection feature.

## Durable attribution

New application tasks include `skill_context` in `task.started`. Each reference
contains the selected scope, name, immutable version ID and catalog digest. Only
skills surviving tool compatibility, byte budgets and context-tier selection are
included. Selection does not change catalog activation or tool permissions.

- `complete: true` with references records the complete fresh skill tier.
- `complete: true` with no references means no fresh skill tier was included.
- `complete: false` with no references means attribution is unavailable, such as
  when an identity collides with a configured credential.
- Missing `skill_context` is legacy/unrecorded attribution, not evidence that
  no skill was present.

The record is committed before model dispatch and can therefore exist even when
inference never starts. The runtime bounds the record to 16 unique scope/name
pairs, validates identity/digest shapes, permits it only on `task.started`, and
owns copies before callbacks and during replay. Ordinary CLI/API input cannot
set this field. Lower-level Go runtime callers are trusted to supply provenance.

Neither user/model JSON nor procedural text retained in history creates fresh
attribution. A context engine that omits the whole skill tier produces an empty
fresh tier. Continuations may still contain older skills in history; do not use
an empty fresh tier as an unexposed control group. Branch/retry identities are
included in outcome inspection so this distinction stays visible.

Secret-bearing identity metadata is never rewritten into a different skill key.
The application preserves redacted workflow context but withholds attribution
for the entire selected bundle. The journal checks identities again before
persistence. Historical records and receipts are not backfilled or rewritten.

## Inspect one task

```sh
darwin task skill-outcome --config config.yaml --task TASK_ID
```

The Go SDK exposes `client.SkillTaskOutcome(ctx, taskID)`. The daemon provides
authenticated `GET /v1/tasks/{taskID}/skill-outcome`, with no query parameters or
request body. All use the configured task database and skill scope, without
opening the skill catalog, initializing storage, invoking models, validating a
skill, updating fitness, or rolling back an activation. Inspection remains
available when learning and skill retrieval are disabled. Tasks without recorded
skill references need no configured skill scope; returned references must match
the currently configured scope exactly.

One read-only SQLite transaction returns:

- Task/session IDs, sequence, state, privacy, parent and retry identities.
- Fresh skill-context attribution, including unknown/unavailable states.
- The latest model attempt and its provider/model/domain/execution-profile key.
- The current evaluation ID and digest, plus quality source and decision when
  independent evidence exists.
- Separately, the recorded mechanical output checks for that attempt, verified
  against persisted output: nonempty text and, when requested, Go syntax.

There is no prompt, workflow body, tool argument, or model output in the report.
Quality evidence references use bounded opaque IDs, not arbitrary diagnostics;
ID grammar does not itself guarantee secrecy. Configured-secret collisions in
returned metadata reject the whole report without partial results. Scope
conflicts, corruption and malformed evidence also fail closed.

Missing quality evidence stays unknown even when a task completed and its text
is nonempty. Syntax validity does not prove that code compiles or passes tests.
The existing evidence ladder applies: deterministic checks and tool evidence
take precedence, then user feedback, then an explicitly permitted LLM judge.
Current feedback revisions replace prior subjective judgments rather than
adding samples. Judge-only quality remains visibly labeled as model judgment.
Failed, canceled, running and not-yet-evaluated tasks remain inspectable.

The read uses the existing 10,000-event/8 MiB journal limits and bounded evaluation
revision admission; its response is limited to 64 KiB. The application and HTTP
adapter impose cooperative five-second deadlines. HTTP uses the shared bounded
control-operation pool and rejects browser origins. No missing evidence or
timeout grants retry, execution, model-pruning or rollback authority.

## Verification and remaining work

Tests exercise actual model-context assembly and journal persistence, omission,
secret redaction, forged user/model JSON, historical context, replay ownership,
and disabled inspection. HTTP/application/SQLite integration preserves negative
and corrected user feedback, keeps mechanical checks separate from quality, and
performs no inference or mutation. An actual WAL concurrency test pins a reader,
commits a feedback revision from another connection, and confirms that the first
report retains the old evidence while the next sees the new revision.

An indexed cross-task/version observation store, sufficiently sampled comparable
cohorts, treatment of multiple simultaneous skills and inherited history,
statistical regression policy, and qualified production skill validators remain
necessary before automatic outcome-based rollback can be enabled.
