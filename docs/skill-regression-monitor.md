# Periodic skill regression monitoring

Go hosts can explicitly start `Client.StartSkillRegression(ctx, interval,
validator)` to periodically check active skills in the configured scope and
automatically restore a validated predecessor on deterministic failure evidence.
This is separate from drafting and activation: `skills.enabled` and
`skills.rollback_on_regression` are required, while learning and automatic new
activation may remain disabled. No monitor starts merely by constructing a
client. The standalone daemon still requires a configured validation engine
before it can offer this behavior.

The returned supervisor is caller-owned. Retain it, inspect `Health()`, and call
`Close()` to cancel and join it. Intervals must be between one second and 24 hours.
The first tick starts immediately. Each tick inspects one active skill, in lexical
name order, then moves to the next. An empty page wraps to the beginning; newly
added earlier names are discovered on the next cycle. A full cycle includes an
empty wrap tick. The catalog currently supports at most 1,000 skills; monitoring
is not restricted to the first 100 discovered entries.

## Validation authority and rollback

Supply trusted, read-only, repeat-safe, concurrency-safe and cancellation-
cooperative `skills.Validator` host code. It must perform meaningful objective
checks appropriate to each candidate and use a versioned evidence identifier.
There is no default success validator and no remote endpoint accepting declared
proof. Generated test descriptions and LLM opinions are not validation authority.

Each check uses a fresh exact activation revision and the existing
`RevalidateSkillVersion` guards. A changed activation or ABA sequence invalidates
that observation. Passing checks leave catalog bytes unchanged. A valid
deterministic failure atomically records evidence with rollback to the validated
predecessor. Callback errors, panic, cancellation, invalid or nondeterministic
proof, stale revisions, and missing predecessors do not authorize a new rollback.
Current secret-collision checks apply before and after callback execution.

The callback has the existing cooperative three-second bound within a ten-second
application step. The monitor never launches inference, task tools, or generated
validation commands. It is not a sandbox: a validator that ignores cancellation
can stall shutdown. `Health()` reports `supervisor_stalled` after ten seconds;
`Close()` waits for the owned callback rather than abandoning it.

## Failures, restart, and inspection

An individual check failure advances past its observed skill, allowing later
skills to be checked. The supervisor retains a sticky generic error and degraded
health even if later checks pass; `Close()` returns that error. A later cycle may
recheck the failed skill. Discovery/catalog errors cannot identify a trustworthy
next key, so they preserve the cursor and retry inspection. Corrupt shared catalog
structure or persistent metadata/secret-admission failures can therefore prevent
later skills from being checked. Failure isolation applies to ordinary callback
failures, not these unsafe discovery/admission cases. Unsafe cursor identities are
not returned. Health contains no scope, skill name, proof, or callback error text.

The scheduling cursor is in-memory, not a record of accepted work. Restart begins
from the current catalog and may repeat read-only validation. It never treats the
cursor as proof that rollback completed. The durable activation history is the
authority: after an interrupted rollback acknowledgement, a new pass inspects
the actually current version, rather than retrying a stale transition. A prior
version can itself fail a later check; exhausted rollback history requires
operator attention, not reactivation of a known regressed version.

For explicit bounded driving, `Client.SkillRegressionStep(ctx, after, validator)`
returns the next scheduling cursor and a generic error. Callers must retain the
returned cursor even when an individual check fails. `FileStore.ActiveStates`
offers read-only pages of exact active revisions, with limits 1–100; page cursors
are names, not concurrency preconditions. Inspect activation state/history to
see durable rollback evidence. The optional `skill_regression` health component
participates in readiness if a host includes it in its report; the SDK does not
automatically attach caller-owned monitors to the daemon's report.

## Qualification and remaining work

Tests exercise pagination past 100 entries, inactive and out-of-scope exclusion,
passing/no-write behavior, deterministic rollback, individual-failure isolation,
joined cancellation, policy gates, and restart-safe catalog revisions. These are
synthetic local fixtures, not evidence that an arbitrary validator detects
semantic or subjective regressions.

This implements periodic deterministic revalidation for Go hosts. Statistical
outcome-based regression detection still needs skill-version attribution,
baselines, minimum samples, and task-appropriate user-feedback weighting.
Standalone daemon validator configuration, persisted passing-check audit records,
durable scheduling fairness across frequent restarts, and cross-store power-loss
qualification remain open. No user monitor is enabled by this change.
