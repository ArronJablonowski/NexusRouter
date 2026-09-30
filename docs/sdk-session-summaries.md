# Session summaries through the Go SDK

The embedded `sdk/v1.Client` supports the same explicit draft, review and
continuation workflow as the CLI and NexusRouter-native HTTP API. It also supports
opt-in trusted deterministic validation; no validator is selected implicitly.
These methods do not enable automatic compaction, retry or background inference.

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

A trusted Go host may replace the manual decision with a registered deterministic
validator:

```go
registry, err := darwin.NewSummaryValidatorRegistry(map[string]darwin.SummaryValidator{
    "project-contract-v1": projectValidator,
})
if err != nil { return err }
review, err := client.ValidateSummary(ctx, draft.ID, "", operationID,
    "project-contract-v1", registry)
if err != nil { return err }
if review.Decision != "approved" {
    // Rejected and abstained drafts remain inactive.
    return nil
}
```

NexusRouter also exports a non-authorizing stock integrity linter:

```go
registry, err := darwin.NewSummaryValidatorRegistry(map[string]darwin.SummaryValidator{
    darwin.SummaryIntegrityValidatorID: darwin.NewSummaryIntegrityValidator(),
})
if err != nil { return err }
review, err := client.ValidateSummary(ctx, draft.ID, "", operationID,
    darwin.SummaryIntegrityValidatorID, registry)
if err != nil { return err }
// The stock linter returns rejected or abstained, never approved. Present its
// bounded evidence to an authenticated operator before any approval.
```

The validation operation ID becomes the immutable review ID. Retrying the exact
operation recognizes its existing record without invoking the callback again.
Every version-two validation review binds the attempt, source digest, complete
draft digest, validator identity and prior review head. Callbacks receive the
full source and draft, must be local, side-effect free, deterministic,
cancellation-cooperative and concurrency-safe. An LLM judgment is not a trusted
deterministic validator. Use a new validator ID whenever semantics change.
The stock integrity validator checks mechanical provenance and high-confidence
lexical anchors only. It intentionally cannot certify natural-language
completeness, polarity, authority, tool effects, or subjective fidelity.

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
All SDK methods preserve an already-canceled caller context; cancellation
during generation or review may instead produce an existing normalized write-path
error. Inspect durable state rather than assuming a failed return means no write.

Each explicit `SummarizeTask` call requests a new attempt, not an idempotent retry.
A failure may leave a durable failed attempt or a started attempt when the
terminal write was uncertain. Inspect before requesting another potentially
billable invocation. A successful draft remains inactive until reviewed and
explicitly selected for a continuation; it does not update measured fitness.

### Interrupted-attempt recovery

After an abnormal owner-process exit, `InspectSummaryAttempt` or the existing
`nexus summaries show` command may report `status: "interrupted"` with code
`owner_interrupted`. This terminal state means the exact guarded local owner was
independently proven stopped; it does not say whether the provider received or
completed the request. The attempt therefore has no draft, token usage or
elapsed measurement and cannot be reviewed or selected for continuation.

The SDK exposes the associated redaction-safe evidence separately:

```go
receipt, err := client.InspectSummaryRecovery(ctx, attemptID)
page, err := client.ListSummaryRecoveries(ctx, sourceTaskID, afterReceiptID, 100)
```

Each receipt binds its ID, attempt and source task, source sequence and digest,
terminal state/code, and recovery time. It excludes generated text, partial
output, provider/model details, token usage, process identity, guard paths and
lock metadata. The task and attempt IDs and source digest remain correlation
metadata, so exported receipts should still be access-controlled.

`ListSummaryRecoveries` uses the same live lexical-page convention as summary
attempt listing: an empty task includes all source tasks, an empty cursor starts
the scan, `after` is the exclusive prior **receipt ID**, and `limit` is 1–100.
Continue with the last returned receipt ID. Concurrent insertion before the
cursor requires a fresh scan. Inspection is read-only, uses the same cooperative
ten-second deadline and returns no partial page on failure. There is currently
no dedicated CLI or HTTP receipt-inspection surface; CLI operators can still see
the terminal attempt with `nexus summaries list|show`.

Trusted hosts can request one mutating recovery scan page explicitly:

```go
next, recovered, err := client.ReconcileInterruptedSummaries(ctx, after, 100)
```

Here `after` is instead an opaque canonical positive decimal scan position, not
an attempt or receipt ID. Use `""` for the first page and pass each nonempty
`next` value unchanged; an empty `next` means that live scan reached its end.
The limit is 1–100 and skipped active or unverifiable owners still advance the
scan. This method opens writable storage and may migrate it, unlike inspection.
It only changes eligible `started` attempts to `interrupted`, appends one receipt
and records summarizer accounting as `failed` with retry class `uncertain` and
no measured token usage. Any configured cost remains an admission estimate, not
proof of provider billing.

Recovery never constructs or calls a provider, redispatches summary work,
salvages partial output, creates a review, approves or activates compaction,
changes the source journal, or creates fitness evidence. Repeating a scan is
inert after the single terminal transition. A terminal draft committed before a
lost acknowledgement remains drafted and is not converted or given a recovery
receipt.

The task-start transaction rechecks the exact current review and records its ID
in the new checkpoint. Rejection committed before that transaction blocks use.
Rejection afterward blocks future direct reuse, but neither cancels the admitted
task nor removes copies in existing sessions. Source journals remain unchanged;
compaction is not deletion or a proof of summary accuracy.
