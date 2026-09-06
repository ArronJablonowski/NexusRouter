# Comparing skill-version outcomes

DarwinRouter can compare an explicit bounded set of recorded tasks for two
versions of one skill. This read-only diagnostic complements deterministic
regression checks; it does not activate, disable or roll back a skill, update
fitness, invoke a model, or execute generated validation commands.

## Interfaces

Supply this versioned JSON object to `darwin skills compare --config config.yaml`
on standard input, or to authenticated `POST /v1/skills/comparison` with
`Content-Type: application/json`. The Go SDK exposes `CompareSkillOutcomes`.

```json
{
  "version": 1,
  "model_id": "local-worker",
  "domain": "coding",
  "profile": "default",
  "name": "review-code",
  "baseline_version": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "candidate_version": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "source": "user_feedback",
  "min_samples": 20,
  "min_drop": 0.1,
  "tasks": ["baseline-task", "candidate-task"]
}
```

Replace example identities with actual stored task/version IDs. Two tasks only
demonstrate the schema and will yield insufficient evidence. Configuration fixes
the skill scope and resolves `model_id` to the actual model/provider. There is no
request scope override. Retrieval and automatic learning may remain disabled.
The database must already exist; inspection creates no database or catalog.

Requests require every field, distinct 32-character lowercase hexadecimal
versions, 1–200 unique task IDs, 20–100 minimum samples per version, and a finite
minimum drop greater than zero and at most one. HTTP/CLI bodies are limited to
64 KiB and reject unknown/duplicate fields, malformed Unicode and trailing JSON.
HTTP additionally rejects query parameters, browser origins and unsupported
methods, and shares bounded control capacity with other inspection operations.
Application/HTTP inspection uses a cooperative five-second deadline.

## Evidence and exclusions

All selected journals and current evaluation chains are read in one SQLite WAL
snapshot. Concurrent feedback revisions cannot produce a mixed-time batch.
Preflight caps the entire set at 10,000 journal events / 8 MiB and 10,000
evaluation metadata rows / 8 MiB, retaining existing per-task chain limits.
Projected observations are capped at 4 MiB. Missing, corrupt, foreign-scope or
configured-secret-bearing records fail the whole request without partial results.

Only finalized completed/failed tasks with exactly one fresh, complete skill
reference can contribute. The version, scope, skill name and execution
model/provider/domain/profile must match. All tasks sharing a selected session
are excluded, as are child/continuation/retry tasks. Running/canceled tasks,
legacy or unavailable attribution, multiple/no fresh skills, other execution
profiles, missing quality and other evidence sources have explicit exclusion
counts. Conflicting content digests for the same selected version fail closed,
including conflicts in otherwise excluded records.

Explicit user feedback may qualify in any domain. Deterministic or tool-result
quality may qualify only in `code`, `coding`, `debugging`, `math` and
`structured_json`. Creative/unknown domains require user feedback. A model judge
never qualifies. Nonempty text or syntactically valid Go is a mechanical check,
not independent evidence of task quality. Latest negative feedback is a rejection,
not a missing sample; revisions never create extra samples.

## Interpreting the report

The report binds the requested configured model ID and its resolved execution
key, and returns aggregate accepted/sample counts, acceptance rates, version
digests, closed exclusion counts, policy and a canonical evidence digest. It
contains no prompts, output bodies, skill bodies or raw evidence references.
`advisory_only` is always true; `method` is `wilson_95_separation_v1`.

Each cohort uses the approximate 95% Wilson score interval described in the
[NIST Engineering Statistics Handbook](https://www.itl.nist.gov/div898/handbook/prc/section2/prc241.htm).
An empty cohort has bounds [0, 1]; its zero numeric rate means unknown, not failure.

- `insufficient_evidence`: either cohort has fewer than the required samples.
- `regression_signal`: both satisfy the minimum and the baseline lower bound
  minus the candidate upper bound is at least `min_drop` (absolute proportion).
- `no_regression_signal`: neither condition above holds; this is not proof that
  the versions are equivalent or that no regression exists.

These are per-cohort intervals, not a joint 95% guarantee, causal proof, an exact
significance test, or protection against repeated/selective testing. Operator
task selection, differing task difficulty, time and unobserved history can
confound the comparison. Fresh context inclusion does not prove semantic skill
execution. The digest supports reproducibility for identical evidence and policy;
it is not a signature or independent validation of evidence truth.

## Remaining delivery work

An [automatic latest-window selector](skill-comparison-selection.md) now uses
indexed recorded exposures and snapshot-wide session exclusions before comparing
current feedback. It selects before inspecting outcomes, not only from successes.

Difficulty controls, repeated
monitoring policy, qualified production validators, and durable activation-bound
outcome rollback are still required. This diagnostic must not be substituted for
those PRD requirements or wired directly to mutation authority.
