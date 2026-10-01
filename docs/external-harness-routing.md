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


## Explicit native harnesses through SDK v1

`ConfigOptions.NativeHarnesses` registers operator-pinned model/harness pairs.
Each `sdk.NativeHarness` needs a unique `ID`, configured `ModelID`, `Kind: "pi"`
or `Kind: "openclaw"`, `Kind: "hermes"`, `Kind: "goose"`, or `Kind: "openhands"`,
absolute `Executable`, `ExecutableSHA256`, trusted `ModelRevision`, positive
`MaxOutputTokens` and `OverheadRAMBytes`, and an explicit `*sdk.NativeHarnessPrices` (the earlier `*pi.Prices` remains
compatible). Prices
and registrations are copied during construction. Zero prices must be an
intentional operator assertion, not a substitute for unknown cloud pricing.

Call `Run`, `RunStream` or `RunTextStream` with that `HarnessID` and the matching
explicit `ModelID`. The normal SDK configuration, secret resolver, privacy and
context policy, shared process admission and cancellation remain in force. The
reservation includes fixed local harness overhead even for a cloud model. Local
model context memory is sized before adding that overhead. Registration does not
start a process or grant task authority.

Both `openai_compatible` and native `ollama` providers are supported. The configured
base endpoint is preserved: `/chat/completions` is appended for OpenAI-compatible
providers and `/api/chat` for Ollama. Use the actual configured provider kind;
Ollama context allocation is enforced through its native options, not inferred
from compatibility-endpoint metadata.

Adapter `pi-rpc-text-v4` forwards the host-assembled system/user/assistant messages
through the single-request policy gateway, retaining their roles. The durable
start records the same context; successful terminal events bind the delivered,
redacted text to `Result.HarnessOutcome`. `ReadEvents` exposes that canonical
outcome. Text streaming currently delivers the committed final text once; it does
not promise live token deltas. Unknown registrations, model mismatches, queued submissions, continuation/compaction,
delegation, tools and unsupported capabilities are rejected without a substitute
execution. Deterministic response-contract failure is terminal, without repair or
hidden inference retry.

`Result.Usage` remains absent: Pi's normalized harness-reported counters must not
be presented as provider-measured usage. Durable usage/cost accounting, ordinary
broader model/harness selection across adapters,
CLI/API registration, tools and other harness adapters
remain outstanding. A successful execution alone creates no quality vote.


## SDK outcome reconciliation and evaluated feedback

The embedding host opens a private `harness.EvidenceStore` and owns its lifetime.
After an SDK task completes, call
`client.ReconcileHarnessOutcome(ctx, ledger, taskID)` to copy the canonical
completed outcome into that ledger. The SDK reads its configured task journal;
it accepts neither caller-provided execution identity nor an imported output.
Reconciliation is idempotent and never performs inference or assigns quality.
If a write is uncertain, retry reconciliation rather than rerunning the task.

After evaluating that exact output, an authenticated operator/evaluator may call
`client.ReviewHarnessOutcome(ctx, ledger, taskID, review)`. The review must bind
`ExecutionDigest` to the canonical outcome, identify its actual method, reviewer
and rubric version, and use the exact `ExpectedHead` for a revision. The embedding
host must authenticate the reviewer and method; a string identifying a human or
AI is not authentication. Never expose this method as a model tool or unprotected
remote endpoint. Failed, canceled, incomplete and non-harness tasks are rejected.
The existing model-only `Feedback` API does not stand in for this joint evidence.

Identical retries preserve one vote, including replay after a later revision;
withdrawal removes the active vote without rewriting history. The existing
`Snapshot` and accuracy-first `harness.Select` consume these current heads with
method-specific confidence and exact model/harness/configuration/task binding.
This SDK bridge makes evaluated outcomes available to selection. Opt-in automatic
evaluation and registered-pair SDK selection are described below. A completed run still contributes no quality sample until an
actual bound evaluation is supplied.


## Native Ollama protocol bridge

Adapter v4 binds `UpstreamProtocol` into the learning identity. SDK registration
selects it from the configured provider kind. Ollama requests use the native
[chat API](https://docs.ollama.com/api/chat) with the allocated `options.num_ctx`,
bounded `options.num_predict`, text message roles and `think: false`. Smaller
child output limits stay smaller; conflicting limits and unsupported controls
reject. The bridge leaves `keep_alive` unspecified and never unloads a resident.
It retains the same policy transport, upstream-only credentials, shared admission,
single dispatch, no redirects/fallback, deadline and cancellation cleanup.

The bridge buffers a bounded native NDJSON response through its terminal record
and EOF before emitting successful SSE framing for Pi. It requires the exact
configured model and a normal stop. Truncation, extra records after completion,
model mismatch, tools/images/thinking output, malformed or oversized records,
provider errors and partial/invalid usage counts cannot produce an accepted
answer. Absent counts remain absent in translated usage; the SDK still does not
label Pi-normalized counters as provider-measured. This is final-output delivery,
not live token streaming.

Installed-Pi SDK qualification uses a fixture native Ollama server and verifies
context/output/credential preservation, completed task provenance, wrong-model
and truncated-stream rejection, cancellation and exactly one upstream request.
It does not establish model quality or compatibility with every deployed Ollama
model; model revision, capability and resource metadata remain host obligations.


## Automatic selection among registered native pairs

Pass the open host-owned ledger as `ConfigOptions.HarnessEvidence`, then request
`HarnessID: "auto"`, `ModelID: "auto"` (or empty), an explicit `ContextTokens`
at least 8192, and a finite `MaxCost`. For automatic selection, zero means a strict
zero-cost ceiling. Use stable domain/profile metadata; difficulty is currently
`unknown`. Selection never borrows observations from another profile, context-
bound configuration, model revision or adapter version.

Each request reads the current ledger snapshot, verifies pinned harness artifacts,
and refreshes provider model inventory through the host policy transport. Only
privacy/mode/budget/credential-eligible endpoints are queried. The ranker applies
correctness first, quality second, confidence next; price cannot outweigh stronger
accuracy evidence while within budget. No-evidence ties are explicitly labeled,
and ordinary requests never perform exploratory inference.

The chosen pair goes through shared model-plus-harness admission. If capacity is
unavailable before any task starts, that pair is excluded and the remaining pairs
are ranked again. Missing resource data is an error. Once an attempt exists,
provider/protocol failures do not silently dispatch a substitute or repeat the
inference. No model-only fallback is used for a native-pair request.

`Result.HarnessSelection` explains the decision; the same selection is bound to
identity and task class in the durable `TaskStarted.Harness.Selection` record.
ReadEvents and canonical outcome reconciliation validate those bindings. Reviews
written through the SDK affect later selection immediately, including after SDK
restart; unreviewed outputs never acquire a success vote automatically.

Current registered adapters are Pi, OpenClaw, Hermes and Goose text-only routes, with native Ollama or
OpenAI-compatible providers. This is selection among the registered eligible
pairs, not a claim of the globally best model/harness or a comparative ranking
against the unimplemented OpenHands adapter. Additional adapters, tools, queued registration authority,
CLI/API configuration, durable usage/cost accounting and held-out qualification
remain required for the full feature.


## Automatic advisory evaluation and learning

The existing `evaluation.judge`, `evaluation.auto_review_model` and bounded
`evaluation.auto_review_max_cost` settings now apply to completed native harness
runs. Supply `ConfigOptions.HarnessEvidence` to ingest the resulting evaluated
feedback; without it the audit remains durable and learning reports
`not_configured`. The reviewer can use the ordinary independently admitted model
path or the configured `ConfigOptions.Evaluator` extension. These are operator
configuration choices; no reviewer, provider or live configuration is enabled by
the library automatically.

Native audits use the existing durable RunAudit workflow. `SourceKind: "harness"`
means the legacy `SourceAttemptID`/`AttemptID` field holds the exact canonical
execution digest; it does not claim a provider-turn identity. Admission and audit
commit independently verify the canonical start/completion/output binding.
Started but interrupted reviews stay inspectable and never silently reinvoke.
Failed and incomplete native tasks are ineligible. Existing provider-turn audits
keep their empty source kind and original semantics.

Automatic review uses the stable idempotency key
`native-auto-review-v1:<taskID>`. `Result.HarnessAuditOperationID` supports
InspectAudit/ReadAuditEvents; `Result.AuditID` remains the audit record ID.
`ReconcileHarnessAudit(ctx, ledger, taskID, operationID)` can repair ledger delivery
without repeating candidate or reviewer inference. Review identity/time derive
from the persisted audit, so identical retries do not add weight.

`HarnessReview` and `HarnessReviewStatus` report learning separately from execution.
A successful candidate remains a completed execution when the evaluator rejects,
abstains or fails; the returned text is not a quality guarantee. Accepted/rejected
audits become `automated_ai` evidence with capped advisory weight, including when
an in-process evaluator produced them. Quality is currently the binary verdict
(1 for accept, 0 for reject), not an invented multidimensional rubric score.
Abstention/zero confidence yields unverified evidence; evaluation failure yields
no quality vote. An existing different operator review head causes `conflict`
rather than silent supersession. Ledger failures report `failed`; retry audit
reconciliation, not Run. Automatic reviews never claim human or deterministic
provenance. Human/deterministic feedback continues through the explicitly trusted
review API when the host has independently established that evaluation method.


## Native OpenClaw through SDK v1

Register `Kind: "openclaw"` for the pinned OpenClaw 2026.9.7 installation. The
same explicit and automatic SDK requests, provider-policy transport, context
assembly, fixed harness memory reservation, output contracts, redacting journal,
canonical evidence reconciliation and advisory review apply. Different harnesses
may register the same model: their separate version/config identities retain
separate task results. A fresh automatic selection uses current review heads;
changing a review does not rerun its original inference.

The adapter runs isolated `agent exec`, disables tools/plugins/skills and automatic
updates, and independently verifies the provider response before accepting the
native output. Both OpenAI-compatible and Ollama protocols are supported. Context
and output caps are explicit. A successful OpenClaw envelope alone is insufficient:
the runner also requires exact model identity, normal provider termination, clean
stream completion, process exit and output binding. No usage normalized by the
harness is represented as measured SDK accounting.

OpenClaw process ownership is currently implemented for macOS/Linux only. Its
launcher is pinned by SHA/version; Node and installed modules are trusted host
dependencies, not an attested sandbox. Unsupported platforms, tool-bearing runs,
queue submission and continuation are rejected. Registrations are constructor
options and do not alter the user's live Gateway or configuration. Native tests
use disposable configuration and fixture providers; they do not establish a
real-world accuracy ranking. OpenHands integration, tool support,
queue/CLI/API registration and held-out comparative qualification remain open.


## Native Hermes through SDK v1

Register `Kind: "hermes"` for Hermes 0.21.5+4983.g6633626. `Executable` must be
the installed dependency-environment Python interpreter (not the shell launcher),
with its SHA256. Set `HermesSourceDir` to the pinned, tracked-clean checkout and
`RuntimeSHA256` to the host-attested digest of the installed dependency manifest.
That digest participates in the learning identity; it does not independently
attest every imported package. Source and dependencies remain trusted host code.

Explicit execution, automatic pair selection, canonical evidence reconciliation
and advisory reviews use the same SDK policy paths as Pi/OpenClaw. Native Hermes
metadata does not establish actual completion: the gateway independently verifies
the model response and matches final text. The host supplies any omitted token
limit, replaces messages with assembled context, and permits at most one upstream
dispatch. Local metadata probes cannot forward to the provider. OpenAI-compatible
and native Ollama backends are qualified with fixtures.

Each invocation has a private home, no tools/plugins/MCP/memory, disabled title
model calls and disabled lazy installation. macOS/Linux process-group cleanup
and provider-handler joining precede reservation release. Output and execution
are bounded; failure does not produce an accepted result. Tool-bearing execution,
queue/CLI/API registration, measured usage accounting and broader platform
qualification remain incomplete. Fixture review labels test selection behavior,
not comparative real-world model/harness quality.


## Native Goose through SDK v1

Register `Kind: "goose"` with the absolute pinned Goose 1.52.0 binary path and
its SHA256. The adapter uses a private GOOSE_PATH_ROOT, no profile extensions,
no saved session, one turn, stdin instructions and quiet stream-json output.
It does not install or update Goose, inherit provider credentials, or configure
the user's existing sessions. Configuration isolation is not an OS sandbox.

The gateway supplies host context and missing output caps, routes through the
host policy transport, and requires verified normal completion before matching
the native text. OpenAI-compatible and native Ollama protocols are qualified.
The child always sees the ephemeral local OpenAI-compatible gateway; canonical
identity records the actual host-configured provider/model and Goose version.
Native metadata or normalized usage alone cannot establish model success.

Explicit SDK routing, automatic pair selection and bound advisory feedback use
the shared evidence paths. Cancellation joins provider handlers and cleans up
owned process groups before release. Tool-bearing runs, queued and CLI/API
registration, measured usage accounting, broader platform and held-out accuracy
qualification remain open. Process ownership currently supports macOS/Linux.


## Native OpenHands SDK through SDK v1

Register `Kind: "openhands"` for official `openhands-sdk` 1.50.1. `Executable`
is the absolute Python interpreter path in the installed isolated environment;
`ExecutableSHA256` pins that interpreter and `RuntimeSHA256` is the host-attested
dependency manifest digest. SDK package files and host libraries remain trusted.
The adapter verifies the SDK version and binds the embedded bridge digest into
its learning identity. This uses the native OpenHands SDK, not the CLI, whose
current new-conversation path restores default tools.

The bridge uses an empty tool list including builtins, no MCP, condenser or
critic, zero LLM retries, one iteration and private state. Host context, output
limits, privacy transport and model admission remain authoritative. The verified
gateway supports OpenAI-compatible and Ollama providers; child-local gateway
identity is never substituted for actual host provider/model attribution.
Cancellation joins provider cleanup and owned process groups before releasing
resources. This is configuration isolation, not an OS sandbox.

Explicit and automatic SDK routes use canonical evidence and current review
heads. Native fixture tests qualify host-context delivery, durable outcomes,
OpenHands/Pi selection reversal, and advisory accept/reject/abstain/failure
handling. Set NEXUS_OPENHANDS_PYTHON and NEXUS_OPENHANDS_MANIFEST to the pinned
interpreter and dependency manifest for native tests; Pi pair tests also require
NEXUS_PI_NATIVE=1. Fixture scores are not comparative quality qualification.
Tools, queued/CLI/API registration, measured usage, wider-platform and held-out
accuracy qualification remain incomplete.
