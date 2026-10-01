# External harness routing

DAR-132 requires joint model/provider and harness selection for Hermes Agent,
OpenClaw, Pi Agent, Goose and OpenHands. Correctness comes first, then output
quality. Cost is an eligibility constraint, not a reward that offsets accuracy.
The strongest eligible combination supported by evidence is not a claim of a
universal optimum.

## Implemented foundation (DAR-135)

The public Go `harness` package is an effect-free policy component. Hosts call
`Replay` with trusted execution and review records, then `Select` with freshly
checked candidates. It does not launch processes, grant authority, reserve
resources, persist evidence, or alter the existing model router.

An identity binds model, provider, model revision, harness and harness version,
adapter protocol/version, and a digest of relevant non-secret configuration.
Feedback belongs to the actual completed identity, including a fallback. Evidence
does not transfer across versions, profiles, task domains or difficulty classes.
Hosts must verify provenance; imported JSON and self-reported harness identities
are not authentication. A mutable provider model alias requires a trustworthy
deployment revision before its evidence can be reused.

Completed execution alone provides no quality credit. A current review must bind
the execution digest (which includes the output hash), method and method version,
reviewer, confidence, quality and verdict. Failed, canceled and indeterminate
executions cannot receive quality reviews. Exact duplicate records are no-ops;
conflicting identities and stale expected review heads fail closed. Revisions and
withdrawals leave at most one quality contribution per execution. Replay validates
an existing journal; the host still needs authenticated, serialized durable
appends with compare-and-swap and restart recovery.

Ranking uses Beta(1,1) prior-shrunk weighted correctness, then similarly shrunk
quality, evidence coverage, and a stable identity tie-break. The default half-life
is 30 days and coverage target is 20 weighted samples. These are initial policy
settings, not calibrated probabilities or statistical confidence intervals.
Automated AI reviews receive one quarter of their stated confidence weight and
are reported separately from deterministic/human observations. Review revisions
cannot make an old execution recent. Sorted evidence accumulation makes exact
replay deterministic.

Availability, authorization, compatibility, credentials, locality/privacy,
capability, context capacity, resource availability and cost are checked before
ranking. Candidate facts must cover the harness's tools and egress as well as the
model. Authorization and reservations must be checked again at dispatch.
Ordinary requests never explore. Separately budgeted evaluation requests can
explicitly opt into a policy probability capped at 25%, selecting a less-observed
eligible alternative. A selection exposes exclusions, sample counts, decayed
weights, last evidence, uncertainty and an exploration reason.

## Native adapter qualification still required

| Harness | Upstream interface to qualify | Important boundary |
| --- | --- | --- |
| [Hermes](https://hermes-agent.nousresearch.com/docs/reference/cli-commands) | Query-file stdin and structured CLI events | Verify actual provider/model and fallback provenance; restrict tool and resource authority. |
| [OpenClaw](https://docs.openclaw.ai/cli/agent) | Isolated one-shot `agent exec` | Qualify explicit auth, tools, sandbox and terminal result semantics on the pinned installed version. |
| [Pi](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/rpc.md) | RPC JSONL | Prompt acceptance is not completion; qualify settled terminal events, retries and cancellation. |
| [Goose](https://github.com/aaif-goose/goose/blob/main/documentation/docs/guides/running-tasks.md) | Stdin tasks and structured run output | Qualify extension/tool authorization and actual completion identity. |
| [OpenHands](https://docs.openhands.dev/openhands/usage/cli/headless) | Headless JSON output | Headless auto-approval requires an independently enforced execution boundary; unsupported policy must reject. |

None of these adapters is advertised as executable by this foundation. Remaining
work includes durable task ownership and evidence storage, typed progress/results,
bounded cancellation and process cleanup, explicit capability rejection, policy
propagation, outcome evaluators, and integration into SDK/CLI/API routing. Native
execution must not be disguised as a provider that bypasses the host tool loop.

Controlled tests cover model/harness interaction, task specialization, decay,
regression, replay, fallback attribution, identity isolation and admission.
They are policy tests, not real model accuracy measurements. Real native adapter
tests and disjoint held-out outcome qualification remain necessary before routing
user tasks or making comparative performance claims.
