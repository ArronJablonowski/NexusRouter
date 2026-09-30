# Fitness observation decay

NexusRouter applies exponential recency decay to each current evidence
observation before aggregation. It does not multiply a lifetime average by the
newest record's age.

For observation time `t`, routing time `now`, and configured half-life `h`, the
weight is:

```text
weight = 2^(-(now - t) / h)
```

The routing call captures one UTC `now` value and uses it for every candidate.
Tests inject this clock. An observation exactly at `now` has weight `1`; one
half-life old has weight `0.5`. Very old observations may underflow to weight
`0`, which is retained as zero contribution. A timestamp after `now`, an empty
timestamp, or a non-positive half-life fails the route rather than being
clamped.

`routing.decay_half_life` is the default. An exact domain/profile selector can
override it:

```yaml
routing:
  decay_half_life: 30d
  decay_overrides:
    - domain: coding
      profile: local
      half_life: 7d
```

Selectors are exact and duplicate selectors are invalid. The list is replaced
as one value by a higher-precedence configuration layer, consistent with other
configuration arrays.

## Identity, replay, and corrections

Every fitness contribution is derived from an immutable evaluation ID, its
base ID, its immediate predecessor when corrected, and the evaluation's source
time. A correction changes only the permitted subjective outcome, remains one
sample, and retains the base observation time. The current chain head supplies
the contribution; the full chain remains inspectable. Exact duplicate records
are idempotent, while conflicting duplicates, forks, orphan revisions, broken
heads, or changed execution facts fail closed.

Records are sorted by source time and stable ID before floating-point summation.
Consequently, replay, late arrival, and backfill yield the same aggregate for
the same immutable record set. Database row order and host arrival time are not
fitness evidence.

For orchestrator audits, any current evaluation record for an attempt suppresses
the separate advisory record so one attempt cannot receive both direct fitness
and parallel audit influence. This includes an optional judge-backed evaluation;
within that evaluation, deterministic, tool, and user evidence still outrank the
judge under the fixed evidence ladder. Otherwise, at most one audit per candidate
attempt is selected by the maximum `(source time, stable ID)` pair.
Same-model positive or abstaining reviews contribute nothing; same-model
warnings retain their existing confidence cap. Audit observations decay
individually; their quality is weighted by both decay and validated reported
confidence. They remain advisory rather than becoming execution samples.

## Ranking and explanation

The minimum-sample threshold is applied to effective sample mass: the sum of
the individual weights. Weighted quality, schema compliance, reliability,
latency, and cost use that same mass. Sparse or stale evidence therefore
shrinks toward neutral without discarding raw sample count.

Each persisted route explanation includes, separately for direct fitness,
advisory audits, and objective validity when available:

- raw sample count;
- effective sample mass;
- average decay contribution;
- oldest and newest source timestamps; and
- whether per-observation decay was applied.

These fields contain no prompt, model output, finding, credential, or provider
endpoint. Inspection revalidates effective-count arithmetic and rejects a
source window ending after the route was recorded. Legacy route records without
decay metadata remain readable and are validated under their original
aggregate-decay representation.

The mutable SQLite `fitness` table remains a transactional compatibility
projection. Automatic routing derives decayed evidence from immutable
evaluation, revision, and audit records instead of treating that projection as
the temporal authority.
