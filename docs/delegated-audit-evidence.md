# Delegated result evidence in standalone audits

`AuditTask` follows successful and rich failure envelopes recorded by `delegate`
and `delegate_batch`. The Codex adapter exposes these tools in the `darwin`
namespace. Before constructing the reviewer provider or recording a review
attempt, the audit verifies canonical envelope shapes, parent-owned work/session
lineage and execution-to-work lineage. Failure references must match the exact
durable terminal sequence, kind, code and reason. Malformed or forged references
fail closed.

A successful envelope requires completed work and execution, a positive
`worker_validator` record followed by `worker.completed`, and a final work
completion attributed to the same worker. The published output must match both
the durable accepted work output and the execution's final answer exactly.
Comparison is local; it does not add raw output to evidence. Redaction-layer
mismatches fail closed rather than normalizing distinct strings into a match.

The reviewer receives `delegated_<parent-tool-sequence>_<work|execution>_<sequence>`
references containing validation and terminal metadata, never additional child
prompts, generated output, tool arguments or raw failure details. Execution
validation must match the observed model turn and attempt. Supervisor validation
uses its separate `worker_validator`/worker identity rather than pretending to
be a model turn. References are retained in the resulting audit record.

Batch references use
`delegated_<parent-tool-sequence>_item_<index>_<work|execution>_<sequence>` and
include a zero-based `batch_index`, including index zero. Canonical batches
contain two to four results, matching the requested task count in the paired
tool call. Generic unavailable results preserve their position but add no child
evidence. Mixed positive and negative evidence is retained independently; one
successful sibling does not establish success for the batch.

This is evidence for an advisory review, not a new deterministic result, model
fitness update, retry permission or authorization to execute tools. Go syntax
validation proves parsing only, not compilation, executable tests or correctness.
Creative judgments continue to defer to explicit user preferences.

## Bounds and limitations

- At most eight referenced work/execution lineages are traversed across all
  single calls and batch items, without recursion. Duplicate work/execution IDs
  fail closed. Generic rejections have no traversable references. Batch
  cancellation still suppresses every item into a generic rejection, so it
  cannot supply child evidence through this path. No background or automatic
  review job is introduced.
- Work records identify their parent and session but do not independently store
  the parent tool-call ID. `parent_tool_sequence` means *referenced by that parent
  journal event*, not independently proven call-specific ownership. Substituting
  sibling work within the same trusted parent journal cannot be independently
  ruled out using the present schema. Batch count and position checks do not
  independently bind each child to its specific input prompt.
- Child traversal uses a five-second deadline derived from caller cancellation.
  Storage replay is separately bounded to 8 MiB/10,000 events per task. Traversal
  then admits at most 1,000 events per task, 32 bounded pages per task and 1 MiB of
  cumulative paged child event payload across the graph. These are distinct
  bounds; the paged limit is not a claim that replay reads only 1 MiB.
- Combined parent/child execution evidence is capped at 252 records and 64 KiB,
  leaving two slots for session history and final-candidate identity in the
  reviewer's 254 supplied-evidence limit. Exceeding limits rejects the audit;
  evidence is never silently truncated.
- Interrupted turns, pending tools and uncertain effects are not accepted for
  this traversal. Some canceled provider histories consequently remain
  unauditable. Cancellation is not detached to complete traversal.
- Reads inspect replay-validated terminal records but do not repair data or
  create a cryptographic attestation against database tampering. Child event
  payloads are read locally for verification but only metadata is exported.

Verification is fixture-based: positive/negative validator metadata reaches the
reviewer for single and mixed batch results; forged lineage, missing acceptance,
mismatched output, terminal references and malformed envelopes stop before
dispatch. Child-only raw text is excluded from reviewer input, and accepted
output already present in parent history is not duplicated as execution evidence.
This change does not add live-model, multi-worker recovery or full PRD
qualification.
