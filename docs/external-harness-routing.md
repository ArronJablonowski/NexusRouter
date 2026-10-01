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


## Durable outcome ledger

`harness.OpenEvidenceStore(absolutePrivateDirectory)` opens a separate version-1
SQLite/WAL ledger with FULL synchronization. It does not migrate or write the
runtime telemetry schema. The directory/database must be owner-private regular
filesystem nodes. Unknown schema identity and invalid existing files fail closed;
the store never repairs or resets them automatically.

A trusted host calls `AppendExecution(ctx, execution, now)` only after obtaining
actual model/harness/version/configuration provenance and the output digest from
its canonical runtime. `AppendReview(ctx, review, now)` requires authenticated
review provenance and the exact previous review ID. The store is not a public
JSON ingestion endpoint; persistence does not authenticate arbitrary self-reports.
Neither operation stores prompt/output bodies or turns successful execution into
a quality vote. Failed, canceled and indeterminate executions cannot receive
quality feedback.

Immutable IDs support exact retries; conflicting records fail. Review transactions
acquire a database write lock before checking the current head, including between
independent handles/processes. Each accepted revision is appended; withdrawals
preserve history. `Snapshot(ctx, now)` reads a consistent transaction and validates
the entire log with `harness.Replay` before it can be passed to `harness.Select`.
Only the current review contributes to ranking. Per-log limits and bounded record
sizes reject growth beyond the supported replay contract; no replay identities
are automatically pruned. Back up SQLite consistently and retain the ledger for
as long as its task/review identities can be retried.

Tests cover restart-equivalent selection, exact retries, conflicting IDs, revision
and withdrawal replay, concurrent independent writers (one expected-head winner),
failed-lineage zero feedback, canceled writes, malformed stored records and file
permissions. This is not physical power-loss qualification or proof of native
harness execution. SDK/runtime provenance ingestion, reviewer authentication,
native adapters and automatic selection wiring are still required under DAR-132.

## Native Pi RPC checkpoint

`harness/pi.Run` supports pinned Pi 0.99.2 in an isolated text-only RPC session.
The caller supplies the CLI artifact digest, explicit provider/model/base URL,
context/output limits, prices, deadline and a mandatory admission/reservation
callback. The callback must enforce destination privacy, credentials, resource
and cost policy before execution. Progress callbacks are trusted host code and
must honor their context. The host installation and Node runtime are trusted;
this adapter is not an operating-system sandbox.

The adapter writes private temporary model/auth/settings files, starts with an
empty working directory and sanitized environment, disables tools, extensions,
skills, project context, session persistence, compaction and retries, and removes
temporary credentials after process cleanup. API keys use the opaque auth-file
field, not command-interpolated model configuration. Admission is released once
on all post-admission exit paths. No inference is retried.

RPC state must match the configured endpoint, provider/model and token limits.
Only final assistant text with matching provider/model, a successful stop reason,
a completed turn and `agent_settled` yields a result. Prompt acceptance and
`agent_end` alone are insufficient. Tools, model changes, retries, malformed or
oversized streams and premature termination fail closed. Intermediate text may
be delivered as progress; it is not a final answer or quality evidence.

`NEXUS_PI_NATIVE=1 go test -race ./harness/pi` exercises the installed Pi against
an isolated local OpenAI-compatible streaming fixture, including cancellation,
single dispatch and reservation release. Protocol tests reject identity/config
changes, tool output, truncation, retries and invalid completion ordering. These
are native harness protocol tests, not actual-model accuracy measurements.

Still required: authorized tool-enabled coding execution, canonical runtime
identity/configuration and usage accounting, durable SDK task ownership,
authenticated ledger ingestion, automatic joint selection, remaining harness
adapters and held-out quality evaluation. This package is not yet wired into
production routing and makes no comparative accuracy claim.

Pi results also carry `harness.Identity` computed from the verified CLI artifact,
pinned harness/adapter version, actual protocol-checked provider/model and the
admitted effective endpoint/context/output/deadline/pricing/system settings.
`Config.ModelRevision` is required and must come from a trusted host inspection
of the deployed model revision: the adapter cannot infer weights from a model
name. Credentials are omitted from the identity; rotating a credential alone
neither loses nor manufactures model quality evidence. Changed deployment or
configuration identities do not borrow previous observations.

Reported usage preserves input/output/cache and optional reasoning/subset counts,
with bounded nonnegative counts and a consistent total. Pi may normalize missing
provider usage to zero; these are harness-reported values, never a claim of
provider measurement or zero billing. The normal accounting integration must
preserve that distinction. Native qualification verifies actual outgoing output
limits and a fixture-host path from completed output to the durable ledger:
completion remains pending, a separately bound exact-answer evaluation changes
ranking, and a new model revision starts without the old quality sample. This
exercises composition of the components; production SDK dispatch and ownership
integration is still outstanding.

## Durable native task execution

`pi.RunTask` wraps native execution with `runtime.RunHarness` using the host's
ordinary journal. The start commits with expected sequence zero before Pi can
launch. Reusing a task ID, including concurrent submissions, therefore fails
before a second native execution. The host must supply its normal submission-
fenced, redacting journal and maintain cancellation ownership; this embedding
API does not grant public dispatch authority or replace shared admission.

Successful terminal text and `harness.Execution` identity/output binding commit
in one event before the result returns. The host `OutputView` must match journal
redaction so the digest binds the delivered/evaluated text. Failed, canceled,
panicked or mismatched runs publish no accepted text or quality verdict. Failure
journals retain the admitted attribution but do not invent an actual-execution
record when the adapter could not establish it. Uncertain journal writes return
persistence failure; inspect the existing task rather than restarting inference.

`runtime.RecordHarnessOutcome` reads the canonical two-event task protocol,
checks start/terminal identity, task class, sequence, timestamps and output hash,
then idempotently appends the execution to the separate ledger. The reader must
be the trusted canonical journal, never imported caller JSON. No review or
quality vote is created. Native Pi qualification now exercises this path using a
temporary real telemetry database; it verifies one dispatch despite task reuse,
terminal binding, reconciliation retries and separately evaluated learning.

Still outstanding: normal SDK/API/CLI route selection and registration, shared
process/cost admission wiring, durable progress and usage accounting, recovery
of interrupted host ownership, tool-enabled harness authority and other adapters.
The embedding API is not a deployed replacement for those host responsibilities.

## Policy transport for native Pi

Adapter `pi-rpc-text-v2` requires an explicit host `http.RoundTripper` and
`TransportPolicySHA256`. The host must derive that digest from its effective
network/privacy policy. Both the policy identity and true upstream endpoint bind
the learning identity; a temporary gateway address does not.

Pi connects only to a temporary IPv4 loopback gateway using a random child token.
The gateway forwards one authorized streaming completion through the supplied
policy transport, substituting the provider credential only upstream. Providers
without authentication receive no invented credential. There is no default-
transport or direct-endpoint fallback, and redirects are not followed. The host
transport remains trusted code and must enforce endpoint/DNS/locality policy and
honor cancellation; the gateway is not a process sandbox.

The gateway checks the exact path, token, model, output ceiling and text-only
message contract, rejects tool/media operations, duplicate top-level keys,
unknown controls and provider-side storage requests, and bounds request/response
sizes. Nested JSON is normalized before forwarding to avoid parser disagreement.
A second valid request cannot issue another inference. Child headers and raw
upstream error bodies are not relayed. Shutdown cancels and joins active upstream
work before the Pi runner releases its admission reservation.

Installed native Pi streaming/cancellation and durable task/learning composition
pass with this gateway. Dedicated tests prove denied-transport no-fallback,
credential separation, one-dispatch semantics, redirect refusal, request-policy
rejection and cancellation/join. Normal SDK registration and admission wiring
remain required before this is offered as a configured production route.


## Explicit native Pi through SDK v1

`ConfigOptions.NativeHarnesses` registers operator-pinned model/harness pairs.
Each `sdk.NativeHarness` needs a unique `ID`, configured `ModelID`, `Kind: "pi"`,
absolute `Executable`, `ExecutableSHA256`, trusted `ModelRevision`, positive
`MaxOutputTokens` and `OverheadRAMBytes`, and an explicit `*pi.Prices`. Prices
and registrations are copied during construction. Zero prices must be an
intentional operator assertion, not a substitute for unknown cloud pricing.

Call `Run`, `RunStream` or `RunTextStream` with that `HarnessID` and the matching
explicit `ModelID`. The normal SDK configuration, secret resolver, privacy and
context policy, shared process admission and cancellation remain in force. The
reservation includes fixed local harness overhead even for a cloud model. Local
model context memory is sized before adding that overhead. Registration does not
start a process or grant task authority.

Only `openai_compatible` providers are currently supported; their configured base
endpoint is preserved exactly and `/chat/completions` is appended. In particular,
Ollama requires a native bridge preserving `options.num_ctx` and `num_predict`;
using its compatibility endpoint does not establish those allocation guarantees.
Do not register an Ollama route as a workaround for this missing bridge.

Adapter `pi-rpc-text-v3` forwards the host-assembled system/user/assistant messages
through the single-request policy gateway, retaining their roles. The durable
start records the same context; successful terminal events bind the delivered,
redacted text to `Result.HarnessOutcome`. `ReadEvents` exposes that canonical
outcome. Text streaming currently delivers the committed final text once; it does
not promise live token deltas. Unknown registrations, model mismatches, automatic
selection, queued submissions, continuation/compaction, evaluation workers,
delegation, tools and unsupported capabilities are rejected without a substitute
execution. Deterministic response-contract failure is terminal, without repair or
hidden inference retry.

`Result.Usage` remains absent: Pi's normalized harness-reported counters must not
be presented as provider-measured usage. Durable usage/cost accounting, ordinary
quality-review ingestion into the joint ledger, automatic model/harness selection,
CLI/API registration, native Ollama support, tools and other harness adapters
remain outstanding. A successful execution alone creates no quality vote.
