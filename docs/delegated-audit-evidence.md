# Delegated failure evidence in standalone audits

`AuditTask` can follow a rich failure envelope recorded by the single `delegate`
tool. The runtime tool name is `delegate`; the Codex adapter exposes it in the
`darwin` namespace. Before constructing the reviewer provider or recording a
review attempt, the audit verifies the canonical failure shape, parent-owned
work/session lineage, execution-to-work lineage, and exact durable terminal
sequence, kind, code and reason. Malformed or forged rich references fail closed.

The reviewer receives `delegated_<parent-tool-sequence>_<work|execution>_<sequence>`
references containing validation and terminal metadata, never additional child
prompts, generated output, tool arguments or raw failure details. Execution
validation must match the observed model turn and attempt. Supervisor validation
uses its separate `worker_validator`/worker identity rather than pretending to
be a model turn. References are retained in the resulting audit record.

This is evidence for an advisory review, not a new deterministic result, model
fitness update, retry permission or authorization to execute tools. Go syntax
validation proves parsing only, not compilation, executable tests or correctness.
Creative judgments continue to defer to explicit user preferences.

## Bounds and limitations

- At most eight rich single-delegation failures are traversed, without recursion.
  Batch delegation, successful-child traversal and legacy generic rejections are
  unchanged. No background or automatic review job is introduced.
- Work records identify their parent and session but do not independently store
  the parent tool-call ID. `parent_tool_sequence` means *referenced by that parent
  journal event*, not independently proven call-specific ownership. Substituting
  sibling work within the same trusted parent journal cannot be independently
  ruled out using the present schema.
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

Verification is fixture-based: actual validator metadata reaches the reviewer;
forged lineage, session, terminal references and malformed envelopes stop before
dispatch; raw child text is excluded. This change does not add live-model,
multi-worker recovery or full PRD qualification.
