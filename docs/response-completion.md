# Response completion and benchmark diagnosis

NexusRouter checks a conservative subset of explicit user response requirements:
JSON (including compact JSON), an ending answer marker, code-only output, and
line limits. Requirements come from the current user message before context
assembly. Quoted/reference material, tool output, memory, and model-generated
text cannot introduce a requirement. Ambiguous or contradictory constraints
are left to the model. This is presentation validation, not semantic grading.

The original prompt is retained. A bounded user-level reminder is included in
context assembly, before estimation and admission. If a completed answer violates
the inferred contract, the runtime can request at most two corrections using
static diagnostics. Correction uses the same model and task and shares the
existing time, context, output, and turn budgets. When there is no correction
budget, the completed candidate remains available for independent grading.

Format correction removes the tool catalog and rejects unexpected tool calls;
it cannot repeat earlier tool effects. Real user steering disables the obsolete
contract and restores the original tool catalog. Corrections and original answers
remain in durable history as `response.revision` and ordinary turn events. Replay
preserves the corrective message. A final format check never substitutes for
accepted/rejected quality feedback. External feedback remains one model/task
sample with latency covering the whole attempt, including correction turns.

For responses with an inferred contract, text-only streaming buffers the current
candidate and publishes only the final completed answer. It does not concatenate
rejected JSON/code drafts. The lifecycle event stream and diagnostic journal
retain those drafts for inspection, with the existing secret-redaction boundary.

The September 25 review of the prior matched-gap campaign found 16 formatting
failures, one refusal to emit a simulated function-call representation, three
IPv4 behavior failures, and one grader false negative. The benchmark repository
separately corrected the legitimate `IPv4Address.packed` implementation rejection
and recorded an immutable regrade. That score change is a grader correction,
not a new model result. No expected answer, task ID, hidden test, or benchmark
specific transformation is embedded in the response-contract implementation.

The coding host separately fixes ignored working directories and reports zero
test discovery. Its general instructions require checking public interfaces,
matching the project's test framework, and executing real tests. These procedures
do not guarantee semantic correctness. Independent benchmark grades and held-out
tests remain necessary.

Context exploration now moves past a larger tier that already has sufficient
quality samples; it no longer keeps sampling that inferior tier while starving
larger candidates. This does not establish that a larger context is better.
The earlier short tasks all used 32K, and their historical resource measurements
were unavailable. Context suitability still needs measured task-specific evidence.
