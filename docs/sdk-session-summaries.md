# Session summaries through the Go SDK

The embedded `sdk/v1.Client` supports the same explicit draft, operator-review
and continuation workflow as the CLI and Darwin-native HTTP API. These methods
do not enable automatic compaction, approval, retry or background inference.

Assuming an initialized `client`, caller-owned `ctx`, completed `sourceTaskID`
and configured `summaryModelID`:

```go
draft, err := client.SummarizeTask(ctx, sourceTaskID, summaryModelID, 6, 0)
if err != nil {
    // If draft.ID is nonempty, inspect that attempt before requesting new work.
    return err
}
saved, err := client.InspectSummaryAttempt(ctx, draft.ID)
if err != nil { return err }
// Present saved and the original source to an authenticated operator here.
// Do not substitute a model's judgment for the operator's approval.
_ = saved
```

Only after obtaining that operator decision:

```go
review, err := client.ReviewSummary(ctx, draft.ID, "", "approved",
    "Compared the proposal against the original source")
if err != nil { return err }
result, err := client.Run(ctx, darwin.Request{
    Version: 1, ModelID: executionModelID, Prompt: followup,
    ContinueTaskID: sourceTaskID, SummaryAttemptID: draft.ID,
})
```

The examples assume `darwin` aliases the SDK import. The host must implement the
actual operator interface; these snippets are not an unconditional approval
callback. The zero cost ceiling permits only a configured zero-cost estimate.
Normal source privacy, deployment mode, context and resource checks still apply.
Native signed-in Sol drafting and continuation use their existing guarded paths;
see [native drafting](codex-session-summaries.md) and
[native compacted continuation](codex-compacted-continuation.md).

## Inspection and decisions

`SummaryAttempt` and `SummaryReview` are aliases for the versioned public session
records. Inspection returns full proposals, including source-derived text when
present, not just metadata. Protect returned records and exported copies.

- `InspectSummaryAttempt(ctx, id)` reads a durable attempt, including its status.
- `ListSummaryAttempts(ctx, task, after, limit)` returns a lexical ID page of
  complete records. An empty task includes all tasks; an empty cursor starts
  the scan. The cursor is exclusive and `limit` must be 1–100. Continue using
  the last returned ID. These are live pages, not a frozen scan: a concurrent
  insertion sorting before the cursor will require a new scan to discover.
- `SummaryReviewHistory(ctx, id)` returns the ordered immutable review chain.
  An unknown attempt also has an empty history; use `InspectSummaryAttempt` to
  distinguish that from an existing attempt with no reviews.
- `ReviewSummary(ctx, id, expected, decision, note)` records `approved` or
  `rejected` against the exact previous review ID. Use an empty expected ID only
  for the first review; later decisions use the latest observed review's ID.
  Stale decisions conflict. Notes are bounded and configured secrets redacted.

Inspection opens existing storage read-only with a cooperative ten-second
deadline, honors shorter caller deadlines, and neither initializes nor migrates
storage. SQLite may use WAL/SHM coordination sidecars. Successful empty lists are
allocated empty slices; failed reads return no partial records. Nil clients,
nil contexts and invalid read parameters return `ErrAdmission`; missing or
corrupt storage and failed single-attempt lookup return `ErrInspection`.
Inspection cancellation/deadline errors remain recognizable through `errors.Is`.
All five SDK methods preserve an already-canceled caller context; cancellation
during generation or review may instead produce an existing normalized write-path
error. Inspect durable state rather than assuming a failed return means no write.

Each explicit `SummarizeTask` call requests a new attempt, not an idempotent retry.
A failure may leave a durable failed attempt or a started attempt when the
terminal write was uncertain. Inspect before requesting another potentially
billable invocation. A successful draft remains inactive until reviewed and
explicitly selected for a continuation; it does not update measured fitness.

The task-start transaction rechecks the exact current review and records its ID
in the new checkpoint. Rejection committed before that transaction blocks use.
Rejection afterward blocks future direct reuse, but neither cancels the admitted
task nor removes copies in existing sessions. Source journals remain unchanged;
compaction is not deletion or a proof of summary accuracy.
