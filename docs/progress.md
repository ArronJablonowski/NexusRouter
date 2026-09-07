# Implementation evidence

Provider-contract and eligibility continuation: DAR-8 is now Done in native
Linear with evidence comment `7bed654b`; its state activity remains
`d8def7c5-8d7e-42d4-a7be-56d9bef19b58` because Linear consolidated the Todo to
Done history. Linear released DAR-9, DAR-10, DAR-16 and DAR-19 automatically.
The exact tested checkpoint is GitHub-backed `9518561` with matching local and
remote heads and zero divergence.

DAR-10 then moved through In Progress to Done under state activity
`62dd15d7-faa7-43de-9141-b8718c6878ca`; its completion comment was posted and
read back. The provider-neutral contracts carry streaming text, structured JSON
schema requests, typed tool-call proposals, usage and finish data. Context
cancellation propagates, bounded model discovery is the provider/model health
probe used by application health, and normalized failures retain safe code,
partial-output and retryability facts without raw adapter errors. The full
provider race suite passed; explicit/automatic/child execution and health-factory
integration passed three race repetitions in 2.915s. Linear released DAR-11,
DAR-12, DAR-13, DAR-23, DAR-29 and DAR-31.

DAR-23 is now In Progress under state activity
`b12799c3-3f42-41d4-95c6-374b1223bb35`. Review confirmed hard filtering for
mode, privacy, health, policy, capacity, context, cost budget and capabilities,
but found required credentials were being collapsed into the generic `policy`
route explanation. Candidate contracts now carry only credential-required and
credential-available booleans. A missing required credential excludes the model
with reason `credential`; the environment-variable name and value are not stored.
The application no longer mislabels this condition as a policy denial and never
constructs the unavailable provider. Routing tests include the new hard filter;
an application/SQLite fixture proves a healthy credential-free route is selected,
the secured route is excluded explicitly, and its credential configuration is
absent from the durable route event. Focused routing and application race tests
passed. Final `make check` passed format/LOC enforcement, vet, the complete native
race suite and build; application tests took 222.050s, telemetry 147.660s, CLI
43.102s, SDK 26.395s, tool-gate 19.666s and runtime 9.897s. Backup and Linear
completion evidence follow; the full PRD remains incomplete.

Resource and canonical-event acceptance checkpoint: native Linear DAR-22 is Done
after review of both acceptance criteria. Its state activity is
`4f25bcae-ed47-46b3-8ce3-4403714c3d82` and evidence comment `355ad15a` was
posted and read back. Linear automatically released DAR-23, DAR-27, DAR-37 and
DAR-44. Existing resource implementation profiles CPU, RAM and swap, reports
Apple unified memory and Foundation thermal state, applies visible Linux cgroup-v2
CPU/RAM constraints and thermal trip-point observations, and surveys NVIDIA/AMD
VRAM per device without aggregating distinct accelerators. Injected proc, sysfs,
command, cgroup and thermal probes provide hardware-independent fixtures.
`go test -race ./resources` passed, and the read-only CLI on this Mac reported
16 CPU threads, 48 GiB unified memory, swap usage and nominal/false OS thermal
pressure. Discrete GPU sources were explicitly unsupported. Null observations
remain unknown; this live check does not qualify physical temperature, Linux GPU
hardware, every container hierarchy or OS-enforced isolation.

DAR-23 remained blocked by DAR-10, which remained blocked by DAR-8, so work
followed the dependency chain instead of bypassing it. DAR-8 is In Progress in
native Linear under state activity `d8def7c5-8d7e-42d4-a7be-56d9bef19b58`.
The canonical version-1 runtime protocol already defines immutable typed events
for task, turn, model delta, tool, worker lifecycle, route selection, evaluation,
error and steering. Envelope validation requires task/session/correlation/order
identity and each event family requires its specific identity or outcome fields.
The focused test now positively constructs, validates, encodes and JSON-round-
trips every canonical kind, checks that the test table itself has no duplicate
kinds, and retains the existing malformed-envelope cases. The focused runtime
race suite and source quality check passed. Final `make check` also passed
format/LOC enforcement, vet, the complete native race suite and production
build; application tests took 221.844s, telemetry 148.124s, CLI 42.362s, SDK
24.967s and tool-gate 19.467s, with unchanged packages cached. GitHub backup and
the native Linear completion comment follow before DAR-8 is closed.

Foundation acceptance audit: work resumed from clean, GitHub-backed `c040fcc`.
Native Linear DAR-5 (Initialize Go module and quality gates) and DAR-6 (Define
versioned configuration schema) are now Done after direct acceptance review; no
production code or schema changed for these two audits. Their completion evidence
was posted and read back in the authenticated native Linear app. DAR-5 activity
is `ca6b995e-5912-4e4c-aff7-c79501419d8b` with evidence comment
`c03ee863`; DAR-6 activity is `05b1ae1e-a66e-40dc-a97f-65e4c979ac17`,
with its evidence comment verified immediately after posting. Linear released
their outgoing dependency links automatically.

DAR-5 evidence: `go.mod` pins the module and Go version; the committed GitHub
workflow runs the repository checks on macOS and Linux; `make check` runs format
and 1,000-line enforcement, vet, the complete race-enabled test suite, and a
production build. Generated code is excluded by the quality checker. The local
equivalent passed at `c040fcc`; remote GitHub Actions UI execution was not claimed
because the local GitHub CLI was not authenticated and the repository API did not
provide a public run listing.

DAR-6 evidence: configuration resolves defaults, user file, project file,
`DARWIN__` environment values and flags in increasing precedence. Lower layers
are validated before merge, maps merge recursively, and lists replace. Schema
version 1 supplies safe defaults and strict validation, including hostile YAML,
type, bound, identifier, deployment, resource, model, tool, memory, skill,
evaluation and telemetry cases. Redacted display operates on a copy and removes
provider endpoints/executables, database/export endpoints and local roots;
credentials remain environment-variable references and are never loaded into
settings. Tests cover precedence, explicit zero, list replacement, invalid
shadowed layers, file/size limits, literal overrides and non-mutating redaction.
The full PRD and backlog remain incomplete; DAR-22 is the next unblocked
dependency selected for acceptance review.

Daemon crash/restart qualification: this turn resumed from clean, GitHub-backed
`a6e38ff`. Native Linear DAR-36 moved from Todo to In Progress. Existing daemon
implementation already performs configuration, listener, database, provider,
resource and supervisor startup checks; exposes detailed readiness; supports
authenticated managed start/status/stop; and joins HTTP, dispatcher, learning and
metrics services during cooperative shutdown.

A new Unix process test builds and launches the actual DarwinRouter executable,
waits for validated readiness, submits detached model work, and observes the
provider receive its request. The provider emits a partial delta and blocks. The
test kills and joins that exact daemon with verified SIGKILL, observes provider
disconnect, and advances only the owned fixture's submission-claim expiry. A
second fresh daemon process opens the same database, reaches readiness, and runs
the production dispatcher recovery sweep. The original task/submission become
failed with one `interrupted_model` receipt, one total provider call, empty result,
an interrupted turn, preserved user context and no partial assistant message.
The restarted daemon then exits through its authenticated graceful-stop path.

The final focused native race test passed three runs in 5.565s. The final test
also passed three times as a CGO-free Linux ARM64 binary in an unprivileged,
read-only, network-disabled container. This qualifies actual daemon process death
and restart-safe recovery for model-only interrupted work using a loopback
provider and deterministic one-byte model footprint. Linux was not race-
instrumented. It does not add or prove an OS crash-restart supervisor, automatic
retry, live-provider compatibility, wall-clock lease waiting, power-loss durability,
remote workers or arbitrary side-effect recovery. Final `make check` passed after
all review changes: format/LOC enforcement, vet, the full native race suite, and
production build. App tests took 220.401s, telemetry 146.082s, CLI 41.548s, SDK
24.885s, and toolgate 19.653s; unchanged packages used cached results. Both exact
DAR-36 acceptance criteria are now proven for the documented local daemon scope,
and implementation checkpoint `820f70064393d66cb88e8867ca1c1d5bee61d137`
is backed up to `origin/main` with matching heads. Native Linear DAR-36 is Done;
its completion comment was posted and read back with the commit, verification and
limitations. Linear released its outgoing blocking links without manual relation
edits. The full PRD remains incomplete.

Process-separated recovery continuation qualification: this turn started clean
from GitHub-backed 66295c9, a verified implementation checkpoint. The existing
owned-process model-crash test now covers explicit continuation in a second
fresh subprocess. The first process is joined with verified SIGKILL after a
durable model delta; its provider disconnect is observed before recovery.
The original stale-owner rejection and idempotent recovery checks remain.

The second process loads the exact configuration/source ID from an allowlisted
environment and reports only a bounded task ID or explicit ErrAdmission denial.
For a recovered failure, the test requires exactly one new inference with the
original user message plus explicit follow-up, excluding partial output. It
replays the new completed task and checks parent, session, privacy, model,
provider and durable final answer. For a canceled source, it requires no new
task and no additional inference. Full source journal, submission status and
recovery receipt remain identical after continuation and another recovery pass;
total task counts exclude hidden extra attempts. Both children are joined.

Final focused native race tests passed three runs in 9.616s. The same final test
also ran three times as a CGO-free Linux ARM64 binary in an unprivileged,
read-only, network-disabled container, passing both recovered and canceled
cases. The providers and hardware profiles are deterministic fixtures; lease
expiry is advanced in owned storage. This is actual process-death/separation
and durable-context evidence, not live Codex/Ollama, real pressure, automatic
daemon restart, arbitrary side effects or full crash-window qualification.
No production implementation or schema changed in this checkpoint. Independent
read-only review found no blocker. Native Linear DAR-43 is now In Progress;
its broader provider/tool/fitness/compaction/skill/worker acceptance remains open.

Final `make check` passed: formatting and 1,000-line enforcement, go vet,
the full race-enabled suite, and go build. App tests took 224.480s, telemetry
150.231s, CLI 41.579s and SDK 25.468s; unchanged packages used cached results.

Recovered model-only continuation checkpoint: the previous goal turn made
verified progress and was backed up as 99637d1. Current Linear DAR-14 requires
queued steering/follow-up, cancellation and safe resumption from durable state;
it has been moved from Todo to In Progress, not marked complete. This checkpoint
allows explicit conversation continuation after an exact model-only interruption
recovery. It does not rerun the old task or broaden uncertain-effect recovery.

The shared sessions.ReplayContinuation captures one owned, bounded journal
(10,000 events / 8 MiB including the recovery terminal). It preserves ordinary
completed/recovered-delegation assessment and admits failed `recovered_model`
history only when recomputing PlanInterruptedModel reproduces the exact stored
terminal. Partial deltas are not imported; raw failed state and InterruptedTurn
remain unchanged. Current tools/effects, canceled tasks, ordinary failures and
read-only tool-recovery protocols do not qualify under this model-only path.
Historical paired tools remain context, never current execution authority.

Application admission and metadata inspection now use the same SQLite read
transaction with raw payload-size checks before loading bodies. A review of
the initial integration found app admission still used the unbounded raw event
reader; ContinuationSnapshot now replaces that path. All continued tasks still
pass normal privacy, provider, context, resource and tool-policy admission.
Failed sources reject compaction and stored-summary options. No database schema
or recovery writer protocol changes; no automatic inference or skill update.

Tests cover exact inclusive event/aggregate-byte limits and one-over rejection,
replay-valid forged recovery terminals, source ownership, cancellation, current
tool rejection, pre-dispatch/partial/completed-turn boundaries and historical
tool context. The real application fixture commits a model-only journal into
an owned submission, expires its fixture claim, executes the existing recovery
transaction and creates a fresh explicit continuation through loopback HTTP.
It proves no inference during recovery/inspection or privacy/compaction denial,
exact complete-message import without partial output, new parent/session/privacy
binding, unchanged failed source events and unchanged failed submission state.
Storage tests retain raw InterruptedTurn even when readiness is true. HTTP
metadata and chat selection tests accept the new reason without granting retry
authority. These are fixtures, not a new live Codex/Ollama recovery run, general
automatic resume, semantic output acceptance or complete PRD qualification.

Final verification: make check passed format/LOC, vet, the full native race
suite and build after the shared transaction refinement (app 227.288s,
telemetry 152.197s, CLI 41.166s, API 12.804s, SDK 26.136s, sessions 16.610s;
unchanged packages cached). make build passed. Final focused app/storage race
tests passed three repetitions in 3.502s/7.951s; strengthened sessions tests
passed three race repetitions in 10.818s, chat resume in 2.462s, and HTTP
metadata in 1.757s. Final app continuation and sessions boundary tests also ran
three times as CGO-free Linux ARM64 tests in an unprivileged, read-only,
network-disabled container. No Linux race or live-provider result is claimed.
Independent final review found no concrete blocker. Source files remain below
1,000 LOC; GitHub backup and a native Linear evidence comment follow this gate.

Interactive saved-context selection checkpoint: the preceding goal turn
confirmed authentication without advancing implementation. Work resumed from
clean GitHub-backed c968c8b. Chat now accepts `/resume TASK_ID` while idle,
using the existing read-only, replay-validated continuation assessment against
the configured database. Matching valid eligible metadata is required; canceled
inspection, unknown/corrupt/ineligible history and active work reject without
changing context. Selection does not dispatch inference or restart old work.
The next ordinary prompt creates a parent-linked task through normal admission,
including privacy, capacity, provider and tool policy. A successful selection
clears source-specific compaction/summary options and feedback targeting;
`/new` clears the selection. Failed selection preserves the previous context.

Focused race tests passed three repetitions in 2.523s. Unit fixtures cover
completed/recovered selections, deferred execution, invalid IDs and status,
foreign responses, backend failure, active-work rejection, canceled inspection,
compaction/feedback clearing and `/new`. Real RunWithInput tests use temporary
YAML, SQLite, actual input pipes and local HTTP inference fixtures with no chat
hooks: selection causes no model call or new task, the next request contains
the saved user/assistant messages, replay proves the new completed parent link,
and source events remain unchanged before/after selection and continuation.
Missing-store selection leaves the database absent. These fixtures are not new
live Sol/Ollama tests or automatic interrupted-task recovery qualification.
The user unlocked the Mac during this checkpoint. Native Linear then confirmed
DAR-38 (Build interactive and JSON task CLI) is In Progress and includes events,
cancellation, steering, feedback and session resume in its acceptance criteria.
Full interrupted-task resume and full PRD qualification remain open; this
checkpoint does not mark the issue complete.

Verification: make check passed formatting/LOC, vet, full native race suite and
build (app 221.709s, CLI 40.471s, telemetry 148.386s, API 12.947s, SDK 25.112s;
unchanged packages cached). make build passed. Final review corrected a test's
invalid-ID disclosure sentinel; the final focused native race suite passed
three times in 2.596s. Final resume tests also executed three times as CGO-free
Linux ARM64 tests in an unprivileged, read-only, network-disabled container;
this is not Linux race or live-provider qualification. The native app now shows
a posted DAR-45 comment linking c968c8b's previously verified live Sol/Ollama
steering evidence with its limits. Issue statuses and dependencies are retained.

Live chat steering checkpoint: the previous goal turn made verified progress
and was backed up as 4ac2c71; this turn started clean. A new opt-in CLI subprocess
qualification enters production RunWithInput/runChat without injected hooks or
synthetic providers. It clones the reviewed Sol/Gemma smoke configuration into
an owned temporary database and resolves the installed Codex executable. A
prelaunch guard pins model/provider identities, local endpoint and budgets, and
rejects extra tools, credentials, memory, learning, judges/reviews or export.
Child environment is allowlisted, input/output are real pipes, output is bounded,
and cleanup signals/joins only the owned child. Default checks skip live inference.

The first live run passed on September 6, 2026 in 22.02s (package 23.546s):
signed-in Codex CLI 0.153.4 / exact gpt-5.6-sol delegated once to actual installed
Ollama gemma4:12b-it-q4_K_M. At the unprefixed delegate-start status, the test sent
/steer guidance. Reopened SQLite proved three completed tasks with parent/work/
execution lineage and matching delegation origin, exact parent/local model
attribution, one tool pair and one accepted worker result. The local completed
turn's output matched the published work result and passed nonempty/Go syntax
evidence plus independent syntax validation. The steering creation timestamp lay
between tool start and completion; its committed application followed the tool
result and preceded another coordinator turn. The durable final answer exactly
matched the revised marker. Rendered queue/application/completion counts agreed.

Pre-run review corrected the verifier to use final TurnCompleted text rather
than empty TaskCompleted data, and actual deterministic journal events rather
than assuming evaluation-table rows. It also required durable final-answer
equality instead of accepting a marker merely echoed in output. No user feedback
or evaluation rows were fabricated. Final independent review found no remaining
concrete blocker. Fast default tests cover authority mutations, status parsing
and output bounds; no positive synthetic journal-verifier fixture is claimed.

Only synthetic prompts/guidance and generated local output reached cloud
coordination. Existing configuration/task stores were untouched; no model pull,
file tool or background learning ran. The subprocess exercised the real CLI
entry point in a test executable with pipes, not a packaged binary/manual TTY
usability test. Syntax acceptance did not compile or execute the generated Go.
See docs/live-chat-steering.md for reproduction. This single run is not an SLA,
general obedience, crash-window or production qualification. Native Linear is
still locked; no issue update is claimed. Full PRD/Linear acceptance remains open.

Live-chat checkpoint verification: make check passed formatting/LOC, vet, full
native race suite and build (app 218.504s, telemetry 148.706s, CLI 40.150s,
API 13.137s, SDK 25.660s; unchanged packages cached). Focused final authority/
render tests passed three race-enabled repetitions in 1.811s; final format/vet
checks passed. Those default tests also executed three times as a CGO-free Linux
ARM64 binary in a read-only, unprivileged, network-disabled container with only
the test artifact and reviewed sample mounted. No live Linux inference or Linux
race result is claimed. The final post-run addition only logs bounded validated
counts after successful qualification; it does not alter acceptance conditions.

Codex boundary-steering checkpoint: the preceding goal turn made verified
progress and was backed up as 84b44d6; this turn started clean. Inspection found
that line-oriented chat already supports interactive guidance, while the PRD's
claim that an interactive steering CLI was missing was stale. The actual gap
was native coordinator continuation after durable guidance. The PRD now reflects
existing chat and the newly implemented native adapter behavior.

Pending tool continuations admit only exact prior history/catalog/schema/model,
the emitted assistant proposal, its paired result, then bounded plain user
guidance. A matching turn/steer acknowledgement is required before releasing the
native tool response once. Completed native segments instead require the exact
prior conversation and assistant answer before starting a new turn in the same
thread. Neither path recreates the process/thread, reinjects history, executes
tools or changes runtime authority/budgets. Lifetime wire/item limits and
cumulative usage are retained; 32 total guidance messages and a 1MiB complete
control-frame bound fail closed without truncation or automatic retry.

The OpenAI Docs skill guided use of official app-server steering semantics;
installed CLI 0.153.4 generated types independently confirmed expectedTurnId and
the turnId acknowledgement. Pre-acknowledgement notices are restricted to checked
compatibility/status, same-turn user-item lifecycle and monotonic usage. Failures
close the session and leave already completed tool work in the durable journal.
Independent review prompted pre-ack usage coverage; further tests cover strict
history and identity bindings, failed tool results, limits and malformed notices.

Real signed-in gpt-5.6-sol smoke tests passed both paths on first attempt:
one-thread/two-turn completed-boundary guidance and one-thread/one-turn paused
synthetic-tool guidance, with exactly one steer and one tool reply in the latter.
Both returned the revised marker with no extra proposal. Total test duration was
15.60s (package 16.951s), not a latency benchmark. No real tool or local worker was
executed and no private task history or project files were sent. Application
fixtures separately use actual SQLite and owned local HTTP work to prove durable
tool/result/steering ordering, one local invocation and cancellation/failure
behavior. See docs/codex-steering.md for reproduction and remaining qualification.

Native Linear remains inaccessible because the Mac is locked; no issue update
is claimed. Actual interactive Sol/Ollama steering, stream interruption, automatic
restart/resume, stronger isolation and complete PRD/Linear acceptance remain open.

Steering verification: make check passed formatting/LOC, vet, full native race
suite and build (app 219.103s, telemetry 147.809s, CLI 40.189s, API 12.534s,
Codex bridge 3.730s, SDK 25.111s; unchanged packages cached). The final stricter
application receipt/sequence/exact-terminal assertions separately passed race
three times in 3.755s. Bridge fixture tests passed three race repetitions and
vet; final independent review found no concrete blocker. Native CLI build and
Linux amd64 cross-build passed. Focused bridge and application tests also passed
three CGO-free Linux ARM64 repetitions in isolated unprivileged, network-disabled
containers. The first application container omitted a writable process-owner
directory, so delegation never reached the worker; setting the documented
DARWIN_PROCESS_OWNER_DIR inside disposable /tmp made the unchanged binary pass.
This is not Linux race or live Linux Codex qualification. Generated schemas,
test binaries and inference diagnostics were kept outside the repository.

Task-duration scale/concurrency checkpoint: the preceding response revalidated
Codex authentication but did not advance implementation. This turn resumed the
two pending agent-authored test files on base 5af6061. No production behavior or
schema was changed. The actual read-only metrics benchmark now qualifies canonical
unique-timestamp histories with 1k/10k/100k completed tasks, valid paired events,
terminal heads and 100ms projections, seeded transactionally outside timing.
Three runs of five warm reads measured means of 2.79–2.86ms, 30.09–30.24ms and
363.52–365.74ms respectively on this M4 Max/macOS ARM64/Go1.27.1. At 100k,
reads allocated about 9.625MB and 400,370 allocations. All full-population count,
sum, bucket and snapshot checks passed; the complete benchmark exited zero (39.840s).
This is not cold-cache, p99, production mixed-workload, write-contention or SLA
evidence. Existing integrity validation was retained without optimization based
on these limited fixtures. See docs/task-duration-metrics.md for reproduction.

New concurrency tests use actual append transactions and independent read-only
metrics access, plus a deterministic pinned WAL snapshot across a terminal commit.
They require coherent lifecycle/histogram totals, stable epoch, fresh-read visibility
and cancellation rejection. No operational store or user configuration was touched,
and no inference was dispatched. Native Linear was checked again but the Mac is
locked and automatic unlock failed; no issue update is claimed. Mixed-workload
performance, instrumentation breadth and complete PRD/Linear acceptance remain open.
Final make check passed formatting/LOC, vet, the full native race suite and build
(app 215.020s, telemetry 143.897s, CLI 40.535s, API 12.794s, SDK 24.927s;
unchanged packages cached). Focused concurrency tests passed three race-enabled
native repetitions in 2.673s and three CGO-free Linux ARM64 repetitions in an
owned read-only, network-disabled, unprivileged container. This does not claim
Linux race execution or Linux performance. Independent review found no blocker
and clarified that the stress test does not force scheduling overlap; only the
separate pinned-transaction test establishes the deterministic commit boundary.

Task-duration instrumentation checkpoint: the previous goal turn made verified
progress and was committed/pushed596ee7f; this turn started clean. PRD13 task
latency observability now includes a durable timing projection, public metrics
and cumulative OTLP histogram rather than deriving a recent-window approximation
and labeling it cumulative. Schema29 captures an instrumentation epoch plus one
metadata row per newly observed task, updated transactionally in all three event
insert paths: ordinary append, orphan recovery and submission recovery.

Recorded duration is TaskStarted-to-terminal event wall time, including tool/
approval waits and recovery downtime, excluding pre-start admission/queue time.
Backwards/overflow intervals are invalid_time, never zero. Tasks without a captured
start—including pre-migration history—are missing_start. Migration adds tables
without rewriting journal bodies or retrospectively fabricating samples.
The exact terminal event/sequence binding and head update share the transaction;
failure rolls back timing and journal state together, and acknowledgement retry
does not duplicate a sample. The epoch remains stable across ordinary reopening.

Metrics read canonical bounded projection fields and head/event metadata in the
same read transaction as lifecycle counts, not private journal bodies. Fixed
completed/failed/canceled groups expose upper-inclusive buckets, floating-point
sum seconds and explicit unavailable reasons; observed+unavailable counts must
equal terminal lifecycle counts. OTLP emits a cumulative task-duration histogram
and unavailable gauges with only fixed state/reason labels. Schema1..28 keep
legacy metrics without a timing field. All feature-specific schema ceilings now
accept29; future-schema denial fixtures use30. Earlier historical fixtures retain
their original versions and supported feature boundaries.

Focused tests qualify all bucket edges and one-nanosecond-above boundaries,
zero/backward/overflow timing, exact counter reconciliation, projection/epoch
corruption, large unusable private journal content, migration rollback and no
backfill, and atomic terminal/recovery paths. Root read tests passed race three
times4.885s; timing/migration/index/metrics focused tests passed10.101s. Public
contract/codec race tests passed three times1.372s, including impossible sum
rejection with conservative floating tolerance. Cross-surface SQLite→app/API
and SDK→OTLP integration passed three times1.920s: completed1.5s and missing_start1
survive inspection/export without private identities or database mutation.

See docs/task-duration-metrics.md for coverage, migration, clock semantics and
cumulative-stream limits. Multiple exporters need coordination; copying/restoring
or manually altering stores is not an automatic metric reset protocol. Native
Linear remains inaccessible because the Mac is locked; no issue update is claimed.
No user configuration/store was changed or live inference dispatched. Full PRD
completion, traces, provider/tool latency and cost histograms, production
collectors and large-store read/write performance remain open.

Final make check passed formatting/LOC, vet, all native race tests and build
(app218.655s, telemetry145.967s, CLI41.162s, API12.694s, SDK25.931s,
metrics2.177s). Native and Linux amd64 CLI builds passed. Full metrics package,
focused timing/projection/migration/legacy/read tests and the cross-surface
SQLite→API/SDK→OTLP integration executed successfully as CGO-free Linux arm64
binaries in existing Alpine3.22 with no external network, read-only root and
an unprivileged UID. Linux execution was not race-instrumented. Independent
final review found no concrete blocker. Back up operational stores and stop
older writers before upgrading; this checkpoint did not migrate user stores.

Periodic metrics delivery checkpoint: the previous goal turn made verified
progress, committed/pushed bc615a6; this turn started clean. PRD13 optional
observability now has opt-in daemon scheduling and a caller-owned Go SDK exporter,
not just a manual snapshot sender. telemetry.metrics_export is absent/disabled
by default and preserves prior default serialized configuration fingerprints.
It specifies an exact endpoint, named credential and interval (default60s,
bounds1s..24h); layered overrides, strict scalars and endpoint redaction are tested.
The reserved opentelemetry_enabled flag remains unsupported: this feature exports
existing lifecycle gauges, not traces or the full required instrumentation.

Each handle starts immediately and waits its interval after each completed
attempt. Every attempt reads fresh SQLite state and reuses the bounded one-shot
delivery policy. No overlapping attempts, accumulated ticks, saved-body retries,
shutdown flush or durable schedule is introduced. Close cancels/joins cooperative
work, is idempotent and reports the last recorded attempt error until recovery.
Separate handles/processes remain independent and require stream coordination.
Configured export authority is snapshotted and checked again after credential
callbacks; a callback cannot silently disable/repoint the setting and still send.

The daemon owns export only after the bind/storage gates, closes it before the
database, and includes a supplemental metrics_export health check. Collector
failure degrades diagnostic status but does not block task readiness; later
acknowledgement restores export health. Configured collector credentials also
join task/context/admission/health redaction even when delivery is disabled.
See docs/metrics-export.md for configuration, lifecycle and delivery limitations.

Native Linear was rechecked but the Mac remains locked; no issue update is
claimed. Owned-loopback/temporary-store tests and independent reviews are recorded
below after final qualification. No user configuration or live collector was
enabled, and no model inference was dispatched. Full PRD completion remains open.

Focused race tests passed three times for config/health (5.254s/1.392s), runtime
lifecycle (15.699s), fresh database observations (4.928s), SDK wiring (2.446s)
and actual failing/recovering collector daemon lifecycle (10.885s). The owned
daemon test observes degraded-to-ready status recovery and HTTP /health200 during
collector failure. Its model inventory is empty, so diagnostic readiness is
compared with the exporter omitted rather than asserting a usable model exists.
No external collector or inference is involved.

Credential qualification exposed an existing default-context gap: fresh prompts
were journal-redacted but could reach the provider raw without a custom context
engine. Assembly now validates and owns redacted tiers for every engine; the
redundant later Codex-only pass is removed, while raw compacted-tool preflight
remains. Actual fresh provider/collector credentials are scrubbed before provider
dispatch and from results/journal/health; submissions containing the collector
credential fail before storage creation. Context and Codex history/compaction
race tests passed three times (14.528s), including prepared-message reuse and
marker-overlap behavior. Default structured tool history now shares the custom
engine's fail-closed JSON checks; arbitrary repeated redaction is not claimed
idempotent. Full verification follows below.

Final make check passed formatting/LOC, vet, all native race tests and build
(app205.154s, telemetry136.084s, CLI39.965s, API12.057s, SDK23.892s,
config3.750s, health1.261s). Native and Linux amd64 CLI builds passed. Focused
configuration, runtime exporter/redaction/context/Codex-history and SDK exporter
suites executed successfully as CGO-free Linux arm64 binaries in existing
Alpine3.22 with no external network, read-only root and an unprivileged UID.
Linux tests were not race-instrumented; actual daemon subprocess qualification
was native macOS. Independent final lifecycle review found no concrete blocker.
Durable delivery, production collectors, traces, histograms and full PRD
instrumentation remain unqualified or incomplete.

Shared transport pinning checkpoint: the previous goal turn made verified progress
and was committed/pushed as 87c80be; this turn started clean. Following the metrics
export finding, inspection showed the shared provider transport still delegated
localhost resolution to the host resolver when localOnly=false. Local-model
application routes already pass localOnly=true, but trusted SDK callers and
hybrid/cloud routes using local proxy endpoints can use false.

The shared dial path now converts recognized loopback hosts to literal loopback
addresses in every mode, preserving the existing localhost-to-127.0.0.1 mapping.
Nonlocal hosts remain denied in local-only mode; explicitly authorized remote
HTTPS hosts retain normal DNS in hybrid/cloud mode. Endpoint allowlisting, TLS
verification, proxy denial and provider redirect handling are unchanged. This is
transport hardening, not an OS-wide egress sandbox or an arbitrary-host SSRF policy.

Pure dial-target tests cover both modes, case-insensitive localhost, IPv4,
IPv6, IPv4-mapped IPv6, local-only remote denial and remote DNS preservation.
Focused policy race tests passed three times (1.424s) before independent provider
integration qualification. Full verification follows below. Native Linear was
checked but the Mac was locked and automatic unlock failed; no issue status
update is claimed. The broader PRD remains incomplete.

Independent provider integration uses NewTransport(false), the real Ollama
adapter and an owned HTTP server with a synthetic bearer. HTTP trace hooks prove
zero DNS lookups and exactly one connection to literal 127.0.0.1 for localhost,
mixed-case localhost, IPv4 and IPv4-mapped IPv6. Configured proxy fixtures receive
zero requests. These tests passed race detection three times (1.391s); final
review found no blocker. Native IPv6 listeners and TLS were not newly qualified.
Compatibility: localhost consistently means IPv4; IPv6-only services must use
explicit [::1]. Previously local-only routes already had that behavior.

Final make check passed formatting/LOC, vet, all native race tests and build
(app197.843s, telemetry134.842s, CLI36.990s, API11.919s, SDK23.587s,
policy1.880s). Native and Linux amd64 CLI builds passed. The complete policy suite
also executed successfully as a CGO-free Linux arm64 binary in existing Alpine3.22
with no external network, read-only root and an unprivileged UID. Linux tests were
not race-instrumented. No live model, user configuration, credentials or database
was changed. Broader egress isolation and full MVP qualification remain open.

Explicit telemetry-export checkpoint: previous turn made verified progress,
committed/pushed d4337c9; this turn started clean. The PRD's optional observability
delivery now has a real one-shot CLI/SDK OTLP/HTTP JSON path for the existing
content-free lifecycle gauges. Pure metrics.MarshalOTLP validates the closed
snapshot vocabulary, omits unavailable groups and preserves int64 counts/uint64
nanosecond timestamps as decimal strings. Fixed resource/scope/state attributes
do not include prompts, task/model IDs, paths, endpoints or credentials.

ExportMetrics reads existing SQLite only, resolves an optional named credential,
and sends through an owned deployment-mode transport. Loopback HTTP is allowed;
non-loopback HTTP, remote destinations in local-only mode, proxies and redirects
are denied. The operation is bounded to ten cooperative seconds and64KiB request/
decoded response bodies, with one POST and no automatic retries. HTTP200 JSON
must acknowledge all data; partial rejection/malformed response fails generically.
Zero-rejection warnings and unknown future fields are accepted without exposing
diagnostics. Collector acknowledgement is not downstream persistence; an error
after dispatch/acceptance is not proof of zero delivery. No delivery ledger or
exactly-once guarantee is claimed.

CLI strict export flags and SDK options preserve the existing local metrics
command. No configuration or startup defaults changed; the reserved runtime
opentelemetry_enabled flag remains unsupported, and no daemon-loop/HTTP mutation
endpoint was added. Periodic export, traces, latency/cost histograms, deployment
qualification and full instrumentation remain required. Multiple database streams
need collector-side identity separation; fixed per-database gauges are not global
aggregates. See docs/metrics-export.md and its primary OTLP/schema references.

Owned SQLite/collector tests cover real queued counts, no sensitive payload or
source mutation, no creation on missing storage, method/path/media/bearer handling,
strict input, cancellation, redirects, response bounds/partial errors and no retry.
Independent review found a final credential callback could change policy after
the initial check; a second authority check now rejects mode/database/flag/key
rotation or callback panic before dispatch. Combined focused race tests passed
three times (metrics1.303s, app7.092s, CLI2.187s, SDK2.464s). Full verification
follows below. Tests contacted only owned loopback fixtures, not any user collector
or model. Native Linear was checked but the Mac remained locked; no issue update
or completion is claimed. The full PRD remains incomplete.

Final independent review found localhost resolution was not pinned in hybrid/
cloud transport mode. Export now forces loopback pinning for all permitted HTTP
destinations and case-insensitive localhost in every mode. A policy matrix and
real hybrid localhost collector regression cover the fix; review found no further
blocker. Codex CLI was rechecked: version 0.153.4, logged in using ChatGPT. The
existing coordinator remains exact gpt-5.6-sol; no credentials were read or
changed, and no new live inference was dispatched during this checkpoint.

Final make check passed formatting/LOC, vet, all native race tests and build
(app197.637s, telemetry135.639s, CLI37.559s, API11.861s, SDK23.476s).
Native and Linux amd64 CLI builds passed. Focused metrics, application, CLI and
SDK export suites executed successfully as CGO-free Linux arm64 binaries in
existing Alpine3.22 with no external network, read-only root and unprivileged UID;
the application binary was rebuilt and rerun after the final pinning fix.
Linux tests were not race-instrumented. No production collector was contacted.

Indexed evidence-read checkpoint: the previous turn made verified progress,
committed/pushed70785d9; this turn started clean. A CPU profile of the actual
eight-model/1,000-seed automatic-task fixture attributed74.46% cumulative sampled
CPU to OutputValidity. An attempted combined-count JSON query increased
allocations and measured115.17–115.71ms, so it was discarded. Query-plan inspection
confirmed existing task/sequence seeks rather than guessing a bad join index.
An owned-database experiment then demonstrated that an additional task/kind/
sequence expression index cuts repeated JSON-kind scans.

Schema28 now creates events_task_kind transactionally during normal migration,
without changing any journal/evaluation body or evidence predicate. The preliminary
model-start count stops at201 because only zero/≤200/>200 distinguish query plans.
The selected latest100 window, domain/profile/validator filtering, source-body
validation, duplicate counts and malformed-evidence rejection remain intact.
Read-only supported schemas extend through28, while comparison/source checks
continue accepting27. Metrics/lease metadata validators and legacy migration
fixtures reflect the new maximum; future-schema tests reject29. The skill file
catalog is unchanged. See docs/event-kind-index.md for upgrade constraints.

Recorded isolated full application benchmarks passed three100-task runs per case
in45.083s. Eight-model/1,000-seed mean improved from111.97–113.83ms to49.76–49.86ms;
run p99 values were55.48–56.60ms, maximum57.72ms. Every case still includes normal
task writes/index maintenance. These are local fixture results, not production
SLAs or inference speedups. An overlapping exploratory run was discarded. Other
host workloads were uncontrolled. Separate read probes on synthetic1,000-task
corpora showed gains for text and4KiB Go-source checks; production migration
duration, disk footprint and standalone write throughput remain unqualified.

Migration tests verify legacy27 read-only nonmigration, body/result preservation,
append maintenance, stable reopening and failed migration/recovery after an index
name conflict. Density regressions cover199/200/201/350 model starts, newer
off-domain/profile distractions, reversed lexical IDs/timestamps, empty/unfinished
evidence, old excluded corruption and selected foreign-attempt rejection.
Focused migration/validity race tests passed three times54.540s and separate
density/unfinished tests passed three times36.062s. Existing migration and public
adapter-focused suites also passed. Independent final review found no blocker.

Full make check passed formatting/LOC, vet, all native race tests and build
(app196.575s, telemetry133.735s, CLI38.148s, API12.383s, SDK24.292s).
Native and Linux amd64 CLI builds passed. Migration/validity/density suites and
application blank-answer/Go-validation routing tests executed successfully as
CGO-free Linux arm64 binaries in existing Alpine3.22 with no network, read-only
root and unprivileged UID. Linux execution was not race-instrumented; no Linux
latency or production migration throughput claim follows from these tests.

Native Linear was checked but the Mac remained locked; no issue status update or
completion is claimed. Tests used owned temporary stores and loopback providers;
no live inference, user configuration changes or user database migration occurred.
Back up operational stores and stop old writers before normal startup upgrades
them. Large-history migration, power-loss boundaries, concurrency and both full
latency SLAs remain open, along with the broader PRD acceptance requirements.

Routing-performance evidence checkpoint: previous turn made verified progress,
committed/pushed72a18bd, and this turn started clean. PRD16 / historical DAR-44
performance evidence now measures complete automatic-task latency distributions
instead of only three-operation means. Each case records100 serial operations,
empirical nearest-rank p50/p95/p99/max and sample count; allocation of samples and
sorting occur outside the timer. A unit test verifies percentile ranks, including
small samples and non-round counts. Three independent runs preserve metric
ranges without pooling or averaging percentiles. The corpus grows from seed
count to seed+101, and normal discovery-cache expiry remains possible.

New separately measured pure routing benchmarks qualify successful evidence-based
selection, expected exclusions/fallbacks, diverse failure domains and exploration
before timing. Two/eight/64-model means were approximately0.37/1.43/15.3µs.
The full eight-model/1,000-seed loopback task fixture averaged111.97–113.83ms,
with run p99 values122.1–131.7ms and maximum141.4ms. These boundaries differ:
the latter includes SQLite/admission/execution/completion and growing history;
the former excludes them. This motivates targeted admission/evidence/persistence
profiling, not an unproven ranking optimization or a claim that all overhead is
one query. See docs/benchmarks.md for the complete matrix and commands.

Independent review confirmed rank calculations and measurement boundaries.
An initial overlapping exploratory run was discarded; recorded app/core runs
did not overlap each other or full project tests. Other host load and thermal
state were uncontrolled. No live inference, user configuration/database changes,
or production runtime behavior changes were made. Larger histories/outputs,
sparse profiles, meaningful feedback populations, multiple providers, concurrency,
actual sensors, auxiliary classification and both production latency SLAs remain
unqualified. Native Linear was checked but the Mac remained locked; no issue
update or completion is claimed.

Full make check passed formatting/LOC, vet, native race tests and build
(app193.197s, telemetry118.764s, CLI38.206s, API11.843s, SDK23.562s,
routing1.510s). Linux amd64 production cross-build passed. The percentile unit
test and every new/modified benchmark fixture executed successfully as CGO-free
Linux arm64 binaries in existing Alpine3.22, no network, read-only root and
unprivileged UID. Those one-operation fixture checks are not Linux performance
measurements or race qualification. Recorded macOS application and core benchmark
runs passed separately in79.299s and19.922s respectively.

Fixed-source freshness checkpoint: the previous turn made verified progress,
committed and pushed a6da473; this turn started clean. The opt-in application/SDK
outcome action now invalidates an unfinished selection when its original evidence
changes. This supersedes the previous checkpoint's ability to complete from old
feedback after correction or evidence-database loss. Completed receipts remain
historical acknowledgements and do not require current source availability.

Selection reports capture sorted exact task IDs in their original SQLite
snapshot. Before checkpoint save and final completion, a bounded read-only check
replays only those fixed sources, verifies original attribution/privacy/ordinal
bounds, and recomputes the comparison with current evaluation revisions and global
session correlation. It requires the saved report/evidence digest to match.
Changed feedback, task outcomes, new same-session correlation, missing journals
or unavailable storage fails closed, preserving the intent and saved checkpoint
without a receipt or activation change. It never reruns latest-window selection
or substitutes newer tasks. Unrelated new traffic leaves membership unchanged;
known empty sources remain empty. Source-less legacy checkpoints are inspectable
but not completable through the application/SDK. Core hosts remain responsible
for their explicit guards. No catalog/SQLite migration was introduced.

Public comparison metadata now includes selected task IDs, not prompts, outputs
or unselected/sibling IDs. Scope and cumulative secret checks cover source
observations and report metadata before persistence/export. Treat identifiers as
sensitive if they encode private information. Current configuration, credential,
activation and immutable-version guards remain required. The source read snapshot
and later catalog write are not one distributed transaction: a concurrent writer
can change evidence after the last check. No checkpoint expiry, automatic reset,
new CLI/HTTP mutation or unattended monitoring loop is introduced.

Focused race tests passed three times for source selection/checking (skills1.292s,
telemetry8.868s), app outcome paths85.897s and SDK9.330s; a separate app missing-DB
case passed three times5.408s. A real SQLite/WAL barrier proves snapshot
coherence: a feedback commit during a pinned check is seen by the next check,
not partially by the current one. A qualified latest-window trap proves retry
does not reselect. API qualification checks exact source-ID export while keeping
prompt content and privacy-excluded tasks out. Independent review found no
concrete blocker.

Full make check passed formatting/LOC, vet, the native race suite and build
(app197.575s, telemetry124.869s, CLI39.135s, API14.657s, SDK29.682s,
skills25.976s). Native and Linux amd64 CLI binaries built. Source-contract,
telemetry freshness, app checkpoint and SDK outcome tests also executed as
CGO-free Linux arm64 binaries in the existing Alpine3.22 image, without network,
with a read-only root and unprivileged UID. Linux execution was not race-enabled.

Native Linear was checked this turn but the Mac remained locked; no issue update
or completion is claimed. No user database/configuration or live inference was
changed. Qualified production validators, current-activation attribution,
repeated-statistical-monitoring policy, atomic cross-store coordination and full
PRD/Linear acceptance remain open.

Selected-evidence recovery checkpoint: previous turn made verified progress,
committed/pushed 19285f7, and this turn started clean. The opt-in application/SDK
outcome action now saves an independently guarded selection checkpoint before
final adjudication. Schema8 links checkpoint, exact intent, configured model,
policy, selected aggregate and save time; new completion receipts bind that
checkpoint. A checkpoint is selected historical evidence, not a completed
rollback or positive validation. Exact retries can complete from the frozen
report without invoking the selector or reopening SQLite, subject to current
activation CAS, permissions, secrets and immutable version checks.

Intent-only attempts still cannot retry selection. Report/guard failures before
checkpoint persistence leave the original claim unresolved. Errors after a
checkpoint save permit only completion from that saved report, never substitution
of newer feedback. Changed activation or policy fails visibly rather than
refreshing the expected state. Current secrets and both version files are checked
again on resumed uncompleted work; historical completed receipt retries remain
independent of evidence databases and version-file availability. Old schema6/7
receipts remain readable without fabricated checkpoint provenance. Fresh direct
core non-checkpointing entry points retain their prior behavior; the new core
checkpointed entry point requires an explicit selection persistence guard.

Application tests use actual catalog/SQLite feedback and deny finalization only
after observing the saved checkpoint. Corrected later feedback is independently
qualified as no-signal, then the owned database is moved away. Exact retry must
complete using the original saved signal/report, without recreating or reading
the database. Other cases cover stale activation, changed model/policy, sensitive
metadata, disabled mutation and read-only inspection. Independent review caught
and fixed a lower-level resume path that skipped immutable version-file reads.
No user configuration, database or live inference is involved. Native Linear was
rechecked but the Mac remained locked; no issue status change is claimed.

Explicit host recovery is not an unattended daemon lifecycle, repeated statistical
monitoring, current-activation exposure attribution, qualified domain validation,
or power-loss qualification. Those broader PRD requirements remain open.

Focused application outcome tests passed three race runs (65.035s). Final core
outcome and selection-specific tests passed three race runs (15.833s and 7.207s),
including concurrent saved-report recovery, altered/missing checkpoint linkage,
corrupted reports, current activation mismatch, schema6/7 receipt compatibility,
and missing or corrupted candidate/predecessor bodies. SDK checkpoint inspection
checks detached values, disabled mutation and no store creation. An owned child
was killed in its final guard after checkpoint persistence and before receipt
commit; reopen preserved the unchanged activation and no completion receipt, and
explicit retry committed exactly once with the saved report and zero selector
calls. This is not a filesystem fsync/rename or physical power-loss guarantee.

Full production make check passed formatting/LOC, vet, all native race tests and
build (app201.308s, telemetry127.607s, CLI39.941s, API14.982s, SDK27.177s,
skills24.366s). The final skill-store test tree, including the last checkpoint
cases, also passed its complete race suite (21.647s). Native and Linux amd64
binaries built. Final core outcome, application and SDK tests executed successfully
as CGO-free Linux arm64 binaries in the existing Alpine3.22 image with no network,
read-only root and an unprivileged UID; Linux execution was not race-instrumented.
Saved evidence may predate corrected feedback: identity is preserved, not current
freshness. Checkpoint expiry/current-feedback invalidation remain explicit gaps.

Durable preselection checkpoint: the prior turn made verified progress, committed
and pushed a256ee3; this turn started clean. PRD9.4's outcome-policy action now
claims an exact activation before selecting evidence. Schema7 intents retain
operation ID, configured model identity, policy and historical activation position.
Only the call that successfully persists the claim proceeds. Failed, canceled,
panicking or interrupted attempts remain inspectable without a fabricated result;
same-ID retries and alternate IDs/policies cannot silently reread later feedback.
The final receipt still commits with optional rollback, and binds its intent.
Existing schema6 receipts remain historical, retryable records without retroactive
claims about how many times selection ran before they committed.

Independent review found that structurally valid but corrupted stored revisions
or moved keys could otherwise free an attempt slot. New claim admission now
verifies all outcome intent/receipt historical prefixes before trusting those
fences, with cancellation between records and a second check under the claim
lock. Ordinary catalog reads retain cheap structural checks; exact lookups fully
validate their own records. Tests prove corrupted pending and legacy completed
records reject new-ID dispatch. Legacy direct-core receipt retries also preserve
nonempty configured model identities; fresh unbound direct-core calls must use
empty IDs or the new guarded entry point with an explicit binding.

The application guards metadata, version contents, current policy, cumulative
secrets and storage bindings before the intent write and again before the final
receipt. Go SDK intent inspection is read-only and available when rollback is
disabled. Pending attempts cannot be cleared or taken over automatically. This
provides at most one selector call for a claimed attempt under supported locking,
not exactly-once completion or a general repeated-monitoring procedure. A crash
after claim may leave no callback invocation; no lease expiry is treated as proof
that it is safe to retry. Recovery of a selected-but-uncommitted report, automatic
monitor scheduling, current-activation attribution and domain validators remain
required. Native Linear was rechecked but the Mac was locked; no issue changes
are claimed. No user database/configuration or live inference is involved.

Focused core outcome/intent tests passed under race three times (11.332s).
Application intent/rollback tests passed three times (45.159s), including a
qualified restored SQLite exposure-view probe proving blocked retries perform
zero selection reads. SDK actual-catalog tests cover both completed and unresolved
inspection, disabled mutation and no database creation. An owned subprocess was
killed inside the selector after durable claim persistence; reopen produced no
receipt, no activation change and zero subsequent selector calls for exact or
changed bindings. This qualifies that process-death boundary, not rename/fsync
interruption or physical power loss.

Final production make check passed formatting/LOC, vet, the complete native race
suite and build (app181.609s, telemetry123.573s, CLI38.960s, API13.775s,
SDK25.393s, skills21.599s). A subsequent test-only strengthening asserts that a
competing operation never enters the selector, not merely that only one result
commits; it passed 20 race repetitions. Final core outcome tests, application and
SDK tests executed successfully as CGO-free Linux arm64 binaries in the existing
Alpine3.22 image with networking disabled, read-only root and unprivileged UID.
Linux tests were not race-instrumented. Native and Linux amd64 binaries built.
Independent review verified both corruption and legacy-compatibility fixes.

Outcome-policy rollback checkpoint: started from clean, verified/pushed 44458e2.
PRD9.4 now has a default-off trusted-Go-host action that binds automatic exposure
selection to an exact first activation and its validated undo predecessor. The
application obtains evidence from the configured SQLite selector; matching
immutable catalog digests and activation revision are checked before commit.
The observational regression signal remains a policy heuristic, not fabricated
deterministic validation or proof that the skill caused the decline.

Schema6 catalog receipts atomically record one committed adjudication per skill
activation revision: a signal restores the predecessor with a distinct outcome
marker, while empty, insufficient and no-signal reports commit no_action without
changing activation state. Exact retries return the historical receipt without
reading newer SQLite evidence, including after later activation or unavailable
telemetry. Failed/interrupted callbacks can repeat before commit; this is neither
exactly-once callback execution nor a strict bound on statistical looks. Catalog
and SQLite are not one distributed transaction. Reports capture a historical
snapshot, and CheckedAt is catalog decision time.

Configuration, application and Go SDK expose the separate outcome_rollback
permission, preserving default settings JSON/fingerprints when false. Every
action/retry checks current policy, model mapping, cumulative secret redaction,
kill switches and per-call catalog/database bindings. Independent review caught
a historical retry policy-change gap; new tests reproduce and cover its fix.
Inspection remains available with rollback disabled. Receipt caps, cooperative
deadlines, immutable metadata ownership and schema/marker consistency checks fail
closed. Ordinary catalog reads do structural receipt validation; requested receipt
lookups additionally verify the historical activation prefix and cohort digests.

Focused race tests passed repeated core, config, application and SDK scenarios,
including concurrent conflicting decisions, no-action consumption, digest and
predecessor rejection, restored-version rejection, callback panic/cancellation,
secret and policy rotation, and mutation-free historical retry. An owned child
process was killed after catalog commit but before wrapper acknowledgement;
reopening and retrying returned the same receipt without another selector call
or rollback. This does not qualify kill-during-fsync or physical power loss.

Final make check passed formatting/LOC enforcement, vet, all native race tests
and production build (app173.267s, telemetry117.821s, CLI38.243s, API11.968s,
SDK21.078s). Native and Linux amd64 binaries built. New core, application, SDK
and configuration tests also executed successfully as CGO-free Linux arm64
binaries in the existing Alpine3.22 image with network disabled, read-only root,
unprivileged UID and bounded resources. Linux execution was not race-instrumented.

No unattended daemon loop or CLI/HTTP mutation endpoint is introduced. Durable
preselection intent, current-activation exposure attribution, repeated-monitoring
policy, production validators and confounder handling remain unfinished. No live
model inference, user database migration or user configuration change occurred.
Native Linear remained inaccessible because the Mac was locked; no issue status
was changed. Codex CLI 0.153.4 was rechecked as installed and logged in using
ChatGPT; the existing exact-Sol app-server integration remains experimental.
See [outcome-policy rollback](skill-outcome-rollback.md) for the contract and limits.

Automatic comparison-window checkpoint: previous turn made verified progress,
committed/pushed e77573a, and this turn started clean. PRD9.4 now has automatic
outcome-independent exposure selection instead of requiring individual task IDs.
Schema27 adds exact recorded skill exposures and indexed insertion-order/privacy
and all-task session lookup. Fresh complete references commit in the same
transaction as TaskStarted; failed index insertion rolls back event/head/ordinal
changes. Exact retries do not duplicate records. Legacy absent/incomplete/empty
attribution remains absent, not guessed from prompts. Migration validates bounded
first-event bodies, binds envelope identities to the original journal, backfills
existing ordinals, and rolls back schema changes on invalid provenance. No user
database was opened or migrated during this work.

The selector fixes scope/name/versions/privacy and takes at most100 latest
insertion-order exposures per version before examining state, execution or quality.
It never searches older records to fill the window with successes. Selected
running/canceled/unknown-quality/wrong-execution tasks remain visible exclusions.
All-task session lookup additionally catches siblings without skill references
or outside the window. Selected index metadata is checked against replayed
journals, and current feedback and correlation observations share one read-only
SQLite snapshot. Existing aggregate journal/evidence/body limits apply with no
partial report or silent skipping. Exposure index data accelerates lookup, not
independent proof of semantic workflow execution or database completeness.

Reports bind configured model ID on outer and nested aggregates, expose selected
window counts/ordinals/has_more and the snapshot insertion watermark, and omit
comparison entirely when no exposures match. Global correlation metadata is
bound into the evidence digest; empty metadata preserves previous explicit
comparison digest encoding. SDK SelectSkillComparison, strict CLI
`skills compare-select --config path` JSON input and authenticated POST
`/v1/skills/comparison/select` all use the configured service, including complete
selected-evidence secret checks, no catalog creation and cooperative deadlines.
Read-only inspection requires schema27 and never migrates. Compatible existing
schema26 control/read features remain accepted, while public schema caps and
future-schema rejection tests now recognize27/reject28.

Focused race tests passed three repetitions for migration/indexing, pure
selection/correlation contracts, storage windows and actual WAL feedback
concurrency, app, SDK, CLI and HTTP. Tests verify older successes do not replace
new unknown/rejected outcomes, separate privacy strata, global unexposed siblings,
current negative/corrected feedback, tampered projections, atomic rollback,
empty windows and no inference/write behavior. Independent review found no
actionable blocker. Full make check passed formatting/LOC, vet, the complete
native race suite and production build (app164.362s, telemetry117.714s,
CLI37.007s, API12.402s, SDK21.700s). Native and Linux amd64 binaries built.
New schema/index/selection/WAL, application, API, CLI, SDK and pure comparison
tests executed successfully as CGO-free Linux arm64 binaries in the existing
Alpine3.22 image with network disabled, read-only root, unprivileged UID and
bounded memory/processes/tmpfs. Linux tests were not race-instrumented.

Insertion order is not wall-clock time; pre-schema18 ordinals reflect the old
lexicographic backfill. Latest-window comparison is observational, not random
sampling, a complete population, causal attribution or repeated-testing proof.
Production validators, confounder handling, durable repeated-monitoring policy
and activation-bound outcome rollback remain required. Native Linear was checked
again but the Mac remains locked, so no issue update is claimed. No live model
inference or user configuration changes were performed. See
[automatic selection rules](skill-comparison-selection.md).

Skill outcome comparison checkpoint: resumed from GitHub-backed 2cfd61e and the
in-progress comparator/batch work. The preceding user-facing turn confirmed the
authenticated Codex CLI but did not itself advance implementation. PRD9.4 now
has a bounded explicit-task-set diagnostic over two fresh skill versions, with
configured scope/model binding and current independent quality evidence.

One read-only SQLite transaction spans every selected journal and current
evaluation chain; aggregate event/evidence metadata limits are checked before
loading bodies. A real WAL barrier test commits two feedback revisions while a
reader is pinned: the first batch retains both old outcomes and the next sees
both revisions. Missing/corrupt tasks and budget overflow return no partial data.
There is no migration, catalog access, inference, fitness update or rollback.

The pure comparator excludes all repeated selected sessions, task lineage,
nonfinal work, unknown/ambiguous fresh attribution, wrong execution profiles and
missing/other quality sources. It preserves explicit rejection. User feedback
qualifies for every domain; deterministic/tool-result quality only qualifies for
the enumerated objective domains. Model judging and mechanical syntax/nonempty
checks cannot qualify. Conflicting version content digests fail even when the
conflicting tasks would otherwise be excluded. Reports recompute count partitions,
rates, Wilson bounds and status during validation, with a canonical evidence
digest and no raw prompt/output/skill/evidence-reference payloads.

Application, SDK, `darwin skills compare --config path` (strict JSON stdin), and
authenticated POST `/v1/skills/comparison` expose the read-only operation.
Configured secrets are checked before I/O and again against complete observations
and the report. HTTP shares bounded control capacity with a five-second deadline.
Review found and fixed lossy surrogate escape decoding and missing configured
model-ID response binding; tests explicitly reject both conditions. Current user
corrections change accepted counts/digests without adding samples or changing
the journal. Unknown legacy records stay unknown rather than fabricating exposure.

The statistical result is explicitly advisory: per-cohort approximate Wilson95
interval separation is not joint confidence, a causal experiment, proof of
equivalence, or repeated-testing protection. Automatic cohort selection/indexing,
confounder controls, repeated monitoring policy, qualified production validators
and durable activation-bound outcome rollback remain unfinished. See
[comparison usage and limitations](skill-outcome-comparison.md).

Focused native race tests passed three repetitions for the comparator, batch
storage, app, SDK, CLI and API, including actual HTTP/app/SQLite integration.
The first full check caught a stale compiled CLI fixture that serialized an
unset estimated cost as YAML null. Configuration rejection was correct; the
fixture was fixed to explicit zero before freeze. The frozen full CLI race suite
passed35.488s. Final make check passed formatting/LOC, vet, the complete native
race suite and production build, including app162.455s, telemetry114.011s,
CLI37.321s, API13.202s, SDK21.944s and skills17.516s.
Native and Linux amd64 builds passed. Comparison tests executed successfully as
CGO-free Linux arm64 binaries in the existing Alpine3.22 image (no network,
read-only root, unprivileged UID, bounded memory/processes/tmpfs), including the
actual WAL and HTTP integration tests. Linux tests are not race-instrumented.
Native Linear was rechecked: the Mac remains locked and automatic
unlock failed; no issue state/comment update is claimed. No user database or
configuration was changed, and no live model inference was performed.

Skill outcome attribution checkpoint: the prior turn made verified progress and
was pushed as6439059; this turn started clean. PRD9.4 outcome-driven regression
needs reliable skill exposure and independent outcome evidence, not skill names
mined from arbitrary prompt/history JSON. New application TaskStarted records
carry bounded host-owned SkillContextUse metadata for fresh admitted skills,
including scope/name/version/digest. Capture occurs after tool/byte selection and
whole-tier context planning; persistence precedes model dispatch. Records mean
fresh context admission, not actual workflow execution, successful dispatch or
causal responsibility for an outcome. Legacy absence and unavailable/redacted
attribution are distinct from a known empty fresh tier. History and user/model
text never fabricate new references. Identity secret collisions withhold the
entire attribution record while preserving existing redacted context behavior.

Runtime and replay validate at most16 unique references and own their copies.
Review caught that adding a nil Snapshot field would change saved source-digest
inputs. Explicit omitempty plus a literal legacy JSON golden preserves the old
snapshot bytes; nonnil records remain visible. No old task or receipt is rewritten,
and this additive event field requires no database migration.

New SkillTaskOutcome returns one bounded coherent SQLite observation of task
state, fresh attribution, lineage, latest attempt key, current quality evidence
ID/digest and separately verified mechanical output checks. Rejected, canceled,
failed, running and unevaluated work stays visible. Nonempty text/Go syntax never
becomes quality acceptance; current user revisions supersede earlier subjective
judgments without creating additional samples. Strict opaque evidence-reference
grammar and fresh configured-secret checks protect the metadata-only boundary.
Referenced scopes must match configuration; unreferenced tasks need no skill
configuration. Inspection opens no catalog and initializes no storage.

The operation is exposed by the configured application and SDK, CLI
`task skill-outcome --config --task`, and authenticated GET
`/v1/tasks/{id}/skill-outcome`. HTTP rejects body/query/browser origins, shares
bounded control capacity and uses a five-second deadline. Malformed, foreign or
secret-bearing metadata returns no partial report. Neither inspection nor
attribution updates fitness, dispatches a model or triggers rollback.

Focused race tests passed three runs across runtime, sessions, skills, storage,
app, CLI, SDK and API. Actual application fixtures verify selected/omitted tiers,
tool/byte exclusion, forged JSON, historical context, identity redaction and
disabled/default-config inspection. Actual HTTP/app/SQLite tests preserve current
negative/corrected feedback with no prompt/output/skill body, mutation or inference.
A test-only SQLite view barrier pins the reader before a real concurrent feedback
revision commits; the first result retains the old evidence, the next sees the
revision. The WAL test passed three race runs1.832s without production hooks.

Native and Linux amd64 builds passed. New telemetry, app, API, CLI and SDK tests
also executed successfully as CGO-free Linux arm64 binaries in the existing
Alpine3.22 image, with no external network, read-only root, unprivileged UID and
bounded temporary storage/resources. Linux tests were not race-instrumented.
The final make check after compatibility/default-scope adjustments passed
(format/LOC, vet, complete native race suite and production build), including
app158.145s, telemetry111.935s, CLI37.732s, SDK20.662s and sessions13.496s.
Independent final review found no blocker.
No new live model inference or user database/configuration changes were performed.
Native Linear was rechecked and remains locked; no issue update/completion is
claimed. Statistical comparable-cohort analysis, indexed cross-task observations,
automatic outcome-driven rollback, production skill validators and complete PRD
acceptance remain open. See docs/skill-outcome-attribution.md.

Configured learning lifecycle checkpoint: this turn started clean from verified
GitHub backup845302d. Optional learning.validator_id, regression_name and
regression_interval select a bounded immutable registry of trusted Go validators;
configuration never supplies executable validation or model self-approval. Empty
selections preserve existing serialized policy digests. The stock CLI has no
qualified domain validator and rejects unknown enabled selections before binding
or database initialization. SDK StartConfiguredLearning exposes the same explicit,
caller-owned lifecycle; no user configuration or background work is enabled by default.

Before either configured controller starts, a separate ten-second read-only
preflight validates existing learner and monitor policy bindings. An existing
task database is required for SDK use; configured regression requires a compatible
existing catalog. Missing bindings can start fresh but conflicts/corruption fail
without inference, validation or state mutation. This is not a cross-store atomic
lock: existing operation/revision fences remain authoritative. The daemon performs
this startup before task dispatch. Shared cancellation stops both controllers
before joining either, and readiness includes both learning and regression health.
Trusted validators must be read-only, repeat-safe, concurrent and cooperative.

End-to-end review exposed that a model could omit domain tags, leaving an activated
generated skill invisible to ordinary domain discovery. New model generations now
retain the verified common source domain without duplicating an existing tag and
revalidate the complete draft and4096-tag discovery limit. Prior saved drafts,
receipts and operator versions are not rewritten. The scheduled fixture now covers
generation, deterministic test validation, activation, retrieval of the exact active
version into a later actual runtime model request, regression rollback, and restart
without regeneration/reactivation. Retrieval is not arbitrary skill semantic execution;
the controlled lookup oracle is not a qualified production domain-validation engine.

Focused skill/application/configuration/CLI/SDK tests passed three native race runs
(app19.971s, SDK2.278s). Linux arm64 application, CLI and SDK tests executed from
CGO-free binaries in the existing Alpine3.22 image with unprivileged UID, no external
network, read-only root, bounded resources and a disposable tmpfs. They passed,
including the scheduled learning/rollback/restart test; Linux was not race-instrumented.
Native make build and Linux amd64 production cross-build passed. Independent review
found no remaining concrete issue after durable-policy startup preflight and the
source-domain discovery fix. Full native make check passed (format/LOC, vet,
complete native race suite and production build), including app162.082s,
telemetry114.168s, CLI36.805s, SDK23.233s and skills18.887s.

Codex CLI recheck: installed0.153.4 reports ChatGPT login; the actual task-owned
gpt-5.6-sol checked launcher and same-session recheck passed under race detection
without submitting a thread, turn or prompt. This follows the supported managed
ChatGPT authentication and app-server integration documented at
https://learn.chatgpt.com/docs/auth and https://learn.chatgpt.com/docs/app-server.
No new live model inference was performed. Native Linear remains locked; no issue
update or completion is claimed. Production skill validators, statistical outcome
regression, long-term receipt retention and full PRD acceptance remain open.
See docs/configured-learning-supervision.md for operational constraints.

HTTP memory-export checkpoint: prior CLI/SDK snapshot progress was verified and
backed up as98df5c5; this turn began clean and extends the same application
operation to authenticated POST `/v1/memory/export`. Request JSON accepts exactly
`{"version":1}`; callers cannot select scope or a partial page. Existing bearer
authentication, browser-origin denial, strict UTF-8/JSON/body admission,
two-request shared memory capacity and five-second cooperative operation deadline
apply. The CLI daemon wiring uses Service.ExportMemory directly. Backend errors,
including custom conflict errors, are generic503 rather than mutation-CAS409.

Snapshot validation and compact buffering precede success headers. The envelope
retains the1,000-fact/8MiB limit plus a newline; exact Content-Length advertises
the complete response. Native HTTP writes have a fifteen-second deadline;
embedded writers without deadline support must bound their own I/O. Review
identified that outer handler panic recovery could append an error after a
partial writer panic; local committed-response containment now prevents this,
including panics while clearing write deadlines. No write retry, second error
document, inference, memory mutation or automatic exported-file storage occurs.

Agents supplied API security/unit tests and actual HTTP→application→SQLite
integration, with independent review. Unit tests passed three race runs5.713s
covering strict/auth admission, invalid/oversized/canceled backend snapshots,
shared capacity, deadline setup failure, short/error writes and write/reset
panics. Integration tests passed three race runs5.628s, proving configured scope,
private/expired inclusion, old stored credential redaction without changing
stored content or last-use, retired omission, empty snapshots,1001-fact overflow
without partial output and zero inference requests. Native make build and Linux
amd64 production cross-build passed. New API export tests also executed as a
CGO-free Linux arm64 binary in existing Alpine3.22, with unprivileged user,
read-only root and no external network; this includes actual HTTP/SQLite tests,
not just cross-compilation. Linux tests were not race-instrumented. Full make
check passed (format/LOC, vet, native race suite and production build), including
API10.900s, app149.547s, telemetry110.620s, CLI36.010s and SDK19.464s. No live
model inference or user database/configuration changes were performed. Larger
exports and full PRD acceptance remain
open. Native Linear was rechecked and remains locked; no issue update or
completion is claimed.

Next learning-gap audit: PRD9.4 still leaves standalone daemon validator
configuration open. CLI serve starts StartLearning; the learner drafts but only
activates when supplied a trusted validator. Validated application/SDK paths and
durable regression monitoring already exist. The next end-to-end milestone is
explicit versioned validator selection and daemon lifecycle integration, with
qualified deterministic validation rather than structural/model self-approval,
plus activation/use/regression/restart evidence. This is an inspected gap and
proposed next milestone, not implemented functionality or acceptance evidence.

Consistent memory-export checkpoint: the previous dispatched-read recovery turn
made verified progress and was pushed as9fa4788; this turn started from a clean
worktree. PRD10.4 requires operator export, while existing live pages could mix
concurrent revisions. New configured CLI `memory export --config` and SDK
`ExportMemory` use an optional memory.Exporter contract. SQLite returns all
current scoped facts, including expired/private facts, from one read transaction.
Retired IDs and deleted content are excluded. Canonical metadata-bound records,
SQL-side materialization limits, at most1,000 facts and an exact8MiB encoded
envelope prevent silent truncation or partial returned snapshots. Capture time is
observation metadata, not a database revision or exact snapshot-pin time.

Application management retains the existing scope, disabled-retrieval access,
existing-store-only policy and cooperative five-second deadline. It validates
custom output, copies its slice, redacts admission plus post-read credentials,
rejects secret identities and rechecks the expanded envelope. Custom exporters
must guarantee isolation/completeness; absent support fails without a live-page
fallback. CLI accepts only explicit configured export, never reads stdin, and
buffers compact JSON before stdout. A write error can still leave a partial
external copy; it returns nonzero. No inference, last-use update, migration,
import, secure erasure or database-backup guarantee is introduced.

Agents implemented storage and CLI with independent review. Storage race tests
passed three runs7.910s, including an actual paused SQLite read and concurrent
atomic WAL writer: the paused export sees both old records and the next export
both new records. Initial concurrency fixture setup failed because its connection
predated scalar-function registration; a fresh read-only connection and explicit
early-error/join handling fixed the test. CLI configured/export/process race tests
passed three runs19.883s, including a real owned CLI subprocess. Root app tests
passed three runs2.150s including rotated-secret ownership and expanded-envelope
denial; SDK tests passed three runs1.759s and contract tests1.559s. Full make check
passed (format/LOC, vet, native race suite and production build), including
app150.112s, telemetry110.960s, CLI37.513s and SDK20.526s. Native make build and
Linux amd64 production cross-build passed. The new telemetry, app and CLI tests
also executed successfully as Linux arm64 binaries in the existing Alpine3.22
image with unprivileged user, read-only root and no external network. This
includes the concurrent WAL writer and actual CLI subprocess; CGO-free Linux
tests were not race-instrumented. Independent review found no outstanding issue.
No user database/configuration, credentials or live model inference was used.
Larger exports,
HTTP export, automatic memory learning and complete PRD acceptance remain open.
Native Linear is still locked; no issue update or completion is claimed.

Pending dispatched read-only recovery checkpoint:
the orphan-worker planner now resolves 1–32 already-dispatched, explicitly
read-only pending child calls as fixed host failures, then fails child and worker
atomically. It does not infer actual output or success, invent dispatch, repeat
tools/inference, change fitness, or grant retry/reassignment authority. All past
completed tools must be read-only/no-effect; undispatched proposals, legacy or
write behavior, delegation tools, acceptance/evaluation/error records and
unresolved effects remain outside this path. Synthetic completions use
`tool_failed`, effect `none`, and `{"error":"read_only_tool_interrupted"}`;
the child terminal is `interrupted_read_only_tool`.

The worker retains its exact unlocked execution-image guard through the reserved
SQLite transaction. Up to 64 held child readers are allowed only with matching
full process identity/reference and unchanged before/after metadata snapshots.
The worker transaction leaves child readers untouched; separate terminal-reader
recovery verifies and releases them after failure resolves the pending calls.
Canonical bounded receipt history re-derives the entire synthetic suffix from
the original last-event marker. Existing model-only, resolved-read-only and
pre-child recovery semantics remain unchanged. No schema migration is added;
older recovery binaries do not understand the new child terminal contract.

Agents contributed session planning, telemetry integration/tests and independent
review. Actual application SIGKILL fixtures cover the built-in read handler
after return but before result INSERT (reader released), and before workspace
reader release (reader held), not interruption inside a callback. Real dispatcher
recovery preserves source prefixes, records failed unavailable results, fails
child/worker/parent, separately reclaims readers and keeps repeat receipts stable.
Provider counts remain one coordinator and one child request, with no repeated
tool execution and no recovered private file contents. App race tests passed
three runs4.106s; focused session/telemetry tests passed. Linux amd64 production
cross-build passed. New session, telemetry and both actual app SIGKILL cases
passed as CGO-free Linux arm64 binaries in existing Alpine 3.22 containers with
an unprivileged user, read-only root and no network; Linux was not race-instrumented.
Full native make check passed (format/LOC, vet, full race suite and production
build), including app153.870s, telemetry112.532s, CLI36.847s, SDK24.266s and
sessions13.336s. Native make build passed; final focused session tests passed
three runs17.517s. Read-only CLI checks
reported Codex0.153.4 and ChatGPT login, with no credential reading or live
inference. General pending/write/uncertain-effect recovery and full PRD acceptance
remain open; no Linear completion is claimed.

Pre-child worker recovery checkpoint: previous turn made verified configured
memory CLI progress and backed up a523604; this turn began from a clean worktree.
Inspection confirmed model-stream and resolved-read-only-tool child recovery were
already implemented, but a killed reader owner before child creation remained
unresolved. New pure planning and transactional recovery support `task.started`
alone after lease acquisition, or `task.started`, `worker.started` and optional
heartbeats. They require strict execution-free metadata, explicit read-only parent
dispatch and the existing exact reader/owner/process-guard proof. A broad child
linkage check includes non-start/malformed linked records before and after writes.

Recovery appends only worker failure, updates its head, releases its original
reader and records `orphan_worker_without_child_unlocked` in existing schema23
storage. No child is invented or executed, no output is accepted, no fitness
changes and no retry/reassignment authority is granted. Subsequent parent failure
and reader recovery remain separate proven transitions. Receipt acknowledgement
checks actual bounded raw row sizes, canonical prefix bytes, exact re-derived
terminal and absence of child linkage. Old receipt reasons remain unchanged.

Three agents contributed session planner/tests, telemetry implementation/tests,
and independent review. Review tightened broad child absence and receipt-history
revalidation; root identified and included the earlier lease-before-WorkerStarted
boundary instead of leaving another equivalent held-reader window. Session worker
race tests passed three runs6.713s; all telemetry orphan tests passed11.071s and
new no-child tests passed three runs9.558s. Actual application SIGKILL tests passed
three runs3.636s across both boundaries: real dispatcher fails parent+worker,
retains source prefixes and exactly two submitted task IDs, releases both proven
readers, preserves repeat receipts and admits new writers. Fixture call counts
remain one parent and zero child inferences. Pause hooks exist only in tests.

Full make check passed (format/LOC, vet, native race suite and production build),
including app152.407s, telemetry109.299s, CLI35.851s, SDK23.946s and sessions7.851s.
Native make build and Linux amd64 production cross-build passed. New session,
telemetry and application cases also executed successfully as Linux arm64 test
binaries in the existing Alpine image, with no external network, read-only root,
unprivileged user and only owned binaries mounted. Both actual pre-child SIGKILL
boundaries ran on Linux; CGO-free Linux tests were not race-instrumented. No live
Sol/Ollama inference, user database/configuration mutation or power-loss claim.
Native Linear remains locked; no issue update or completion is claimed. Pending
tool recovery, ambiguous outcomes, writer/uncertain-effect resolution, automatic
reassignment/continuation, stronger isolation and full PRD acceptance remain open.
See docs/orphan-worker-recovery.md for scope and compatibility.

Configured CLI memory checkpoint: the previous turn made verified durable-monitor
progress and backed up cdc68e7; this turn began from a clean worktree. Requirement
audit found that raw CLI memory commands bypassed the configured-scope/redaction
service already used by SDK/HTTP. New `darwin memory ... --config path` commands
now use that same service for list/show/put/delete, including with retrieval off.
Explicit legacy `--db/--scope` access remains distinguishable and cannot be mixed
with configuration, even through empty flags. Creation requires explicit expected
revision zero; correction/deletion retain CAS, privacy and retired-ID protection.

The configured path rejects duplicate or irrelevant flags and strict fact JSON
with unknown/duplicate/case-alias keys, nulls, missing required fields, invalid
UTF-8, unpaired surrogate escapes, extra values or more than128KiB. Read limits
remain100 facts and the application encoded-page bound. Errors omit raw details;
storage must already exist and is not migrated. Read inspection never touches
last-use. Application reads now preserve admission secrets and recheck credentials
after backend calls, suppressing newly sensitive identities/filters and redacting
both old and newly observed text credentials without rewriting stored facts.

Three agents contributed the gap audit/CLI implementation, end-to-end fixtures,
and independent review. Review caught last-wins repeated flags, now rejected.
Configured CLI race tests passed three runs4.361s; application memory race tests
passed three runs2.429s. Owned subprocess tests use the actual RunWithInput command
entry with synthetic-only environment and isolated YAML/SQLite; they prove redacted
storage/output, stale revision rejection and joined command completion. Final
deletion/retirement assertions additionally inspect storage, not just a failed show.

Full make check passed (format/LOC, vet, native race suite and production build):
app150.245s, CLI30.797s, telemetry104.134s and SDK19.596s. Native make build and
Linux amd64 production cross-build passed. After adding final owned-process
deletion/retirement assertions, the complete CLI race suite passed33.709s and
format/LOC plus app/CLI vet passed. Core memory-management and configured CLI tests,
including actual child processes, executed successfully as CGO-free Linux arm64
binaries in an existing Alpine container: no network, read-only root, unprivileged
user, only owned test binaries mounted. Linux execution was not race-instrumented.
No live inference, user database migration
or configuration change was performed. Native Linear remains locked; no issue
status changed. Consistent multi-page export, semantic/background factual learning,
secure erasure, daemon validator configuration and full PRD qualification remain
open. See docs/memory-management.md for the implemented operator boundary.

Durable named monitor checkpoint: starting from
ce65bdc, added catalog-schema5 scheduling state and check intents before trusted
validation, exact pending restart, persisted cadence and lexical fairness. A
definite check failure retains a fixed-code tombstone; receipt-first completion
and failure-first late-callback fencing are enforced under the catalog lock.
No callback exactly-once claim: concurrent read-only validation remains possible.
Application/SDK opt-in driving, caller-owned supervision and read-only inspection
preserve policy and rotating-secret guards. Legacy monitoring is unchanged.

Core focused race tests passed three runs (8.338s); application/SDK durable tests
passed three runs (12.670s/1.703s). Three subagents contributed core, facade tests
and independent review; no blocking safety defect was found. Review qualified
fixed polling as minimum spacing, not an exact check frequency.
No daemon validator or user monitor was enabled. Catalog maps are bounded without
silent eviction; long-term retention, statistical outcome attribution and complete
PRD/Linear qualification remain open. See docs/durable-skill-regression-monitor.md.

Checkpoint verification: make check passed (format/LOC, vet, full native race
suite and production build), make build passed, and Linux amd64 production
cross-build passed. Full race results include app152.604s, telemetry107.093s,
SDK23.716s and skills19.368s. The core, application and SDK durable-monitor tests
also executed successfully as Linux arm64 binaries inside the existing Alpine
container with network disabled, read-only root, unprivileged user and only an
owned test-binary directory mounted. Those Linux runs were CGO-free, not race
instrumented. No new live inference or physical power-loss qualification was run.
Native Linear remains blocked by the locked Mac; no issue status was changed.

Durable regression-operation checkpoint: the previous turn made verified progress
with Linux thermal admission and backup 3da51ce; this turn began from a clean
worktree. Inspection of the skill-learning path found that explicit passing
regression checks had no durable audit receipt and controller retries could not
recognize a completed check. The new FileStore RevalidateAndRollbackOnce and
application/SDK RevalidateSkillVersionOnce bind an operation ID, trusted validator
identity and exact expected activation to a historical receipt. Passing checks
record evidence without changing activation revision; deterministic failure and
rollback commit with their receipt in one existing atomic catalog replacement.

Catalog schema4 introduces a separate bounded map of at most1000 regression
operations. Schema1–3 remains readable; publication and activation receipts are
preserved and later ActivateOnce cannot downgrade the schema. Regression IDs use
a namespace separate from activation-operation IDs. Lookup validates the exact
historical prefix and any rollback's evidence, timestamp and following revision.
Exact retries acknowledge that history without callback or another rollback,
including after later activation changes. Capacity exhaustion rejects new checks
before a callback; receipts are not silently evicted. Read-only inspection creates
or migrates nothing. SQLite schema26 is unchanged.

Three agents contributed core implementation/tests, app/SDK fixtures and an
independent review. No blocking correctness/security defect was found. Review
identified an intentionally qualified preflight race: a matching concurrent commit
can make the application return a generic conflict before its next exact retry
recognizes the receipt. The core serializes commit, not callback execution;
concurrent read-only validators may both run. Current policy and cumulative secret
checks remain on callbacks, inspection and receipt acknowledgement. No default
success validator, generated command, model-judgment authority or daemon engine
was enabled. Existing periodic monitoring retains its legacy in-memory cursor.

Focused race tests passed three runs: core7.041s, app11.757s, SDK2.131s. Test coverage
includes reopened pass/failure receipts, changed later activations, stale/rebound
identities, proof/operation/validator credential collisions and rotation, canceled
contexts, kill switches, ABA changes, unavailable predecessors, malformed receipt
bindings, full capacity and schema compatibility. A corrected test assertion now
uses ActivationCount as the pre-operation history length, not the post-rollback
length. CGO-free Linux/arm64 core and app lifecycle/credential fixtures plus SDK
regression tests also executed successfully in a network-disabled, read-only,
unprivileged Alpine container using only temporary fixture storage. Native build
and Linux/amd64 production cross-build passed. Final full gates and subprocess
qualification are recorded below.

Full make check passed formatting/LOC, vet, native race tests and build
(app149.560s, telemetry105.535s, CLI32.137s, API11.129s, SDK23.699s,
skills18.881s). A subsequently added owned-subprocess test passed three race runs
1.792s: the child commits a deterministic failed check and rollback, signals only
a test boundary before wrapper response, and is then SIGKILLed and joined. Reopen
recovers the exact receipt, source version remains unchanged, history gains only
one rollback, and exact retry invokes zero callbacks or writes. No production
hook was added. The final complete skills race suite passed15.854s with this test;
format/LOC and skills vet also passed. The rebuilt CGO-free Linux/arm64 crash test
executed successfully in the isolated container, and Linux/amd64 skills test
compilation passed. This is process death after completed commit, not interruption
during rename/fsync or power loss. No live model or user data was involved.

Native Linear was rechecked and remains blocked by the locked Mac; no issue
status was changed. The repository backup is made only after these verified gates.

Standalone daemon validation-engine configuration, persistent monitor scheduling
identities, statistical regression baselines/user-feedback attribution, receipt
retention and full crash/power-loss qualification remain required. This is not
full PRD/Linear acceptance, and no user learner or monitor was started.

Linux thermal-admission checkpoint: the previous turn made verified progress
with the public SDK summary workflow and backup 3be5f51. This turn began from a
clean worktree. PRD12 thermal-pressure sampling had an unimplemented Linux path:
mandatory proc/cgroup memory and CPU data were present, but thermal pressure was
always unknown. The host profile now shares its three-second deadline with a
read-only survey of fixed kernel thermal sysfs paths. This uses published
millidegree passive/hot/critical trip points, not invented temperature limits or
fan activity. No OS settings, cooling controls or model processes are changed.

Any valid reached positive threshold denies new local reservations through the
existing budget. False requires complete bounded discovery, a usable passive
trip in every visible zone and no reached recognized threshold; otherwise the
reading stays unknown. Unknown zones cannot erase separately observed positive
pressure. Zero/negative/invalid thresholds, absent sensors, unsupported types,
incomplete pairs and overflow cannot establish absence of pressure. Linux does
not receive a fabricated macOS thermal_state label. Mandatory memory/CPU facts
remain intact on optional sensor failure; cancellation fails the whole profile.

A research/test agent checked official kernel ABI and driver documentation,
implemented filesystem fixtures, and independently reviewed the sampler. Review
found no further concrete defect after fixing a double-suffix index alias and
checking directory type before opening. Root integration tests prove cgroup
preservation, threshold-driven reservation denial, shared deadlines, caller
cancellation and host-only fallback. An initial host-only fixture used malformed
empty cgroup data and correctly failed; it was corrected to valid non-memory
legacy membership without weakening production parsing. Final focused thermal
race tests passed three runs (6.516s).

CGO-free Linux/arm64 thermal tests executed successfully in an existing Alpine
image with no network, an unprivileged UID, read-only root/binary mount, dropped
capabilities and temporary fixture storage. Its actual rebuilt resources command
observed two CPUs and a512MiB cgroup capacity with thermal pressure null because
no usable sensors were exposed. No images were installed, no user sessions or
local model inference were involved, and only owned disposable containers were
removed after completion. This qualifies Linux execution/absence handling, not
physical hot-sensor behavior, atomic sysfs sampling, cooling hysteresis or
active-inference preemption. See docs/linux-thermal-profiling.md.

Native Linear was rechecked and remains inaccessible while the Mac is locked.
No issue status was changed. Full PRD/Linear acceptance remains open.

Verification: make check passed formatting/LOC, vet, the native race suite and
production build (app141.910s, telemetry104.671s, CLI30.045s, API9.496s,
SDK19.490s, resources7.240s). The later suffix-alias regression test passed in
the final focused native race run and rebuilt Linux/arm64 test execution. Native
make build, CGO-free Linux/amd64 production build and resource test-binary
compilation also passed; amd64 tests were not executed. No paid/live inference,
physical sensor stress or complete PRD qualification is claimed.

SDK summary-workflow checkpoint: the preceding CLI-access turn verified installed
authentication but did not advance implementation. This turn resumed the pending
SDK/app changes against the actual worktree. The versioned embedded client now
exposes explicit SummarizeTask, InspectSummaryAttempt, ListSummaryAttempts,
ReviewSummary and SummaryReviewHistory, with public session-record aliases.
They use the existing application generation, approval and continuation policy;
there is no automatic approval, compaction, replay or new model dispatch path.

New shared read facades use existing read-only storage, strict IDs and 1–100-row
lexical pages, ten-second cooperative deadlines, sanitized errors and no partial
results on malformed rows. Review-history reads now share those bounds. Empty
history alone does not establish attempt existence. Pre-canceled SDK calls retain
context errors; mid-flight generation/review retain existing write-path error
normalization and may leave durable state. Inspection returns sensitive complete
proposals, not a metadata-only feed. Source journals remain immutable.

Independent read-only review found no blocking defect and clarified cancellation
and unknown-attempt history semantics in the documentation. Public SDK fixtures
exercise drafting, reopened-client inspection, owned result copies, explicit
review, exact checkpoint provenance, stale review rejection, future-use revocation,
separate explicit attempts and read-only pagination. App fixtures cover malformed
pages, missing-storage noncreation and unchanged source/database contents.
An additional external SDK fixture proves malformed auxiliary output leaves one
inspectable failed attempt, cannot be approved or continued, does not retry and
leaves source state unchanged. Unknown-attempt history/detail semantics are also
asserted. That focused race test passed three runs (2.204s).

Initial full make check passed formatting/LOC, vet, native race tests and build
(app 142.846s, telemetry 103.353s, CLI 30.199s, API 9.957s, SDK 18.824s). Native
make build and CGO-free Linux amd64 production build plus app/SDK test-binary
compilation passed. Linux binaries were not executed. The added failure test is
included in the final gate recorded below; no new live model inference was run
for this SDK facade work. Fixtures use temporary loopback providers and storage,
not actual local models, user files or production operator authentication.

Final make check including the new failure fixture passed formatting/LOC, vet,
the native race suite and build (app 144.157s, telemetry 104.499s, CLI 30.541s,
API 9.566s, SDK 18.382s; unchanged packages cached). Final SDK Linux test-binary
compilation also passed. No automatic-compaction or full MVP claim is made.

Linear access was rechecked: the native app is blocked by the locked Mac. The
connected plugin reports Cyber Operations Harness, not DarwinRouter. No changes
were made to that unrelated workspace or to Linear issue status. The user must
unlock native Linear or reconnect the connector to DarwinRouter for issue sync.
Local progress remains possible; full PRD/Linear acceptance is not claimed.

Native compacted-continuation checkpoint: the previous turn made verified
progress with signed-in Sol summary drafting and backup e57a7c8; the worktree was
clean at entry. The exact Sol native task path now accepts explicit manual
compaction and currently approved stored summaries. A private resolved-checkpoint
binding gate replaces the blanket denial; request flags alone cannot supply
source/approval authority. Existing loadContinuation reconstructs canonical
source context and compares reviewed proposal bytes. The TaskStarted transaction
still rechecks the exact current review before committing input/checkpoint.

Official OpenAI documentation confirms thread/inject_items adds history without
starting generation. The existing typed bridge now receives canonical compacted
messages: original system roles, fixed host reference-data warning, summary JSON
in a user role, and the complete recent suffix including paired historical tools.
Only the final new prompt enters turn/start after a checked import acknowledgement.
No native automatic compaction, historical tool redispatch, automatic approval,
automatic retry, source rewrite or weaker privacy mode is introduced. Completed
cloud-eligible sources only; failed recovered sources retain ordinary uncompacted
continuation, not compaction. Existing context and full-frame bounds remain.

Three agents qualified the application/SQLite path, typed projection/protocol
and an explicitly gated live probe. Application race tests passed three times
5.377s, bridge compaction tests three times 1.883s; combined final-shape tests
passed app 2.934s/bridge 1.816s. Independent review found no new binding/privacy
bypass. An initially overbroad revocation test was corrected against runtime
ordering: context estimation happens after durable TaskStarted. Rejection before
that start prevents import; rejection afterward blocks future direct reuse but
does not cancel the admitted task. Fixtures verify both sides, not a new policy.

The live TestLiveCodexCompactedContinuation passed 4.89s (race package 6.351s), one
launch/stream/close, done=true, recalled=true, checkpoint_exact=true and unchanged
source. A synthetic loopback source/draft plus explicit trusted-host review
fixture supplied a marker only through the approved summary; actual signed-in
Sol recalled it. The observer did not count import RPCs. No user session/files,
actual local-model delegation or live crash recovery was involved.

Final fidelity hardening validates retained original tool JSON before a custom
context engine or map-based redaction can collapse duplicate keys. Duplicate,
escaped/nested duplicate and excessive-depth cases reject before launch with
unchanged source in both built-in and custom-engine paths. All native compaction
race tests then passed three times 7.840s. This validation followed the live run
and is separately fixture-covered. Final make check passed formatting/LOC, vet,
native race tests and build: app 141.429s, telemetry 102.548s, CLI 30.619s, API
9.665s and SDK 18.652s, with unchanged packages cached. Native make build and
CGO-free Linux amd64 production, application-test and bridge-test compilation
passed; Linux execution is not claimed.
Native Linear remained locked; no issue mutation is claimed. Automatic semantic
compaction/validation, in-flight native steering, broader crash/isolation/release
qualification and full PRD/Linear acceptance remain open.

Native session-summary checkpoint: previous turn made verified progress with
reviewed file replacement and backup 84ebec4; the worktree was clean at entry.
SummarizeTask now uses the shared inert auxiliary adapter for the exact signed-in
Sol codex_app_server provider. Mode/source privacy/cost checks precede a durable
started attempt and schema-inclusive context admission; only then does the CLI
launch. The one-minute summarizer deadline includes startup and streaming, with
owned cleanup, no tools, no retries and no automatic application/fitness update.
Source provenance remains derived from unchanged durable history.

Following the official OpenAI app-server per-turn outputSchema contract, native
summarization requests a fresh closed version-1/six-array schema. The host parser
remains authoritative and rejects empty abstention, invalid/oversized output,
unknown/duplicate fields and tools. HTTP keeps schema-off default and accepts
omitted categories; the shared prompt now requests empty arrays where needed.
Native source redaction strictly validates original argument JSON before
decoding structured tool output/arguments, retaining numeric precision and
denying ambiguous duplicate keys. Current-secret guards before launch and after
startup deny changes to admitted input; output redaction refreshes after stream.
Independent review prompted a post-stream metadata check before draft publication;
already persisted started/source identities are not retroactively rewritten.

Three agents contributed schema/ownership tests, application failure/rotation
fixtures and an opt-in live qualification. Focused sessions schema race tests
passed three times 1.519s; native summary application tests three times 6.735s;
combined summary/estimator tests passed sessions 1.280s and app 5.744s. Strict native
source tests passed three times 1.480s, and focused vet passed. Tests cover durable
start before launch, estimator/BeginSummary denial, privacy/mode/config gates,
malformed output, tool proposals, cancellation, unavailable launcher, cleanup,
source immutability and rotated input/output credentials.

Explicit live TestLiveCodexSummaryDraft passed 6.77s (race package 8.233s), launches=1,
streams=1, closes=1, done=true, response=490 bytes. Real signed-in Sol produced one
durable inactive draft from a synthetic cloud-eligible loopback source. The
source events and host provenance were unchanged and owned directory removed.
No raw prompts/drafts were logged. This is not actual Ollama generation, semantic
summary validation, summary import or live crash-recovery qualification. Final
metadata hardening followed that live run and has separate fixture coverage;
all native summary tests including identity rotation passed race tests three
times (8.274s).
Final make check passed formatting/LOC enforcement, vet, full native race tests
and production build: app 138.484s, telemetry 102.434s, CLI 30.412s, API 9.993s and
SDK 18.521s (unchanged packages cached). Native make build and CGO-free Linux
amd64 production/application-test cross-compilation passed; Linux execution is
not claimed. Native Linear remained locked;
no issue mutation or user configuration change is claimed. Native compacted
history import, automatic semantic validation/application, mid-task compaction,
summary-attempt crash reconciliation and full PRD/Linear acceptance remain open.

Reviewed replacement checkpoint: opt-in local replace_file adds full-content
editing of existing UTF-8 regular files, bounded to 64 KiB per preimage/new body.
Exact per-call old/new review is consumed under the shared workspace writer
lease. Pinned roots, two preimage checks, synced staged bytes, atomic rename
visibility and private retained original copies are implemented. This is not
external-writer compare-and-swap: a noncooperating editor can race after the last
check, and the backup preserves the reviewed preimage, not intervening edits.
Basic permission bits survive; ownership/ACLs/extended attributes/hard-link
identity do not. Before-publication cleanup failure and after-publication
uncertainty are not replay authority. No automatic recovery/cleanup is enabled.

Configuration defaults off and redacts its root. Cloud routes, unattended calls,
durable submissions and delegated workers cannot use this built-in write tool.
SDK and terminal chat reuse existing exact approval controls; the complete
ASCII-quoted OLD/NEW preview includes hashes and limits and rejects configured
secrets instead of redacting the proposed edit. Repository recovery copies are
ignored by Git; other configured workspaces need their own exclusion.

Three agents implemented wiring/SDK tests, CLI/PTY coverage and handler/runtime
failure fixtures. Focused race tests passed three repetitions: application
4.913s, CLI 9.113s, config 1.222s, tools 1.372s and SDK 2.284s; focused vet passed.
Tests include changed content during approval, retained original bytes/private
modes, final-target rejection, pinned-root behavior, real terminal approve/deny,
input/output pipe rejection, and a post-effect SQL completion failure leaving
spent approval and uncertain journal state without another execution. Independent
review found no blocker under the cooperating-writer assumptions and prompted
clearer backup caveats and matching preview/handler argument validation.
Final preview-path/no-op hardening passed race tests three times (3.418s) and
CLI vet. Full make check passed formatting/LOC enforcement, vet, native race
tests and production build: application 138.985s, telemetry 102.451s, CLI 29.846s,
API 10.491s and SDK 18.635s. Native make build and CGO-free Linux amd64 production
and application-test cross-compilation passed; Linux execution is not claimed.
No live inference,
user-file editing, user configuration enablement or Linear update is claimed.
Native Linear was locked; complete PRD/Linear qualification remains open.

Native skill-generation checkpoint: previous goal turn made verified progress
with periodic deterministic regression monitoring and backup f268d17. Worktree
was clean at entry; native Linear remains locked and no issue mutation is claimed.
The exact gpt-5.6-sol signed-in Codex provider now supports explicit and selected
skill drafting, and is accepted by the enabled learning model gate. Auxiliary
launch is inert until a durable generation/budget claim and schema-inclusive
context admission. The request has trusted system instructions, untrusted
source data, no tools and a closed eight-field schema; host parsing/provenance
remain authoritative. Empty schema-compatible abstention is rejected, rather
than forcing invented workflows. HTTP generation keeps its schema opt-in default.
Native input digests include executable/schema mode; full-config selections
reject executable changes. Originals are decoded before escaped-secret redaction;
case aliases/duplicate keys fail. Credential changes after estimation or startup
reject changed admitted input before streaming. No user learning config changed.

Three agents contributed structured schemas, protocol/application tests and
independent lifecycle/binding review; root integrated providers/configuration,
credential refresh guards and docs. Focused generation/learning tests passed
app 26.937s, config 1.738s and skills 1.779s. Later focused schema-abstention tests
passed three repetitions 1.700s; native generation tests 11.236s, rotation guards
3.451s, strict escaped-source tests 1.630s and protocol tests 1.513s, all with race
detection. Skills/app vet passed. A private-source test setup initially failed
its own task admission; it now constructs a valid local-private source before
asserting that mixed-privacy generation cannot launch Codex.

Explicit live qualification passed: TestLiveCodexSkillDraft 12.51s (race package
14.070s), launches=1, streams=1, done=true, response bytes=1106. Exact signed-in Sol
generated a durable inactive proposal from two synthetic accepted Square/Cube
Go-code outputs served by a loopback fixture. Host provenance and source snapshots
were unchanged, the owned working directory was removed, and no skill catalog
was created. No raw prompt/output was logged. This used one real cloud call, not
actual local-model inference or production learning validation. The ordinary
suite skips it. Standalone validator-engine
configuration, statistical regression detection and full PRD acceptance remain open.

Final `make check` passed formatting/LOC, vet, full native race tests and build:
app 138.343s, telemetry 105.575s, CLI 30.998s, API 10.533s, config 3.310s,
SDK 21.254s and skills 14.902s. Native build and CGO-free Linux amd64 production
build/app-test compilation passed; Linux binaries were not executed. Fetch
confirmed HEAD/origin-main 0/0 before commit. Only source, tests and documentation
are included; no credentials, generated binaries or live task databases.

Regression-monitor checkpoint: the previous goal turn made verified progress
with opt-in validated learning and backup 00b5327. This turn confirmed a clean
worktree; native Linear is still locked and no issue status was changed.
The Go-host API/SDK now supports explicitly started periodic deterministic
revalidation with automatic revision-fenced rollback. Each tick checks one
current active skill, advances past individual callback failures and retains
sticky degraded health. Scope-isolated read-only pagination covers more than
100 active entries. Cancellation joins cooperative validation; restart derives
authority from the current durable catalog rather than an in-memory cursor.
Three agents supplied enumeration, SDK integration, tests and independent review;
root implemented the monitor, health/readiness integration and documentation.

Focused evidence: skills pagination race tests passed 1.746s, app monitor tests
passed three race repetitions 13.019s, SDK monitor tests passed three repetitions
2.309s, and health race tests passed 1.396s. App/skills vet passed. Actual background
fixtures verified deterministic rollback, failure isolation and joined shutdown;
all data was synthetic. No inference, user monitor, or user catalog migration was
performed. Review found no rollback-authority or cancellation defect, but unsafe
metadata/secret admission can still prevent later-key discovery; this is documented.
Standalone daemon validators, semantic/statistical outcome regression detection,
persisted passing-check audit records and durable scan fairness across frequent
restarts remain open.

Final `make check` passed formatting/LOC, vet, full native race tests and build:
app 135.470s, telemetry 104.423s, CLI 31.354s, API 10.350s, SDK 21.675s and
skills 15.613s. Native build and CGO-free Linux amd64 production build plus app
test-binary compilation passed. Linux binaries were not executed. Fetch confirmed
HEAD/origin-main 0/0 before commit; no user files, credentials, catalogs or
databases are included in the checkpoint.

Validated-learning checkpoint: the previous goal turn delivered operation-keyed
activation and backup7a729aa. This turn confirmed a clean worktree; native Linear
remains locked. A new opt-in Go-host learning path binds validator identity into
policy, persists an immutable schema26 intent before validation, and uses the
existing catalog operation receipt before acknowledging the learning cursor.
The SDK offers single-step and explicitly started, caller-owned supervisors plus
read-only intent inspection. The ordinary daemon remains draft-only. Three agents
implemented schema/contracts, SDK integration and workflow/supervisor tests while
root integrated the durable phase progression and receipt reconciliation.
No default success validator, remote proof endpoint or user learner was enabled.
Standalone daemon validation configuration, intent resolution, regression
monitoring and full PRD qualification remain open.

Final `make check` passed: formatting/LOC, vet, full race suite and build;
app 127.629s, telemetry 101.820s, CLI 29.754s, API 9.924s and SDK 18.308s.
A test-only Service mutex copy was caught by vet and replaced with a fresh
constructor before the final run. Focused missing-generation regression passed
three race runs (3.413s): a durable activation intent prevents redispatch when
its generation row is missing, preserving cursor/intent and callback counts.
The real background-supervisor fixture also traversed discovery through
validated activation without manual ticks. Native build and CGO-free Linux
amd64 production build/app-test compilation passed; Linux binaries were not run.
All new learning evidence uses synthetic databases/catalogs and loopback fixtures;
no user data was migrated, no live inference was run, and no learner was enabled.
Read-only CLI verification reported codex-cli 0.153.4 and ChatGPT login; the existing
smoke configuration still selects exact gpt-5.6-sol. Fetch showed HEAD/origin-main
0/0 before the checkpoint commit. Native Linear remains unavailable while locked.

Activation-operation checkpoint: the previous goal turn made verified sweep
progress and backupe902eff. This turn confirmed a clean worktree and found a
durable-retry prerequisite for automatic skill activation. File catalog schema3
now records operation ID, prior revision and deterministic evidence alongside
the activation in one replacement. Exact retries recognize historical completion
without invoking validation or reactivating after rollback; conflicting bindings
fail. App/SDK wrappers retain scope, policy, secret and validator gates. Legacy
catalog revisions remain compatible and publication cannot downgrade schema3.
Three agents contributed contracts, adapters, tests and independent review;
root implemented execution and concurrent-retry handling. Background validation
engine integration and activation remain unfinished; no model was run or learner
enabled. Native Linear remains locked and no issue mutation is claimed.

Activation-operation focused evidence: full skills race tests passed13.408s and
operation tests passed three repetitions6.320s. App/SDK guarded tests passed
three repetitions4.561s/1.988s, including a validator that rotates credentials to
the operation ID: no receipt or activation is persisted. Independent review
verified preflight/commit operation binding, epoch checks and retry after rollback.
The first aggregate gate caught a test copying Service's mutex; the fixture now
constructs a fresh Service instead. Native build and CGO-free Linux amd64
production/skills test-binary compilation passed; Linux execution and power-loss
qualification were not tested. All catalogs were synthetic, not user data.

Activation-operation final gate passed: make check completed formatting/LOC, vet,
full native race tests and build, including app117.702s, telemetry102.612s,
CLI30.271s, API10.792s, SDK20.453s and skills15.539s. Git fetch confirmed no
divergence. No live inference or user-catalog migration occurred. This is durable
activation retry recognition, not completion of automatic learning/validation or
the full PRD; Linear updates remain pending while the Mac is locked.

Attention-sweep checkpoint: the previous goal turn delivered verified atomic
history and GitHub backup16adb7e. This turn confirmed a clean worktree and native
Linear remains locked. The daemon now selects a bounded candidate page, releases
that read snapshot, and observes each exact row in its own writer transaction.
Malformed metadata or rejected writes roll back only that candidate; later valid
observations/history can commit. Errors remain visible in supervisor health, and
failed candidates are revisited after cursor wrap. Selection failures preserve
the input cursor; mid-sweep cancellation advances only through attempted rows.
The existing whole-page atomic storage API remains unchanged. No migration,
automatic repair, lease release, ownership probe or task retry was introduced.
Three agents supplied storage fault tests, real dispatcher integration and
independent boundary review. Final verification follows; full PRD work remains.

Sweep-focused race tests passed three repetitions4.702s after all test additions,
including exact-row deletion/renewal, maximum row ID, malformed first/middle
candidates, ignored/aborted history insert rollback and deterministic cancellation
during the second candidate. The real dispatcher later-page test passed6.641s:
attention/history was created beyond a poisoned first page while supervisor_error
remained visible, with no lease/journal changes, recovery receipts or model calls.
Independent review found no correctness blocker. Native build and CGO-free Linux
amd64 production/telemetry test-binary compilation passed; Linux execution was not
tested. All fixtures were synthetic; no user database or live model was used.

Sweep final verification: make check passed formatting/LOC, vet, full native
race suite and build, including app114.985s, telemetry98.603s, CLI29.959s,
API9.771s and SDK17.479s. An earlier aggregate run used the cancellation fixture
before its SQLite callback was registered prior to connection creation; corrected
focused tests and the frozen-file aggregate rerun passed. No production change
was required for that fixture issue. Git fetch confirmed no divergence. This
closes corrupt-candidate sweep starvation, not general database repair or full
PRD acceptance; native Linear issue updates remain pending while locked.

Attention-history checkpoint: the previous goal turn made verified API and
observer-race progress backed up as 52a19dd. This turn confirmed a clean worktree;
native Linear remains locked and no issue mutation is claimed. Schema25 adds
per-record append-only observation transitions. Migration preserves existing
state as a labeled baseline without inventing prior events. New and changed
observations append history in the same transaction as the current projection;
unchanged observations do not append. CLI, SDK and authenticated HTTP expose
bounded read-only history. Three agents contributed contracts/migration, adapters
and API tests while the root implemented storage and atomic observer integration.
Final verification follows. History is not execution authority or cryptographic
tamper evidence; acknowledgment, retention, broader supervision and full PRD
qualification remain unfinished.

History qualification covers observed lifecycle sequences, unchanged scans,
baseline migration preservation, strict pagination and privacy, read-only reopen,
head/projection mismatch, missing history, malformed canonical snapshots, sequence
gaps/overflow and rejected/ignored inserts rolling back both projections. Actual
daemon HTTP wiring verifies missing404 and a synthetic baseline without execution.
The first aggregate run exposed an old corruption fixture deleting an attention
row without its new dependent history; the fixture now removes both synthetic
records before exercising page rollback. No runtime deletion path was added.

Attention-history final verification: make check passed formatting/LOC, vet,
full native race tests and build, including app111.183s, telemetry99.331s,
CLI29.148s, API9.346s and SDK17.465s. Storage history/migration race tests passed
three repetitions4.928s; the separately added concurrent two-connection observer
test passed three repetitions1.770s. Real daemon HTTP history passed2.682s;
SDK/CLI focused race tests passed three repetitions1.875s/2.074s. Native make build
and CGO-free Linux amd64 production/API test-binary compilation passed; Linux
execution was not tested. The schema cap is25, future26 is rejected, and existing
feature thresholds remain unchanged. Only synthetic databases were migrated.
Git fetch confirmed no divergence; full PRD completion is not claimed.

HTTP attention checkpoint: the previous turn made verified schema24 durable
attention implementation and backup progress (0550c75). This turn rechecked a
clean worktree; native Linear remains locked and no issue update is claimed.
Authenticated GET /v1/resources/attention exposes the same read-only records as
CLI/SDK with default open/25, strict state/after/limit queries, body rejection,
bounded control admission, cooperative five-second deadlines and generic errors.
Returned pages are validated against requested filter, cursor and size as well
as public metadata contracts. Neither the route nor its app adapter writes
records, probes guards or dispatches models/tools. Two agents supplied HTTP
contract and actual-SQLite tests; a third verifies actual serve-process wiring.
Final aggregate evidence follows below. Broader attention actions, retention,
notifications, uncertain effects and full PRD qualification remain unfinished.

HTTP checkpoint qualification exposed an existing attention-observer transaction
race: the initial full suite failed two dispatcher shutdown assertions, and
isolated repeated tests reproduced it. A two-connection storage regression
reproduces SQLITE_BUSY_SNAPSHOT (517) when a schema read precedes writer
reservation. The observer now reserves the writer with a zero-row update before
reading the schema, preventing that stale-snapshot upgrade without changing
lease ownership or dispatching work. The exact schema guard remains mandatory;
unsupported schemas roll back. Final post-fix verification is recorded below.

HTTP attention final verification: post-fix make check passed formatting/LOC,
vet, full native race suite and build (app109.602s, telemetry96.644s,
CLI29.128s, API9.160s, SDK17.460s). The two formerly failing app tests passed
all ten isolated repetitions58.294s; deterministic reservation and schema23/25
rejection tests passed three repetitions2.145s. Final schema-test additions were
also checked separately because the aggregate run had already started. Native
make build and CGO-free Linux amd64 production/API test-binary compilation passed;
Linux execution was not tested. Actual HTTP daemon and SQLite tests prove no task
dispatch or inspection-time record writes. Independent review found no safety
blocker in the reservation fix. No user database, configuration or live model was
used. This delivers HTTP inspection plus the observer contention fix, not full
MVP acceptance; native Linear remains locked and no issue update is claimed.

Lease-attention final verification: make check passed format/LOC, vet, full
native race suite and build, including app108.731s, telemetry95.869s,
CLI29.238s, SDK18.078s and API9.007s. Additional ignored-insert/update fault
tests passed three repetitions2.005s, proving an ignored write cannot report
successful observation. Real dispatcher persistence passed three runs1.830s;
storage lifecycle/corruption/privacy tests passed three runs5.038s; SDK/CLI
focused race tests passed three runs2.413s/1.709s. Native make build and
CGO-free Linux amd64 production plus application/telemetry test-binary builds
passed; Linux execution was not tested. Independent review found no unauthorized
probe/release path and confirmed the documented corrupt-page limitation.
No user database/configuration or live model was used. Git fetch found no
divergence. This is durable expired-lease attention, not full PRD completion.

Durable attention checkpoint: the previous goal turn delivered verified
read-only-child recovery and GitHub backup (94367cd). This turn rechecked a clean
worktree; native Linear remains locked, with no issue mutation claimed. Schema24
adds private lease-linked attention records. The daemon observes expired
unreleased readers/writers, including legacy or unverifiable owners, after its
recovery passes. Renewal/release resolves a record; recurrence reopens the same
ID. Unchanged observations preserve bytes and timestamps. Observation never
probes guards, releases leases, changes task journals or dispatches work. Public
CLI/SDK metadata omits tokens, owners, scopes, guard references and content.

Three agents supplied schema/contracts, SDK/CLI adapters, storage qualification
and real dispatcher integration. Focused race tests passed migration/contract,
storage lifecycle/corruption/rollback, repeated SDK/CLI and daemon-persistence
cases. Schema max checks and future-version rejection fixtures advance to24/25;
feature introductions remain unchanged. Only synthetic databases were migrated.
The observer intentionally fails an entire corrupt page and retains its cursor,
so corruption can block later attention pages and degrade health. Record writer
and task identity drift is rejected, not updated. These are latest-state
observations, not immutable transition history or execution authority. HTTP,
acknowledgment, notifications, retention, broader stall reasons and full PRD
acceptance remain open. Final verification follows; see docs/lease-attention.md.

Read-only-child final verification: make check passed format/LOC, vet, full
native race suite and build, including app106.793s, telemetry91.878s, CLI29.032s
and SDK16.616s. Full sessions race4.279s covers exact 10,000-event/8MiB limits,
multiple tools and forged terminal rejection. All orphan storage regressions
passed8.019s; new and existing real model-stream SIGKILL application cases passed
three repetitions3.762s. Tests cover completed read_file before interruption,
released read lease, actual request disconnection, child/worker/parent failure,
unchanged source pairs, idempotent receipts, no repeated model/tool work, six
atomic rollback boundaries and commit-time conflicting child ownership.

Native make build and CGO-free Linux amd64 production/application/telemetry
test-binary builds passed; Linux execution and power loss were not tested.
Independent review found no concrete unsafe acceptance/reclamation path. Only
synthetic providers/databases were used; no user content/configuration or live
inference was accessed. Git fetch found no divergence. Pending tools, uncertain
writes, unknown ownership, persistent operator attention, reassignment and full
PRD acceptance remain unfinished; the overall goal stays active.

Read-only interrupted-child checkpoint: the previous turn made verified durable
guard-location implementation and backup progress (e18f328). This turn rechecked
a clean worktree; native Linear remains locked and no issue mutation is claimed.
The worker recovery planner now distinguishes a model-only child from one with
fully resolved, explicitly read-only current-journal tool pairs. The latter gets
interrupted_read_only_model failure, never accepted output. Initial uncertain
dispatch markers require matching no-effect completions; pending tools, writes,
legacy behavior, delegation tools, evaluation/errors and independent unreleased
child holders remain excluded. Existing worker ownership proof, transaction,
receipt binding and parent failure reconciliation remain in force. Model-only
submission recovery is not broadened. Three agents supplied planner, storage and
actual application crash qualification; final verification follows below.

Durable-location final verification: make check passed format/LOC, vet, full
native race tests and build, including app106.445s, telemetry91.102s,
processguard3.766s, CLI28.302s and SDK16.364s. The final additional CLI environment
isolation change passed the complete CLI race suite27.945s. Native make build,
CGO-free Linux amd64 production build and processguard/application/telemetry
test-binary compilation passed; Linux tests were not executed. Focused repeated
guard and real subprocess SIGKILL/exec/recovery tests passed after all isolated
root fixes. Independent review found no unsafe ownership/reclamation path.
Git fetch found no divergence. Full PRD qualification remains open.

Durable ownership-location checkpoint: the previous turn delivered verified
scope-holder inspection and GitHub backup (3684df7). This turn rechecked a clean
worktree; native Linear remains locked, so no issue mutation is claimed. New
process guards default to UserConfigDir/DarwinRouter/process-owners, with an
explicit DARWIN_PROCESS_OWNER_DIR override resolved only at first acquisition.
Private roots are validated without repair, children are created through pinned
handles, and file/child/root entries are synced before publishing ownership.
Existing reference version/schema and root-independent probes are unchanged;
old temporary references are neither moved nor recreated. Environment changes
do not rotate an established singleton. Creation-time parent trust is distinct
from ongoing per-guard identity verification. Garbage collection, disk-growth
bounds, reboot/host proof and power-loss qualification remain unfinished.

Two agents provided design/security review and one supplied location and
subprocess tests. Makefile check/test now isolate synthetic ownership roots;
stripped-environment crash fixtures explicitly supply private roots. Initial
qualification caught test-fixture permissions and a missing test import; final
verification is recorded after correction. Additional stripped/filtering child
environments were found during broad qualification and now receive isolated
roots. The initial filtering fixtures created private guard metadata in the
default application directory; it is retained, not confused with user-task
ownership or automatically deleted. No user database or live inference was used.
See docs/process-lifetime-ownership.md.

Scope-holder final verification: make check passed format/LOC, vet, the full
native race suite and build (app105.695s, telemetry90.457s, CLI28.507s,
SDK16.430s, API8.739s). Native make build and CGO-free Linux amd64 production
build plus API/telemetry test-binary compilation passed; Linux tests were not
executed. Git fetch confirmed no divergence. No live inference or user database
was used. This checkpoint completes bounded per-scope inspection, not the full
PRD or automatic lease recovery requirements.

Scope-holder inspection checkpoint: CLI resources leases, SDK InspectScopeLeases
and authenticated GET /v1/resources/leases now identify task holders in a bounded,
coherent read-only snapshot. Matching uses exact scopes plus the existing
workspace/create_* compatibility family. Expired unreleased holders remain;
released rows do not. Responses expose requested scope and task IDs (potentially
sensitive), but not stored alias names, owner/token/guard capabilities or content.
Schemas 1–2 explicitly report unavailable; schemas 3–23 support the observation.
More than 1,000 overlapping rows, invalid metadata and duplicate exact-scope
writers fail closed without partial results. Task heads are checked, not entire
holder histories. No process probing, migration, release or retry is introduced.

Three agents supplied storage/contracts, app/SDK/CLI adapters and HTTP tests.
Focused race tests passed for workers/telemetry, repeated SDK/CLI tests, and the
full API suite. Only synthetic databases were used, including unchanged main DB
byte checks and missing-store noncreation; SQLite coordination sidecars remain
possible. Full checkpoint verification is recorded below after completion.
Native Linear remains locked; no issue update is claimed. Persistent attention,
global listing, uncertain writes, reassignment and full PRD acceptance remain
open. See docs/scope-holder-inspection.md.

Final task-lease inspection verification passed: make check completed after the exact-limit addition, including telemetry83.442s and cached passes for the remaining previously qualified packages. Format/LOC, vet and build passed; no production changes followed the successful native/Linux builds. Git fetch confirmed no divergence. This checkpoint adds inspection only, not resource release or completion of the full PRD.

Task lease inspection checkpoint: the previous goal turn made verified interrupted-child implementation and GitHub backup progress (555be6b); this turn rechecked a clean worktree. Native Linear remains locked, so no issue status/comment was submitted. CLI task leases, SDK InspectTaskLeases and authenticated GET /v1/tasks/{id}/leases now report one coherent bounded task/head/lease/recovery summary. The response excludes prompts, outputs, tool arguments, tokens, owners, scopes, guard paths and receipt bodies. Counts are task-owned, not a global blocker list or admission/proof-of-death decision. Explicit null groups distinguish older schemas lacking leases/recoveries from actual zero counts. No schema migration or new recovery authority is introduced.

Three agents supplied storage/contract, SDK/CLI adapters and HTTP qualification. Focused race suites passed three repetitions across all surfaces; independent review found no concrete unsafe path. Initial make check passed format/LOC, vet, full race suite and build, including app105.494s, telemetry86.935s, CLI28.474s, SDK16.540s and API8.568s. Native build and CGO-free Linux amd64 build plus telemetry/API test-binary compilation passed; Linux tests were not executed. A subsequent exact-1,000-holder test passed2.760s, complementing 1,001-holder rejection; final full checks include that addition before backup. Tests use synthetic databases and actual recovery receipts, validate unchanged main database bytes/journals, no missing-store creation, generic errors, cancellation, strict HTTP/CLI input and impossible status rejection.

No live inference or user database/configuration/content was accessed. SQLite read-only opens may create ordinary WAL/SHM coordination sidecars, not task/lease/receipt mutations. Inspection does not touch process guard files or infer liveness from expiry. Persistent attention, per-scope holder inspection, uncertain-write resolution, idempotent reassignment and full PRD acceptance remain unfinished. See docs/task-lease-inspection.md; the overall goal stays active.

Interrupted-child recovery checkpoint: the previous turn made verified orphan-worker implementation and backup progress (80e2e30), and this turn rechecked a clean worktree. Native Linear remains locked; no issue mutation is claimed. The daemon's existing worker sweep now handles a running model-only execution child: retain the original worker process proof, validate the child prefix and parent dispatch binding, then atomically append interrupted_model child failure and worker_owner_interrupted worker failure, update both heads, release the exact worker reader and store a receipt bound to both terminals. Existing parent reconciliation records failure afterward. No partial output, completed turn, acceptance evidence, inference retry or tool execution is invented. Child InterruptedTurn remains intact.

Qualification passed: make check (format/LOC, vet, full native race suite and build), including app106.308s, telemetry86.444s, sessions4.673s and CLI28.330s. Combined real application SIGKILL cases passed three repetitions5.215s; focused storage child tests passed three repetitions5.732s. They cover actual request disconnection, preserved source prefixes, child/worker/parent failure, unchanged model-call counts, recovered writer availability, receipt idempotency, six atomic rollback boundaries, cross-connection single commit, canonical child-receipt corruption, and rejection of tool/evaluation/uncertain or independently leased children. Three agents supplied planner, storage tests, app qualification and independent review; no concrete unsafe mutation path was found. Native build and CGO-free Linux amd64 build/application+telemetry test-binary compilation passed; Linux execution and power-loss qualification remain absent.

Only synthetic databases and loopback providers were used; no user configuration/content/database migration or live inference was performed. Receipt inspection validates paired terminal metadata, not a full historical audit. Process proof covers cooperative local execution-image lifetime, not remote generation or billing cessation. Pending tool-aware children, missing outcomes, writer/uncertain-effect reconciliation, durable operator attention, idempotent reassignment, automatic continuation and robust guard retention remain unfinished. See docs/orphan-worker-recovery.md. Full PRD acceptance remains unproven and the goal stays active.

Orphan-worker recovery checkpoint: the previous goal turn made verified terminal-reader implementation and GitHub backup progress (75c5e08); this turn rechecked a clean worktree. Native Linear remains locked, so no issue status/comment was submitted. The daemon now sweeps running worker readers before parent submission reconciliation and terminal-reader reclamation. It requires retained unlocked ownership proof, exactly one historical reader, strict supervisor lifecycle, one already-terminal effect-resolved child and an actual dispatched parent-origin binding. One transaction appends worker_owner_interrupted failure, updates the head, releases the reader and records an event-bound private schema-23 receipt. Preliminary acceptance never becomes recovered success, and no model/tool is rerun.

Qualification passed: make check (format/LOC, vet, full native race suite and build), including app107.522s, telemetry85.436s, sessions4.366s and CLI29.230s. Focused real SIGKILL app tests passed three repetitions4.076s; expanded storage race tests passed three repetitions12.892s. They cover preserved source history, failed parent/worker outcomes, unchanged child, no repeated inference, freed writer scopes, receipt idempotency, concurrent recovery and transaction rollback after guard/cancellation/reference/parent/child damage. Three agents supplied planner implementation, storage tests, app qualification and review. No concrete unsafe release path was found. Native build and CGO-free Linux amd64 build/test-binary compilation passed; Linux runtime and power-loss behavior are not qualified. Only synthetic databases/providers were used; user configuration/content and live models were untouched.

Remaining requirements are explicit in docs/orphan-worker-recovery.md: running or missing child outcomes, uncertain effects, writer recovery, persistent operator attention, idempotent reassignment, automatic continuation and robust guard retention. Non-submitted worker planning is supported, but automatic parent journal reconciliation still targets submitted work. Source verification compares canonical event content, not arbitrary raw unknown JSON metadata. The private receipt binds terminal metadata but repeat inspection is not a full historical corruption audit. Full PRD acceptance remains incomplete and the goal stays active.

Terminal-reader recovery checkpoint: schema 23 adds private, atomic recovery receipts. The daemon now sweeps bounded pages of terminal reader leases using an independent cursor. Release requires a retained unlocked process guard, revalidated identity and immutable lease metadata, and full bounded replay proving a terminal task with no pending calls, uncertain effects or interrupted turn. The receipt and release commit together. No model/tool is dispatched, no task journal or acceptance evidence is rewritten, and expiry alone never authorizes release. Writers, running workers, legacy ownership and missing/damaged guards remain blocked. See docs/terminal-reader-recovery.md for bounds and limitations.

Qualification passed: make check (format/LOC, vet, full native race suite and build), including app104.004s, telemetry79.906s, processguard2.643s and CLI28.823s. Native make build, CGO-free Linux amd64 build and application/telemetry/processguard Linux test-binary compilation passed; Linux tests were not executed. Real subprocess SIGKILL tests prove the actual dispatcher can release a verified orphaned parent reader after journal repair, preserve all journals and receipt bytes on repetition, and admit a new writer without another inference call. Running-worker crash cases remain unreleased. Three subagents provided storage implementation, guard tests and independent review. No user database/configuration or live inference was touched. Native Linear updates remain pending Mac unlock; broader worker/writer recovery and full PRD acceptance remain open.

Final process-ownership verification: make check passed after all guard hardening (format/LOC, vet, full native race suite and build), including app103.019s, telemetry77.923s, processguard2.433s and CLI28.649s. Native make build, CGO-free Linux amd64 production build and processguard/telemetry Linux test-binary compilation passed; Linux execution was not performed. Independent final review found no concrete blocker and confirmed that identity/header/path checks and special-file nonblocking probes preserve conservative unknown states. Git fetch confirmed no divergence. The schema and fixture updates preserve older data rather than downgrading it; only synthetic test databases were migrated. This checkpoint is ready for code backup; full recovery and whole-project acceptance remain unproven.

Process-ownership foundation checkpoint: the previous goal turn made verified crash-qualification and backup progress (0509e6c); clean HEAD was rechecked. Native Linear is still locked, so no issue update was submitted. PRD worker recovery lacks trusted lifetime ownership: expiry does not prove a stopped callback, and logical owner/token alone previously allowed use from another process. New schema-22 leases now bind transactionally to a shared private OS lifetime guard, with same-image validation on renewal, release, worker append/finalization and approval consumption. Read-only/control opens do not create guards. Legacy NULL ownership is preserved, never backfilled or interpreted as dead.

The guard uses one retained file/directory pair per execution image across all Store handles. It verifies private UID/mode, regular file/link count, canonical path, pinned inode identities and immutable random-ID contents; missing/replaced/insecure paths fail without recreation. Independent probes retain acquired locks until Close and cannot release a live owner's separate lock. CLOEXEC deliberately releases ownership on exec replacement as well as exit, so this is not PID-death or remote/descendant-termination proof. Local filesystem allowlists exclude network/overlay/unqualified platforms. Temporary-directory retention, durable configurable location, cleanup, host/reboot proof and automatic orphan reclamation remain explicit gaps; see docs/process-lifetime-ownership.md.

Focused evidence includes real foreign-process denial even with a known token and approval, Store.Close/reopen identity persistence, schema-21 migration preserving unknown leases, registration rollback and corrupt metadata fencing. Real guard subprocess tests cover concurrency, SIGKILL, same-PID exec replacement, probe isolation and tampering, with three race-enabled repetitions passing after ID-content hardening. Existing application/gate/SDK/worker race suites passed (app103.583s, gate17.654s, SDK14.924s); full telemetry passed74.808s before final guard hardening. Full checks and native/Linux builds are running before backup. No user database, configuration or live model request was opened or changed; this implements ownership groundwork, not completion of general recovery or the full PRD.

Process-finalization verification: make check passed (format/LOC, vet, full native race suite and build), including app97.192s and telemetry66.136s; unchanged packages cached. Native make build, CGO-free Linux amd64 production build and both application/telemetry Linux test-binary compilations passed. Linux tests were not executed. Remote fetch confirmed zero divergence before publication; only PRD/docs and crash-test files belong to this checkpoint. No production code, schema, user configuration or live-model behavior changed. General orphan recovery and full PRD qualification remain incomplete, and the goal stays active.

Worker-finalization process-crash checkpoint: the previous turn made verified implementation and backup progress (99603ac), with clean HEAD rechecked before this sprint. Native Linear is still unavailable because the Mac is locked; no status/comment update was submitted. Work continued on PRD durable-boundary recovery qualification instead of treating the UI lock as a whole-project blocker.

New storage subprocess tests pause before terminal insertion, before reader release in the finalization transaction, and immediately after commit. The parent verifies SIGKILL and reaps its owned helper before reopening storage: both terminal and release roll back precommit, both survive postcommit, and exact acknowledgements leave records unchanged. Actual application process tests pause at worker release and enclosing parent-reader release, using loopback coordinator/worker responses. They prove preliminary acceptance cannot substitute for a missing worker terminal, committed worker evidence supports only an interrupted/failed parent repair, and repeated reconciliation changes no child/parent journal, receipt or lease and dispatches no inference.

The separate enclosing reader survives the post-worker-commit crash; recovery deliberately does not release it. Explicit fixture expiry still does not permit a conflicting writer. This exposes an availability limitation rather than claiming general orphan recovery. Tests use private test SQLite pause points, not production hooks, actual model calls, power-loss simulation or a process-death detector. Application focused crash tests passed three repetitions alongside the existing single/batch parent-result crash tests (5.376s); storage crash tests passed three race-enabled repetitions (2.353s). Independent reviewers found no concrete blocker and prompted full second-pass receipt/journal equality assertions. See docs/worker-finalization.md. Aggregate checks, Linux compilation and backup follow; full PRD acceptance remains open.

Worker-finalization verification: make check passed (format/LOC, vet, full native race suite and production build), including app98.957s, telemetry69.385s and workers4.127s. Native make build and CGO-free Linux amd64 cross-build passed; Linux execution was not tested. Three agents supplied storage, supervisor and application test work; focused repeated race tests also passed. Git fetch confirmed no divergence before checkpoint publication. Full PRD completion and the pending native Linear update are not claimed.

Atomic worker-finalization checkpoint: the production redacting journal now commits a joined worker's terminal event, task projection and reader-lease release in one SQLite transaction. Supervisor output requires acknowledged finalization; legacy journal cleanup errors or panics suppress successful output. Orchestration-adapter panics cancel and join callback work before releasing the lease or capacity slot. Normal appends, submission/cancellation fencing, redaction and approval authority remain unchanged.

The new capability requires exactly one historical reader lease for the task/owner, matching the supplied token; ambiguous historical ownership is refused for both new commits and exact retries. No receipt, migration or index was added. The LIMIT 2 query bounds returned rows, not database scan cost. Expired ownership permits cancellation cleanup only. Earlier validator/WorkerCompleted events can precede failed terminal persistence; this is atomic terminal/release visibility, not atomic storage of all acceptance evidence. Fallback release after joined work may leave a nonterminal journal requiring later reconciliation. General crashed-holder recovery and proof of owner termination remain open.

Focused race tests cover rollback on event/release failure, reopen, duplicate acknowledgements, wrong/writer/reassigned/ambiguous tokens, submission fences, cancellation, delayed callback join, legacy cleanup errors and adapter/observer panics. Actual loopback-provider application delegation tests prove failed finalization output cannot reach the next parent model. This is fixture fault injection, not a new process-kill or live-inference qualification. Full checks and backup follow. Codex CLI was independently rechecked: version 0.153.4, logged in using ChatGPT; the existing smoke configuration selects gpt-5.6-sol through codex_app_server. No credentials were read or changed, and no new live inference was dispatched. Native Linear updates remain pending the previously reported Mac unlock.

Final scope-visibility check passed after the exact-limit test: make check completed successfully, including telemetry61.655s and cached passes for the rest of the previously verified suite. No production changes followed the successful native/Linux builds. The checkpoint is ready for GitHub backup; native Linear remains unmodified because the Mac is locked. This advances diagnostic visibility, not full holder discovery or safe recovery, and the complete PRD goal remains active.

Scope-blocker visibility checkpoint in progress: the preceding goal turn made verified implementation/backup progress (56991b5/efb41f7). This turn revalidated clean HEAD efb41f7 and found native Linear still locked; the pending DAR-20 completion update is not claimed submitted. Code work continued against PRD worker/inspection requirements. Existing ScopeWriterState deliberately observes only exact-scope writers, so it cannot reveal legacy aliases or expired reader blockers. A new optional ScopeLeases observation adds versioned live/expired reader/writer counts across the actual overlap family in the same read-only approval/journal transaction; exact writer semantics and approval authority remain unchanged.

New storage-backed observations always fill the object, scan at most 1,001 rows to reject totals above 1,000, validate bounded metadata and return no partial status on failure. Multiple distinct legacy writers are counted; duplicate same-scope writers fail. Public output contains no lease token/owner/holder task ID. Nil legacy data remains unknown, never an empty scope. Expired holders still block and all-zero snapshots are not dispatch/retry/release authority. CLI, SDK and native HTTP adapters expose the additive metadata through the existing execution-inspection surface, not a new mutation endpoint.

Remaining work is explicit: this is approval-bound aggregate visibility, not a global holder listing, proof of death, automatic lease release or full orphan recovery. Native Linear completion/comment posting still requires the operator to unlock the Mac. No live inference, user configuration/content writes or user-database migration was requested or performed for this checkpoint. Final verification and GitHub backup follow after cross-layer tests.

Scope visibility verification: make check passed (format/LOC, vet, full native race suite and production build), including app98.192s, telemetry67.342s, gate17.520s, SDK15.505s, CLI28.442s and API8.042s. Native make build and CGO-free Linux amd64 cross-build passed. SDK/API scope tests also passed three race-enabled repetitions; direct CLI output checks confirm an explicit empty observation and unchanged database bytes. An additional storage boundary test proves exactly 1,000 holders are accepted (500 live/500 expired readers), complementing the 1,001-holder rejection and malformed-row tests; focused race checks passed after that addition. Independent review found no concrete blocker or authority expansion. A final full check includes this last boundary test before publication.

DAR-20 publication: implementation 56991b5b4725df2b63a8686fb9266c84af924e21 is pushed to origin/main, with clean worktree and matching heads verified. Native Linear had confirmed In Progress via status activity 514f655a-50c8-462f-b83c-572ff2ee8594. The attempted completion update was blocked because the Mac locked and could not be automatically unlocked; no Done status or completion comment was submitted. Operator unlock was requested. Pending native-app follow-up: mark only DAR-20 Done based on its two verified criteria and post the implementation URL, tests and cooperative/crash-recovery limitations. No other issue status or relationship was changed. The full project goal remains active.

Final DAR-20 verification: make check passed after all review fixes (format/LOC, vet, full native race suite and production build). App99.083s, telemetry65.527s, gate17.243s, SDK14.644s, CLI28.088s, tools2.941s, workers3.301s; unchanged packages cached. Native make build and CGO-free Linux amd64 cross-build passed. Three independent agents contributed implementation, storage/recovery review and actual SDK tests. Both exact issue criteria are proven within the specified trusted in-process/shared-store scope; no remaining issue-specific blocker was found. General crashed-holder recovery, stronger isolation and full PRD acceptance are not claimed complete. Source staging excludes generated binaries, databases and secrets. GitHub/Linear checkpoint identifiers will be recorded after verified publication.

DAR-20 reader/writer checkpoint in progress: revalidated native Linear acceptance (safe concurrent reads; exclusive side-effect scopes) and moved Todo to In Progress. Previous CLI confirmation did not advance this issue; current worktree inspection exposed that application Allow/read-only handlers bypassed resource reader leases. Application/SDK/delegated dispatch now always installs a schema/policy/pending-identity-bound shared reader gate. Callback invocation is joined, one-use per execution, cancellation-aware and ownership-checked; no reviewer is required for allowed reads, while Ask retains exclusive approval behavior.

The safety audit also identified two real exclusion gaps. Expired unreleased readers now block writers until explicit release after handler return, rather than relying only on discarded stale output. Built-in read_file/create_file share the reserved workspace scope across same/nested/aliased/unrelated roots; legacy create_* scopes remain bidirectionally conflicting for admission and trusted inspection. Generic other scopes remain exact. Approval records keep their exact scope identities; no stored records are relabeled. Older concurrent binaries are not qualified. No automatic expiry-only release was added.

Focused evidence: SDK tests use independent Clients, actual loopback HTTP and shared SQLite to prove two concurrent readers, conflicting writer denial, unrelated-scope independence, cancellation joining and eventual writer admission. Built-in application tests verify approved create is denied under a reader lease for four root layouts. Gate/storage tests cover expired readers, retained lease blockers, legacy upgrade scopes, stale-output rejection and multiple/late/forged callbacks. Full make check is being run before backup; final results will be recorded below. No live inference, user configuration/content changes, automatic learning or release publication in this checkpoint.

Outstanding DAR-20/PRD limits: general crashed-holder reconciliation needs proof of termination before safe release; availability can be blocked by unresolved leases. Separate databases, external processes and misbehaving trusted handlers remain outside cooperative exclusion. SDK hosts must canonicalize resource scopes. See docs/reader-writer-execution.md. Earlier root-hash scope and read-expiry observations below are historical and superseded by this checkpoint, not current guarantees.

Aggregate verification caught a real regression: TestCodexTaskCancellationDuringLocalDelegation failed in caller-context and durable-cancellation modes because the first reader gate erased known effect-free rejection diagnostics. The implementation was corrected rather than weakening those assertions: only an exact valid joined Failed/NoEffect result with actual caller cancellation and proven final ownership may survive as diagnostics, with Recoverable cleared and cancellation returned. Expired ownership, storage/handler errors, malformed results and post-cancel success remain rejected. The unchanged cancellation tests passed after correction; final aggregate verification follows.

Independent review confirmed both native DAR-20 criteria against actual SDK/application tests, not only the lease primitive. It found a coincident-cancellation/storage-error classification corner, now fixed with a direct regression; exact cancellation attestation also rejects joined extra errors. Legacy create_* scopes now mutually overlap each other as well as workspace, avoiding a transitive alias gap. One aggregate make check passed after the cancellation correction (app96.613s, gate16.873s, SDK14.002s); a final aggregate run includes the later review fixes before checkpoint staging. General supervisor/recovery and whole-MVP acceptance remain separate and unproven.

DAR-18 completed checkpoint: 6f6f9201e0fc448e4aa905f574b1e037801a9ef5 was pushed to origin/main after final checks, with local/remote heads matching. Native Linear status change b906acfa-be0b-43ec-8704-b0d7b5cf6973 and comment de21e6f8 were posted and read back. Both exact classification criteria were matched to implementation/tests before marking Done. Linear automatically resolved its outgoing blocking links to DAR-13/DAR-20/DAR-26; all five existing issue links remain under Related. No separate relationship edit or other issue status change was made. Completion is limited to DAR-18, not DAR-16/DAR-17 or the full PRD/MVP.

Declared-tool-behavior checkpoint: the preceding goal turn made verified progress through live qualification and GitHub backups ec56d1d/4890c55. This turn started from a clean 4890c55 checkout and re-read native Linear DAR-18 acceptance/dependencies. Runtime.ToolBehavior and registry/SDK aliases now distinguish read_only, idempotent_write and non_idempotent_write separately from observed none/confirmed/uncertain effects. Omitted registry declarations derive conservatively from ReadOnly; explicit declarations must agree with that boolean, and unknown/contradictory values reject. Registry/extension descriptions are sorted owned metadata; provider wire schemas gain no authority field.

The runtime snapshots trusted executor behavior before ToolStarted and retains it in ToolCompleted. Legacy custom executors/history remain unclassified; paired declarations must match on replay. Both writer classes still require exact per-call approval, active single-writer lease and one-use dispatch. Approval.Request stores optional validated tool_behavior and the authorization policy digest binds policy+class in a versioned envelope. Request/decision/consumption/execution inspection reject class omission or drift against typed evidence. Existing absent version-1 fields remain readable; no record rewriting or SQL column migration. Mixed old/new binary execution against typed histories is not qualified.

Review caught and fixed propagation defects before backup: interrupted-delegation recovery omitted the pending class; audit loading discarded it before reviewer projection; execution-status inspection lacked journal/class correlation. New regressions exercise declared and legacy recovery, tampered pair rejection, production AuditTask with real HTTP reviewer and retained class (including delegation), and corrupted approval classes at open/completed inspection. No model-authored text sets these declarations or gains tool authority from audit metadata.

Real public SDK→loopback provider→approval→handler tests exercise both write classes, inspect consumed approval and live writer ownership inside one actual controlled file effect, and verify durable class afterward. Uncertain effects and confirmed effects followed by provider failure do not trigger automatic retry/fallback. Runtime tests preserve observed effects independently, reject declaration panics/invalid values before dispatch and prohibit readonly/confirmed success. Built-in reads/delegation explicitly declare readonly; create_file conservatively declares non-idempotent write, with terminal preview checking/displaying that class. No live inference, user configuration/content change or background learning occurred in this checkpoint.

Final aggregate verification passed after all recovery/audit/inspection fixes and tests landed: make check (format/LOC, vet, full native race suite and production build); final app race94.416s and sessions3.171s, other packages cached from the preceding full run (including telemetry68.031s, SDK17.323s, toolgate17.665s, CLI28.396s, runtime8.927s). make build and CGO-free Linux amd64 production cross-build passed; cross-build is not native platform qualification. Independent scope-specific acceptance audit found both DAR-18 criteria satisfied: declared read-only/idempotent behavior and observed none/confirmed/uncertain effects, including exact binding and no retry expansion. This does not close DAR-16/DAR-17 or imply full MVP completion. Origin/main matched HEAD after fetch before staging.

Remaining broader scope is unchanged: arbitrary handler idempotency is a trusted implementation contract, not proved by declarations; automatic tool retries, general editing/coordinator writes, stronger isolation, all crash/platform boundaries and full PRD acceptance remain separate work. See docs/tool-behavior.md for compatibility, inspection and safety boundaries.

Live qualification backup: ec56d1de1a750a5e4c9a825ba83b99be5a8f2fa5 was pushed to origin/main with local/remote heads matching. Native Linear DAR-18 comment8532c8fb was posted and read back with both live results, controlled-worker limits, full-check evidence and the missing idempotency-classification requirement. Status remains In Progress and dependencies were preserved.

Live failure-repair qualification: the previous goal turn made verified progress (521fb57 production checkpoint, 2c17b7b tracking backup); this turn started from that clean matching repository. Installed codex-cli 0.153.4 still reports Logged in using ChatGPT. Official OpenAI App Server documentation was rechecked before supervised tests. No API key or credential-file extraction was used.

The new opt-in TestLiveCodexRecoverableToolProtocol passed against gpt-5.6-sol in6.68s (race-enabled package8.093s). Actual runtime+temporary SQLite committed one tool_failed/NoEffect completion before its one success:false RPC; actual Codex emitted status failed/success false, Sol completed the second model turn and replay retained exactly one failed tool message. Diagnostics are metadata-only. This confirms the pinned failure tuple without changing production validation to accept a guessed alternative.

The new opt-in TestLiveCodexDelegationRepair passed in14.92s (race-enabled app package16.372s). One actual checked Sol coordinator made two sequential delegate calls through the application Service to a controlled loopback Ollama-protocol fixture. First invalid Go failed requested go_source validation; the second distinct work attempt returned exact corrected source and passed supervisor acceptance. Parent completed in3turns with80events; both child execution histories had7events. All five task records had correct lineage/terminal states, no pending/uncertain effects, no active worker leases, and the coordinator temporary directory was removed. This is real Sol inference with synthetic local-worker responses, not a live local LLM or compiled/executed Go program. Session closure is verified; general descendant containment is not implied.

Reproduction: make qualify-codex-repair runs both explicitly opted-in signed-in cloud tests; normal make check skips live inference. Each has a90second context and bounded turns/calls, not a backend spending cap. Temporary test DB/CWD are owned fixtures; no user configuration, content directory, real Ollama model or background learner was changed. Codex itself may refresh login or update its own state. Details and exact commands are in docs/recoverable-tool-failures.md.

DAR-18 acceptance audit still finds a concrete missing requirement: tools.Definition and SDK expose ReadOnly but no declared idempotent-write behavior; runtime NoEffect/ConfirmedEffect/UncertainEffect classify observed outcomes, not safe repeatability. Submission idempotency does not fill this tool-contract gap. Next focused implementation must add validated read-only/idempotent-write/non-idempotent-write declarations with conservative compatibility defaults and approval/lease-bound inspection, without authorizing automatic retry of confirmed or uncertain effects. Keep DAR-18 In Progress. Full PRD acceptance remains open.

Checkpoint verification: make check passed (format/LOC, vet, full native race suite and production build; app93.793s, Codex bridge2.070s, unchanged packages cached). make build and CGO-free Linux amd64 production cross-build passed. make -n qualify-codex-repair resolved both intended opt-in commands; those commands were run individually for the live evidence above, not repeated through the aggregate target. Independent review found no concrete blocker in either qualifier and confirmed their limited scope. Origin/main matched HEAD after fetch before checkpoint staging.

Recoverable-tool checkpoint backup: production/tests/docs commit 521fb5764fbd288cfbda46a075ee2daddc74f838 was pushed to origin/main after full checks; local and remote heads matched. Native Linear DAR-18 comment06817b50 was posted and read back with the commit, verification evidence and remaining limits. DAR-18 remains In Progress and its dependencies are unchanged. No release or full-MVP completion is claimed.

Latest recoverable-tool-failure checkpoint: trusted handlers can explicitly permit a fresh model turn only with Failed + Recoverable + NoEffect and no Go error. The loop persists tool_failed before delivering its paired ToolFailed provider message; cancellation, budgets, policy and approvals remain unchanged. This is model-directed repair, not automatic tool replay. Built-in reads and known safe delegation rejections use the flag. Batch aggregation preserves safe successful siblings but marks any failure; child journal uncertainty, dispatched pending tools, runner panics and invalid outcomes cannot become recoverable. Create-file failures remain terminal.

Failure status now survives replay, compaction, serialized history and coordinator audit evidence. HTTP/Ollama adapters project an explicit failure prefix without an unsupported wire field and account for its context bytes. Native Codex replies success:false with original content and binds the resulting completion to the pending identity/status; imported historical outputs use the prefix. Official App Server documentation and generated protocol schema informed this experimental path. No new live Sol failure-response exchange is claimed. See [recoverable tool failures](recoverable-tool-failures.md).

Focused verification passed for runtime/tools, providers, Codex bridge, app delegation and real read→repair→feedback→restart→workflow grouping. An actual runtime+SQLite+scripted Codex test proves tool_failed commits before success:false; injected completion persistence failure prevents the RPC response. Two repaired tasks with positive feedback remain excluded from procedural learning while two clean control tasks qualify; the audit projection retains the failed step. A stale grouping regression was updated because durable tool failure now changes replayed conversation digests. Review found and fixed swallowed-runner-panic uncertainty and malformed-batch downgrading. Tests use owned fixtures, not user content/configuration or live model calls.

This resolves the earlier read/delegation outcome-model gap for new execution records, not arbitrary legacy histories whose error-valued content lacks a failure code. Old journals are not rewritten. General tool-aware fallback, uncertain-effect recovery, coordinator writes, stronger isolation, live failure-protocol qualification, automatic skill validation/activation and full PRD qualification remain open. Native Linear DAR-18 was inspected and remains In Progress; dependencies are preserved and no issue completion is claimed.

Aggregate verification: make check passed (format/LOC enforcement, vet, full native race suite and production build). App race97.883s, CLI28.774s, telemetry66.308s, SDK14.751s and Codex bridge2.418s passed; unchanged packages used Go's test cache. make build and CGO-free Linux amd64 production cross-build also passed. Cross-build is not native Linux runtime qualification. Remote origin/main was fetched and matched the starting HEAD before checkpoint staging.

Reviewed-file-creation backup and tracking: implementation commit fbd8f6e9adc253b75a47e9bb2641bfdfbb4c64ce was pushed to origin/main after the final full checks (app race96.149s, CLI28.016s, telemetry65.354s, SDK13.648s; unchanged packages cached), native build and Linux amd64 cross-build. Native Linear DAR-17 comment2bf73be3 was posted and read back with the commit link, per-call approval/explicit-failure evidence and open boundaries. DAR-17 remains In Progress, with existing dependencies preserved. No completed-issue or released-MVP claim was made.

Latest reviewed-file-creation checkpoint: local-model terminal chat and trusted Go SDK hosts can opt in to `create_file` through `tools.create_enabled` and an explicit existing absolute `create_root`. Defaults remain off and old default configuration fingerprints are preserved. The parent-only capability requires per-call reviewer/presenter authority; ordinary headless/daemon execution and durable submission execution cannot acquire it implicitly. Children retain only their existing read capability. The tool creates new UTF-8 files up to 64 KiB, never modifies existing targets or creates parent directories, and uses pinned root/parent descriptors, private staging, file/directory sync and atomic no-replace publication. Writer scopes derive from root device/inode identity; aliases share a scope. This remains cooperative same-database coordination, not an OS sandbox or a fence against arbitrary external writers/directory renames.

Final review identified and corrected a learning-eligibility defect before backup: returning error content with NoEffect and no Go error did not mark a procedural step failed. ToolResult now has an explicit Failed flag independent of its effect; the loop persists tool_failed and fails the task while retaining the known effect. Create-file rejection uses that path. Existing workflow grouping excludes the failed marker even if separate task feedback is positive. Generic handler errors/panics retain conservative uncertainty; this is not permission to declare unobserved effects safe. Regression tests cover runtime events, executor preservation and workflow eligibility.

At the reviewed-file-creation checkpoint, read/delegation recoverable-error semantics were still unaudited and explicit failures always stopped. The newer recoverable-tool-failure checkpoint above supersedes that limitation for new known-safe read/delegation outcomes; general editing, coordinator write handoff, recovery/inspection of write uncertainty and stronger isolation remain open.

Terminal review independently validates exact bounded argument fields/digests, rejects decoded known secrets and renders complete ASCII-quoted content with path/byte count/content digest. Only exact active `/approve ID` or `/deny ID` commands reach the trusted reviewer; model text, stale/reused IDs, EOF, cancellation, expiry and unavailable preview delivery cannot authorize a call. Both actual terminal descriptors are required for production chat approval. Approval consumption is durable under the existing writer lease before dispatch, and ambiguous publication, cleanup, cancellation, lease ownership or completion persistence remains non-retryable. Cleanup never removes the final target; interrupted staging artifacts are not silently recovered. See docs/reviewed-file-creation.md.

Verification: final aggregate make check passed (format/LOC, vet, full native race suite and production build), make build and Linux amd64 production cross-build passed. Actual loopback HTTP tests verify durable ToolStarted before review, exact approved bytes, denied/malformed/escaped/child proposals, preserved existing targets, consumed approval and released lease, and no fallback after a real file effect followed by injected ToolCompleted storage failure. Owned macOS PTY subprocesses exercise actual RunWithInput→runChat approval/denial/no-overwrite, with separate piped-input and piped-output rejection before provider/database access. Focused race checks cover concurrent no-replace publication, existing symlink/directory/FIFO identity, mode 0600, empty files, root alias/pinning, pre-cancellation, EOF-before-preview, expired/stale/reused IDs, callback snapshot isolation and public SDK configuration/creation. CLI approval/PTY tests also passed three consecutive race-enabled runs. Independent review found no concrete safety blocker within the documented scope. No live Sol/Ollama calls, user configuration changes or writes to user content directories were made; tests use owned fixtures. Linux interactive PTY execution is not qualified by the macOS subprocess tests. Native Linear DAR-17 acceptance/dependencies were inspected and its status changed from Todo to In Progress; no issue completion is claimed.

Latest interactive-streaming checkpoint: production `darwin chat` now uses the combined Service.RunLiveStream / SDK Client.RunLiveStream interface rather than waiting for Result.Text. One unbuffered channel preserves committed lifecycle/text order and bounded backpressure; provisional model lines are terminal-filtered across chunk and metadata boundaries, prefixed with `| `, limited to1MiB redacted text per task, and not duplicated at completion. Trusted status does not enter the model's filter. Only successful application return advances continuation/feedback state and prints the separate completion marker. Failed partial text remains visibly provisional and is not reused as conversation history.

Combined callbacks share an error/panic/cancellation gate; failure cancels and joins execution without recasting durable appends as storage failure. A reviewed edge was corrected: cancellation during TaskCompleted callback now returns context cancellation even if the durable task completed and trailing redactor text was withheld. Existing single-channel streaming APIs and JSON lifecycle output remain unchanged. Statefulness covers escape/control strings and Unicode fragments without retaining unbounded payloads; per-task reset prevents an unterminated escape from hiding the next task. Line quoting distinguishes explicit model-imitation status lines, but is presentation separation rather than authentication. See docs/interactive-streaming.md.

Interactive-streaming verification: full make check passed (format/LOC, vet, native race suite and production build; unchanged packages cached), make build passed and Linux amd64 production cross-build passed. Actual loopback HTTP tests prove prefix delivery before provider completion, split-secret/Unicode handling, no duplicate final answer, active steering into a second model turn, output-failure cancellation, feedback exclusion for failed attempts and next-task retention of only successful history. Owned CLI subprocess tests enter real RunWithInput→runChat with YAML and stdin/stdout pipes, verify early stdout, and distinguish clean exit1/provider cancellation from abrupt SIGPIPE. App/SDK tests cover lifecycle-before-text ordering with read-only SQLite commit evidence, both callback error/panic gates and terminal cancellation preserving the completed journal. Repeated focused display tests cover byte/UTF-8 limits, control suppression across trusted metadata, reset and status quoting. Independent final review found no remaining concrete issue. Tests used fixtures, not new live Sol/Ollama calls or user database/configuration changes; complete PRD qualification remains open.

Native Linear recovery: its previously blank issue view rendered again after a normal Quit/reopen. DAR-38 and DAR-46 acceptance criteria and existing dependencies were inspected in the authenticated DarwinRouter workspace. After GitHub backup80f3dfb, both issues were moved from Todo to In Progress and verified checkpoint comments were posted through the native app: DAR-38 comment13e42ba8 links the interactive-streaming evidence; DAR-46 comment02ebfe2b records release preparation through ea5c279 and its remaining signing/qualification gates. Both resulting comments and status-change activity were read back. Neither issue was marked Done, and dependencies were preserved. Search requires entering the issue ID and pressing Return; list-row center clicks can hit the nested project link, so the issue search result is the reliable native navigation path.

Latest release-preparation checkpoint: local packaging now builds macOS/Linux amd64/arm64 from one private snapshot of the explicitly reviewed Git commit, with sanitized CGO-free Go builds, fixed archive metadata, a canonical schema-1 manifest and checksums covering the archives plus manifest. Clean HEAD/worktree checks are operator gates; exact blob materialization, not those checks alone, prevents ignored or concurrently modified worktree content from entering builds. Snapshot bounds, regular-file-only inputs, no local module replacements, bounded subprocess capture/deadlines and atomic no-replace output publication are enforced. Installed toolchain, module cache and caller-owned directories remain trusted; this is not hermetic build provenance.

Offline `cmd/sign-release` and `cmd/verify-release` use an explicitly provisioned, separate Ed25519 identity and authenticate the exact manifest/four-target payload set. No Git SSH key is read or reused; no production release key, tag, upload or installation is created. Verification checks the signature before file hashes and rejects malformed metadata, missing/extra files, symlinks and tampering without extracting/running artifacts. Source is reviewed separately from signature trust. See docs/release-packaging.md and the explicitly unreleased docs/release-notes.md. Native Linear's app window was visually blank; its View → Reload control did not restore issue content, so no issue update is claimed.

Release-tool verification: make check passed (format/LOC, vet, full native race suite and production build; existing packages cached), with new package/signature/CLI tests and independent review. Make build and Linux amd64 production cross-build also passed. The opt-in clean-checkpoint qualification results follow. Real release signing/distribution, hosted gates, native execution on the other targets and full DAR-45/DAR-46 acceptance remain open.

First clean-checkpoint release qualification: `make qualify-release` passed against bc50e983f14a30f1e32e29d1ff24358c4e870af0 in30.25seconds on macOS ARM64. All four targets were built twice; every archive, manifest and checksum file matched byte-for-byte. Executable headers matched each target, Linux outputs had no interpreter segment, the native version command returned the test prerelease version, and a disposable-key signature verified before deliberate tampering was rejected. No artifacts/test keys were retained or uploaded. GitHub CLI has no authenticated API session, so no hosted CI result is claimed; SSH Git backup is configured separately.

Final command-level release qualification: the expanded `make qualify-release` passed against48dc612967b06f70f319ac6c508c2ac50b882722 in20.30seconds on macOS ARM64. The first artifact set used the library and the second the actual package-release command; all six unsigned files matched. Actual sign-release/verify-release commands passed with a disposable test key, produced the same signature as the library, and rejected a modified archive. Four executable format/architecture checks and the native version smoke test passed again. This proves the current local packaging workflow, not Linux/Intel macOS runtime behavior, published artifacts, production signer identity or full MVP acceptance. Final follow-up edits record evidence only; no runtime configuration, model calls or user databases were changed.

Next-sprint evidence audit corrected the README's stale blanket statement that live streaming was unfinished: Service.RunTextStream, the OpenAI-compatible SSE handler and SDK already deliver provisional incrementally redacted provider text, with a blocked-provider HTTP fixture proving delivery before completion. Interactive chat still passes only lifecycle events through chatHooks.Run and writes Result.Text after completion. A coherent next sprint is combined lifecycle/text callbacks for interactive CLI output while retaining durable task identity, steering/cancellation, redaction, no child/tool-content forwarding, bounded backpressure and explicit failure after partial output. This gap audit is not an implementation claim.

Latest background-learning checkpoint: the opt-in daemon learner now drives durable discovery, consumption, grouped selection, generation and inactive publication instead of accepting an enabled-but-unwired setting. One bounded phase runs per tick. Schema 21 adds canonical revision-CAS learner state with immutable full-configuration binding and a pending selection/bucket pin written before inference. Existing single-use generation claims and aggregate budgets remain dispatch authority; scan cursors do not grant inference or activation rights. Replayed pages/receipts and idempotent catalog publication permit restart without inventing new attempt IDs.

Background-learning boundaries: defaults stay disabled and mandatory budget/privacy/model admission is rechecked. Ordinary budget/cooldown exhaustion waits on the same pinned identity with healthy learning_budget_wait status. A started/failed generation, stale source, policy mismatch or publication/storage failure stops for attention; the learner never silently discards an uncertain claim or activates a draft. Native Codex skill generation is explicitly rejected until that auxiliary backend is implemented. The initial signed-in Sol task/audit integration is unchanged. Source scope remains a single-operator destination namespace, not cross-tenant ownership. Current-user configuration was not changed and no live model was called.

Daemon startup/shutdown now starts, cancels and joins the learner; health and readiness include its separate metadata-only observation. `darwin skills learning status --config` and Go SDK Client.SkillLearningState inspect persisted cursor/pending IDs without initialization or inference, even after disabling learning. See docs/background-learning.md for policy drift, kill-switch and recovery limitations. Native Linear still exposes only the window/menu accessibility tree; no issue update is claimed.

Learning verification: final make check passed (format/LOC, vet, full native race suite and production build), make build passed and Linux amd64 production cross-build passed. Tests exercise real accepted tool workflows → loopback model generation → exactly one inactive draft, new-Service restart, two-Service single-use claims, actual failed terminal-ledger writes retaining started/uncertain attempts, budget pauses, policy/credential guards and supervisor join. Actual CLI daemon processes advance and resume empty-workflow state across restart without provider calls. Storage tests cover canonical bounded decoding, CAS contention, immutable policy/phase transitions and transaction rollback; schema-20 migration preserves complete memory facts and retired identity tombstones and still rejects reuse after upgrade. CLI/SDK inspection tests cover scope, disabled learning and non-creation. Independent review found and corrected a misleading uncertain-claim test, credential-safe policy hashing that still binds endpoint/path changes, and normal budget-wait health semantics. Full PRD qualification, automatic skill validation/activation, stale-source resolution controls, native Codex generation and live learning qualification remain open.

Latest managed-residency checkpoint: opt-in dedicated loopback Ollama providers now support provider-confirmed low-memory switching through the same admission path for automatic, explicit, delegated and auxiliary local work. The Service holds active model references for the full operation; endpoint gates protect inspection/switching without holding the global resource mutex during HTTP. The resource budget's read-only LowMemory calculation includes live RAM and selected-device/aggregate VRAM reservations. Confirmed absence never grants memory credit: admission re-profiles hardware and applies the existing budget again.

Managed residency requires explicit provider manage_residency authority, defaults off, rejects common duplicate endpoint aliases and ambiguous configured model identities, and refuses custom factories. Unknown inventory and equal-digest aliases block switches rather than being evicted. Ollama inspection/unload uses bounded strict JSON, current API acknowledgement plus inventory absence, owned policy transport, deadlines and no transparent unload replay. Uncertain unloads block queued repeat mutations until identity and original digest are observed absent. Configuration is not a cross-process ownership lock; one Service and a dedicated server are required. Uncertainty is process-local, not durable. No user endpoint was enabled or live model unloaded.

Residency is admission-time maintenance: known credential/continuation failures are preflighted, but later context/tool/storage checks can still fail after an idle model has been unloaded; this creates no task success or fitness evidence. See docs/model-residency.md for bounds and unsupported shared-server/custom-backend cases. Native Linear currently exposes only its window/menu accessibility tree, so no issue status update is claimed.

Managed-residency verification: make check passed (format/LOC, vet, full native race suite and production build), make build passed, and Linux amd64 production cross-build passed. Real HTTP fixtures prove explicit/automatic unload confirmation and fresh pressure checks, refusal of unknown/active models, no repeated uncertain unloads, original-digest alias blocking, and no mutation on missing credentials/continuation source. Provider tests cover malformed/duplicate/case-aliased/trailing/oversized JSON, far-future resident expiry, redirects, cancellation and still-resident acknowledgements. Resource tests cover exact low-tier boundaries and live RAM/selected-device reservations. Independent review caught and resolved random-draw locking, pre-unload route metadata and outstanding-reservation accounting concerns; final review found no blocking defect within documented scope. No live physical memory reclamation, hosted CI, cross-process residency ownership or full PRD qualification is claimed.

Latest context-engine checkpoint: the public contextengine package and SDK ContextEngine option now drive task assembly, conservative estimation and compaction retention selection. Validated plans may reorder/omit whole optional memory/skill bundles, but must preserve complete history/current bundles and tool pairs. The application owns a frozen prepared assembly between automatic routing and dispatch; explicit tasks and scoped delegated workers use the same integration. Omitted memory is not touched; privacy restrictions remain conservative. Nil retains default ordering, and ContextEstimator remains a mutually exclusive alternative for measurement-only hosts.

Context-engine compaction uses a credential-scrubbed owned callback view, while canonical materialization preserves original source digest/sequence, summary and safe tool-pair boundaries. Auxiliary summary generation selects retention once before dispatch; approved drafts bypass re-planning and retain exact checkpoint comparison even after engine replacement. Context callbacks are bounded, panic-isolated, local-only trusted host code with cooperative cancellation, not sandboxed processes. Raw preflight precedes redaction, encoded objects are bounded, and structured tool-output credentials are decoded before scrubbing. See docs/context-engine.md. No dynamic engine loader, background compaction, new prompt-configuration tier, routing SLA or full PRD completion is claimed.

Context-engine verification: final make check passed (format/LOC, vet, full native race suite and production build), make build passed, Linux amd64 production cross-build passed, and make fmt now includes the new package. Pure tests cover owned bounded inputs/outputs, invalid plans/sources, UTF-8, callback failures, cancellation and canonical compaction. Application/SDK tests verify real selected context reaches routing/provider/history, optional-memory use tracking, per-turn estimator gating, scoped delegated assembly, custom retention, raw source immutability, review-before-use and no approved-artifact re-planning. Independent review found a redundant Codex scrub could change frozen context when a secret overlaps the replacement marker; the custom-engine path now avoids that second scrub, with a provider-fixture regression test. Existing Codex compaction/reviewed-summary import restrictions remain in place. No live model calls or Linear issue updates were made in this checkpoint.

Latest memory-management checkpoint: the application service, Go SDK and authenticated daemon expose configured-scope factual-memory inspect/query, create/correct and revision-checked delete. Existing SQLite storage is opened read-only for inspection or with the current-schema existing-only control path for writes. Management remains available with retrieval disabled, never dispatches inference and never changes last-use during inspection. Custom stores share the same service boundary but remain trusted host code responsible for atomic CAS and privacy/storage invariants.

Memory-management boundaries: current credentials are redacted from factual content/provenance before persistence/output; secret-bearing identities/cursors/filters are rejected rather than rewritten. API requests use strict versioned/nested JSON, bounded UTF-8 and lossless surrogate validation, authentication/origin restrictions, dedicated capacity and cooperative deadlines. Pages validate scope/order/expiry/filter and bounded encoded size. Conflicts are normalized without backend text. Mutations have no automatic retries or durable idempotency receipts: a lost acknowledgement requires inspection. Logical deletion does not erase session copies, WAL or backups. See docs/memory-management.md. No Linear issue completion or full PRD qualification is claimed.

Memory lifecycle review found a pre-existing delete/recreate ABA gap: revision 1 could be reused for a replacement fact and satisfy a stale delete. Schema 20 adds content-free scope/ID tombstones for deletion/expiry and rejects recreation of retired IDs. New facts must use new identities; tombstones persist across restart. This cannot reconstruct identities deleted before migration, and trusted custom stores must enforce equivalent lifecycle safety. Older writer processes must stop before upgrade.

Memory-management verification: final make check passed (format/LOC, vet, full native race suite and production build), make build passed, and Linux amd64 production cross-build passed. Real HTTP-to-Service-to-SQLite tests cover scoped create/read/query/correct/delete, secret redaction, conflicts, retired-ID rejection and zero provider calls. App/SDK tests cover existing-store requirements, custom-store errors/panics, malformed pages, escaped-byte limits and canceled/nil clients. Storage tests cover two-connection contention, scope isolation, stale/absent deletes, expiry, retirement rollback, reopen and payload-preserving schema-19 migration. A stale future-schema test fixture caught during verification was updated to schema 21 before the final full pass. Independent review found no remaining concrete blocker in this slice. Native Linear inspection remained on DAR-42 after attempted search navigation; no issue update was made. No live model calls or user-database migration occurred in this checkpoint.

Latest deprecation-interface checkpoint: authenticated POST /v1/models/deprecation and SDK Client.ModelDeprecation now expose the same read-only application report as the CLI. A shared versioned request validates identifiers and policy; shared report validation checks population, attribution, digest shape, count partitions/unions, exact finite failure rates, threshold/minimum-sample decisions and approval-only semantics. The daemon route is wired to the existing service, with a dedicated single inspection slot, cooperative five-second deadline, bounded strict JSON at both levels, raw UTF-8 validation, authentication/origin/query restrictions, and generic failures without partial data. The application now validates its request and redacted output consistently. Metadata consistency is not proof of underlying evidence truth.

Deprecation interface verification: full make check passed (format/LOC, vet, native race suite and production build), make build passed, and Linux amd64 production cross-build passed. Focused API tests cover invalid framing/JSON/numbers, duplicates/aliases, capacity, cancellation, backend panic/error/forged metadata and policy mismatch. Real HTTP-to-Service-to-SQLite inspection verifies no provider calls or storage mutation; SDK tests cover feedback revision without resampling, no inference during inspection, missing-store non-creation and nil/canceled/invalid clients. Independent review found no concrete blocker. Broader runtime-failure population, value thresholds, scheduled suggestions and full PRD qualification remain open. No Linear issue update or completion is claimed.

Latest daemon-lifecycle checkpoint: `darwin daemon start|status|stop --config` adds a detached macOS/Linux launch and authenticated loopback control. A fresh random instance ID binds startup readiness and stop requests; occupied endpoints are refused, socket binding precedes storage initialization, and no PID file grants signaling authority. Startup owns only its spawned process handle, with SIGTERM/two-second grace/hard-kill-and-reap on failed launch. Successful launches survive the calling CLI's exit. Status/stop never create task storage or dispatch model requests; starting the dispatcher may execute existing queued work as intended.

Daemon control exposes only instance metadata through GET /v1/daemon/status and exact-instance POST /v1/daemon/stop, under existing authentication, origin denial, query/body validation and bounded control capacity. Stop acknowledges stopping rather than proving process exit and cannot undo effects. Degraded dispatcher state retains identity so the operator can still stop it; startup waits for its own ready instance rather than treating a different listener as success. Client transport forbids non-loopback destinations, proxies, redirects and automatic POST replay, with bounded strict responses. See docs/daemon-control.md. OS service installation, automatic restart, configuration reload, shared database/resource singleton enforcement and stronger descendant containment remain open.

Daemon verification: full make check passed (format/LOC, vet, native race suite and production build), make build passed, and Linux amd64 production cross-build passed. Focused race tests passed for controller/API validation, client control, owned-child startup and actual cross-process CLI start/status/duplicate/stop/restart. Tests prove duplicate binding cannot initialize storage, successful start survives caller cancellation, failed owned children are reaped, and degraded status still allows authenticated shutdown. Review found and corrected hidden degraded identity and transient-startup handling; both have regression tests. Tests use isolated loopback fixtures/idle queues, not live provider crash qualification. No Linear update or full PRD completion is claimed.

Latest model-deprecation checkpoint: `darwin models deprecation` now exposes a bounded read-only recommendation for one configured model/domain/profile. CLI flags set the trailing evaluation window, minimum eligible samples and strict failure-fraction threshold. Current revisions are resolved coherently in one read transaction, but original evaluation times determine membership; corrections cannot inflate counts or promote an old attempt into the window. Execution, schema and authoritative quality failures are reported separately and unioned without double-counting. Successful judge-only records are excluded, and positive user feedback cannot erase a failed explicit schema measurement. Reports disclose population=evaluated_attempts, a canonical evidence digest, configured local RAM/VRAM estimates and approval_required=true; no disabling, unloading, deletion, routing mutation or inference occurs.

Deprecation boundaries: missing/corrupt storage produces an error without creation or migration; reads have candidate/row/history/byte bounds and a cooperative application deadline. Model tags with colons/slashes are accepted independently from strict event identifiers. The shared evaluation preflight keeps the original workflow key limit while allowing longer catalog tags for this report. Current secrets are redacted from returned identifiers. Resource figures are configured estimates, not observed reclaimable memory or disk savings. Separate runtime-validity events, unevaluated failures, value thresholds, population-wide scheduled suggestions and native HTTP exposure are not included; this advances PRD §9.3 without claiming it complete.

Deprecation verification: full make check passed (format/LOC, vet, native race suite and production build), make build passed, and Linux amd64 production cross-build passed. Focused race tests passed across pure evaluation, SQLite, application and CLI, covering thresholds, minimum evidence, judge/schema precedence, revisions, fractional timestamp ordering, readonly behavior, redaction, missing-store non-creation, corruption, cancellation and model tags. Independent final review found no concrete blocker. An actual read-only command against the existing Sol/local smoke database reported zero code-domain evaluations and insufficient_evidence for coordinator, rather than inferring success or weakness from missing data. No live inference was dispatched. No Linear issue update or completion is claimed.

Latest memory-retrieval checkpoint: automatic and explicit task paths now select scoped, privacy-filtered factual context by positive unique keyword overlap with the current user request, then confidence and ID. ID-cursor scanning reaches later pages under 1,024-fact, 8 MiB and cooperative three-second bounds; whole-fact packing retains max_facts/max_bytes and credential redaction. Unmatched facts are omitted rather than injecting the first IDs. Automatic routing and execution share the selected snapshot. This is a deterministic lexical baseline, not semantic retrieval or general preference understanding.

Memory-use integration: the optional memory.UseStore / SDK MemoryUseStore capability compares the complete selected fact except LastUse before updating metadata at the first admitted model-stream boundary. SQLite checks identity, revision, content, privacy, provenance, confidence, times and expiry transactionally, rejecting stale corrections, deletion and different same-ID/revision recreation. Only selected facts are touched; query/inspection and routing/context rejection remain read-only. Legacy custom stores without the capability stay read-only. Errors/panics/conflicts block dispatch. Individual touches are not a batch transaction: earlier timestamps may remain when a later check fails, so LastUse denotes attempted context use, not confirmed provider receipt or successful completion. In-flight context cannot be recalled. See docs/memory-retrieval.md.

Memory qualification: full make check passed (format/LOC, vet, native race suite and production build), make build passed, and Linux amd64 production cross-build passed. Focused race tests passed across application, SDK, memory and SQLite storage, including later-page ranking, deterministic/Unicode tokens, secret-independent matching, privacy, invalid cursors, global scan bounds, cancellation, admission denials, selected-only use, monotonic timestamps and stale/recreated facts. Additional query-boundary tests passed after the full suite was started, covering current-user-only selection and source immutability. Actual application/provider-fixture tests reject a fact deleted between discovery and inference and permit the next fresh task with current context. Independent review found no blocking issue and prompted explicit partial-touch semantics documentation. Native Linear remained on DAR-42 after team-issue navigation; no memory-issue update or completion is claimed. Semantic retrieval, background learning, large-corpus performance and complete PRD qualification remain open.

Latest recovery checkpoint: expired, configuration-matched submissions with one eligible model-only task now close as failed or canceled instead of remaining running indefinitely. This includes task-start-only, partial-stream and completed-text-turn boundaries before terminal acknowledgement. A shared writer transaction preserves the original journal prefix, appends one terminal event, updates task/submission state, fences the old claim and records an interrupted_model receipt. Partial output is not returned as a completed answer. No inference, tool, evaluation or fitness update is retried; ordinary failed/interrupted histories remain continuation-ineligible. Current tool activity, workers, retries, approvals and unsupported records fail closed. See docs/interrupted-model-recovery.md.

Recovery qualification: full make check passed (format/LOC, vet, all native race tests and production build), make build passed, and Linux amd64 production cross-build passed. Focused race tests passed for planners, storage transactions and actual SIGKILL application cases with both failure and cancellation. Tests cover raw-prefix preservation, empty result, exactly one terminal/receipt, old-token rejection, concurrent recoverers, rollback and idempotency. The existing unfinished-delegation process-crash negative still remains unresolved without redispatch. Independent final review found no concrete blocker. Tests use loopback provider fixtures and manually expired leases, not live-model crash tests, power-loss qualification or upstream billing cancellation. No Linear issue update or MVP completion is claimed.

Current PRD gap review: remaining substantive work includes automatic skill validation/activation and learning-attention resolution, physical/shared-server and non-Ollama model residency qualification, end-to-end routing overhead qualification, configuration reload and signed release packaging. Opt-in background drafting, dedicated Ollama residency control, replaceable context planning, daemon lifecycle control, task-relevant lexical memory retrieval/last-use updates and evaluated-attempt model-deprecation reports now have separate checkpoints above; these do not establish automatic context management, measured prompt-cache reuse, full lifecycle supervision, semantic retrieval or a unified runtime-failure deprecation population. The initial signed-in Sol/local-worker round trip has been qualified separately. No background-learning scheduler was enabled on user data, and a full requirement-by-requirement PRD qualification remains open.

This is a chronological implementation record, not a claim that the MVP or any hosted gate is complete. The initial table and paragraphs describe the first checkpoint; later entries supersede their implementation and remaining-work statements.

| Linear issue | Initial checkpoint implementation | Remaining at that checkpoint |
| --- | --- | --- |
| DAR-5 | Go module, CLI seed, size/format gates, vet/race tests, CI workflow | Hosted CI after remote publication |
| DAR-6 | Versioned typed YAML, strict fields, layered scalar overrides, validation, redacted CLI display | Provider admission and runtime integration; secret-store adapters deferred |
| DAR-7 | SQLite/WAL, FULL synchronization, serialized migration, version and integrity checks | Wider crash-injection and migration qualification |
| DAR-8 | Versioned events for tasks, turns, streams, tools, workers, routes, evaluation, errors | Richer payload contracts as provider/tool interfaces are implemented |
| DAR-9 | Atomic event plus task projection, expected-sequence writes, exact retry deduplication, replay pages | Process-kill crash qualification and runtime integration |
| DAR-10 | Provider-neutral messages, tools, streaming chunks, usage, cancellation and typed failures | SDK versioning and richer model metadata |
| DAR-11 | OpenAI-compatible SSE adapter, authentication, redirect denial, error classification, bounded parsing | Live-provider qualification, configurable timeout integration; native Linear status In Progress |
| DAR-12 | Ollama NDJSON adapter, model listing, tool-result name mapping, unique call IDs | Endpoint discovery, hardware metadata and health integration |
| DAR-13 | Bounded provider/tool loop with durable turn content, usage, tool intent/results, cancellation terminal records, output budget and default tool denial | Production schema/policy executor, application admission, redaction and CLI integration |
| DAR-15 | Local HTTP fixtures for streams, fragmented calls, malformed/truncated output, cancellation, discovery and credential-safe errors | Full cross-provider runtime handoff and context-overflow suite |
| DAR-16 | Immutable tool registration, copied catalog, compiled JSON Schema, duplicate-key/depth/size checks, normalized handler errors | Application wiring and richer tool-result schemas |
| DAR-17 | Exact tool/resource allow/deny/ask rules, inherited denials and approval requirements | Durable approval events, operator UI, policy snapshots and redaction integration |
| DAR-18 | Read-only admission and conservative uncertain-effect normalization on errors/panics | Idempotent classification and durable side-effect lease integration |
| DAR-19 | Persisted holder identity, expiry, renewal and restart inspection for resource leases | Bounded worker scheduling, heartbeats, stall detection, orphan reconciliation and lifecycle events |
| DAR-20 | SQLite v3 shared-reader/exclusive-writer acquisition, owner tokens, no expired-writer takeover | Canonical scope mapping, executor integration, approvals and cancellation/lease-loss supervision |
| DAR-23 | Hard filters for mode, privacy, capability, context, health, capacity, cost budget and policy | Resource reservations, configuration/model admission and runtime integration |
| DAR-24 | Domain/profile-specific normalized ranking, neutral sparse-data priors, recency decay, confidence, deterministic explanations | Durable evidence updates, configuration adapter and production calibration |
| DAR-25 | Injected uniform bounded exploration among eligible candidates, stable tie-breaking | Production random source and exploration telemetry |
| DAR-26 | Ordered eligible fallback list preferring different known failure domains | Runtime fallback dispatch and side-effect-aware recovery |
| DAR-28 | Trusted evidence records and conservative deterministic-check resolution; attempt attribution and completion checks | Actual schema/test validator producers and artifact provenance verification |
| DAR-29 | Explicit evidence-source precedence with optional judge eligibility; same-tier failure wins | Authenticated user feedback endpoints, judge execution/cost tracking and supersession |
| DAR-30 | SQLite v2 immutable evaluation records plus transactional fitness aggregates; idempotent records and one sample per attempt | Time-window/decayed aggregation, projection rebuild, revision protocol and automatic runtime evaluation wiring |
| DAR-14 | Validated paginated replay reconstructs completed conversation turns, pending tool dispatches and interrupted turns | Actual resume/retry transitions, branches, steering and follow-up queues |
| DAR-31 | Safe suffix compaction expands cuts to preserve complete parallel tool batches; structured summary contract | Summary generation/validation, stable prompt tiers, persisted compaction records and context budgets |
| DAR-41 | Owned allowlisted HTTP transport; local-only literal loopback/localhost pinning, no DNS/proxy, denied Host override; TLS required for non-loopback endpoints | Wire every application/provider/telemetry client through admission policy; full daemon egress qualification |
| DAR-38 | Headless explicit-model CLI over application admission, provider transport and durable runtime; stdin prompt, cancellation and completed answer output | Automatic routing, steering, JSON streaming, session resume and config-path discovery |
| DAR-42 | Known configured credential redaction at application persistence/output boundaries; delta text suppressed to prevent split-secret persistence | Configurable sensitive fields, audit exports, adversarial transformations and all future surfaces |

Local `make check` passes formatting, source-size checks, vet, race tests, and builds. Storage tests cover close/reopen, lost-acknowledgement retry, conflicting identity reuse, terminal-state rejection, cursor pagination, concurrent connections, canceled writes, and rejecting a future schema. No live LLM calls or hosted CI runs have occurred.

SQLite stores session content locally. This store is not an export/redaction boundary, nor a sandbox for hostile filesystem paths. Runtime admission, privacy enforcement, tool execution, and full event payload conformance still require their backlog sprints.

Provider adapters require an explicit HTTP transport and refuse redirects. This is an integration seam for network policy, not local-only egress enforcement by itself. Provider-generated tool calls remain proposals and are released only after the protocol completion marker; no tool executor is connected. Tests use loopback fixture servers, not paid or live model endpoints. The native Linear app was used to post the DAR-11 implementation checkpoint because the connector is authenticated to a different workspace.

The runtime loop is now exercised against SQLite with a fixture executor. Reopening the database verifies ordered task/turn/tool records and paired tool context. Failure tests cover incomplete streams, missing executor, uncertain effects, exhausted turn budgets, cancellation, failed intent persistence and duplicate task IDs. The loop never automatically retries a task or tool; persistence errors require inspection rather than guessing whether dispatch happened. Task completion is explicitly separate from evaluation acceptance. Restart/resume, provider panic isolation and production executor permissions remain unfinished.

The tool executor admits registered read-only handlers after JSON Schema validation and policy checks. `ask` and all side-effecting tools are currently denied, not silently approved. Tests prove invalid, duplicate-key, denied, canceled and unknown calls never dispatch; handler failures and panics do not expose raw errors. Schema compilation uses a deny-all external resource loader. The schema library is pinned to `github.com/santhosh-tekuri/jsonschema/v6` v6.0.3; API details were checked against its published package documentation. Durable approvals and single-writer leases must land before enabling writes.

Routing tests cover each hard exclusion before exploration, stable tie ordering, task-domain evidence isolation, half-life confidence decay, cold-start exploration boundaries and failure-domain fallback preference. Ranking normalizes cost and latency against fixed policy scales, avoiding score changes caused solely by adding an unrelated candidate. Resource capacity and estimated costs are input snapshots, not live enforcement or reservations. Explanation structures have no prompt, credentials, endpoint or generated-content fields.

Network-policy tests use local HTTP fixtures and assert destination filtering, pinned localhost, ignored proxy environment variables, rejected Host overrides and provider redirect rejection. Local-only currently supports loopback inference only; LAN inference needs a separate explicit policy rather than being treated as local by name. This is an owned HTTP transport boundary, not a process sandbox: arbitrary Go extensions or a local inference server can still create their own network traffic. Runtime application wiring and exporter enforcement are required before claiming end-to-end local-only compliance.

`app.RunExplicit` connects configuration admission, provider selection, credential lookup, policy transport and durable runtime. End-to-end loopback fixture tests verify completed output, split-credential redaction, reopened task history and denial of cloud tasks in local-only mode. They also caught and fixed nil JSONSchema becoming literal `null` during runtime request cloning. This application path denies telemetry export rather than silently ignoring it. Actual model health/capacity admission and adaptive routing remain separate integration work.

SQLite schema version 2 adds immutable evaluation records and fitness projections. Tests verify precedence, rejection of unsupported evidence, attribution to a completed persisted attempt, duplicate record/attempt protection, reopening aggregates and transaction rollback when a trigger aborts the fitness update. Metrics are currently lifetime per-key means; router recency shrinkage is not a substitute for per-observation decay. Evaluation callers are trusted adapters, not model messages; authenticated producers and real validation still need integration. No automatic success credit is given by the current CLI.

Session replay tests verify page boundaries, ordered identities, tool/result pairing, ignored partial model text, unfinished dispatch uncertainty and corruption rejection. Context suffix selection retains whole parallel tool batches and refuses incomplete histories. Replay is read-only: it does not resume an unfinished attempt or retry an effect. Compaction summaries are supplied by a caller and not yet generated, validated or persisted; stable system/project prompt material must be managed separately before application integration.

SQLite schema version 3 adds durable resource leases. Tests prove concurrent database connections elect one writer, shared readers exclude writers, owner mismatches fail, renewal cannot revive expired leases, and expired writers remain blocking across restart. Explicit release asserts the holder has actually stopped; cancellation or expiry alone is not sufficient. Exact resource-scope IDs and holder tokens are trusted internal inputs, not model-controlled paths or public API data. Worker bounds and automatic supervision remain unimplemented, and write tools remain denied.

A bounded in-process supervisor now connects durable task/worker events, read leases, periodic heartbeats, cancellation and explicit output validation. Tests verify acceptance-event ordering, rejected output/panic handling, heartbeat-loss cancellation and keeping a slot occupied until a canceled callback actually returns. This advances DAR-19/DAR-21, but application delegation, inherited context/policy admission, orphan recovery and stall detection remain unfinished. A callback that ignores cancellation can still stall indefinitely; the supervisor does not falsely free its slot. Lease-release errors currently leave a read lease to expire and need operational diagnostics. Durable journal redaction must wrap this supervisor before it is exposed to users.

DAR-22/DAR-27: host profiling now reads Darwin memory statistics/unified-memory identification/swap and Linux host meminfo, while leaving GPU/thermal metrics unknown. The `darwin resources` CLI was exercised on the host. A shared reservation budget checks snapshot freshness, configured percentages, thermal pressure when available, GPU-data availability, unified-memory accounting and concurrency. Race tests verify atomic admission and idempotent release. Cgroup limits, GPU vendor adapters, thermal sensors, model-memory estimates, queue/offload decisions and application reservation integration remain unfinished.

The user supplied the GitHub destination ArronJablonowski/DarwinRouter and requested regular backups. Project workflow instructions now require verified checkpoint commits/pushes, without private keys, databases or generated artifacts. SSH access was verified; hosted CI is not yet verified.

Task inspection now connects the CLI to validated session replay through SQLite `mode=ro`. Integration tests verify completed conversation reconstruction, missing-file non-creation, output failure handling and database write rejection. The unauthenticated GitHub Actions API returned 404 for the supplied repository, so hosted CI remains unverified even though SSH push succeeds.

Provider/runtime handoff now validates complete conversation tool batches before network dispatch. Tests reject orphan results, duplicate call IDs, tool calls attached to user messages and unanswered proposals followed by user input. Replay additionally verifies tool-attempt identity, rejects reused turn/attempt identities and validates initial conversation history. This closes an attribution gap before implementing resume; it does not itself implement resume.

Completed-task continuation is now available through `run --continue-task`. It reconstructs history, appends the new prompt, creates a distinct task, preserves the session ID and records parent/privacy metadata. Fixture integration tests verify source immutability, parent/session attribution, complete context handoff, rejection of unfinished/missing histories and blocking local-history cloud transfer. This advances session/CLI integration without claiming interrupted-task recovery, mutable branch navigation, automatic compaction or token-aware context admission.

DAR-36/DAR-40: foreground `darwin serve` now exposes authenticated loopback health, synchronous task submission and task inspection through the same application service. Tests cover authorization, browser-origin rejection, duplicate/unknown/oversized JSON, bounded concurrency, cancellation, safe error/panic handling, listener cleanup and an actual HTTP-to-provider-fixture-to-SQLite path. API credentials participate in application redaction. Async durable admission, SSE, idempotency keys, separate cancellation, service installation, restart supervision and full health aggregation remain unfinished.

Parallel implementation checkpoint (user-authorized sub-agents):

- DAR-32: SQLite v4 factual memory with provenance, confidence, privacy, scoped bounded retrieval/export, optimistic correction, last-use, expiry and deletion. Race tests cover restart, scope/privacy isolation, concurrent correction, expiry and migration. Runtime/API/CLI admission and redaction, semantic retrieval, operator authorization and physical erasure from WAL/backups remain unfinished.
- DAR-33/DAR-35: local procedural skill store with immutable version files, digest checks, metadata-only discovery, deterministic validation-gated activation, CAS, rollback and automatic-mutation kill switch. Tests cover path/symlink safeguards, private permissions, corruption, rejected activation and restart. Runtime/API/CLI wiring, automatic drafting/regression detection, deletion/export and durable telemetry integration remain unfinished. The host must supply trusted validators and redact skill content.
- DAR-15: additional provider fixtures reject malformed discovery, unsupported tool kinds, refusal-as-success, duplicate choices/usage, invalid UTF-8 and oversized tool batches. Discovery preserves cancellation. Real provider qualification and richer model metadata remain unfinished.

Native Linear was readable during this checkpoint, but input actions repeatedly failed with a window-availability error; no issue-status updates are claimed for this round.

Second parallel checkpoint:

- Automatic application routing is wired to CLI `--model auto` and the shared daemon service. Eligible routes require configured context, cost and local RAM estimates; health discovery, SQLite domain fitness, exploration, shared local reservations and durable route explanations precede inference. Default request cost ceiling is zero. Execution fallback, live health caching, calibrated context/memory estimates and overhead qualification remain unfinished. Explicit model selection remains an operator override of automatic capacity ranking.
- `/v1/chat/completions` accepts configured model aliases, text system/user/assistant messages and optional `stream`. Other OpenAI parameters are rejected. Streaming is explicitly buffered (`X-Darwin-Stream-Mode: buffered`), not live; usage is included only when reported. Native task APIs remain synchronous. Shared authentication errors currently retain the native error format.
- Operator CLI memory list/show/put/delete provides scoped JSON inspection and optimistic revisions. Skill list/show/history/draft/rollback exposes private local stores; inspection does not create stores or lock files. Skill activation still requires trusted programmatic validation. Runtime memory/skill injection, automatic drafting and regression detection remain unfinished. Memory deletion is logical, not secure erasure from backups/WAL.
- Verified `make check`: formatting, source limits, vet, all race-enabled tests and build passed. Tests use local fixtures, not live model qualification. Hosted CI and Linear issue-status synchronization remain unverified.

Routing-control integration checkpoint: native task JSON and CLI expose domain/profile, capabilities, context requirement, estimated cost ceiling and local-only request classification. Tests cover typed forwarding, malformed/duplicate fields, numeric bounds, application admission before storage, explicit capability/context/positive-cost constraints and no-route admission error classification. Explicit zero cost remains a documented legacy override; automatic zero cost is a strict ceiling. Shared chat authentication/origin/query/panic errors now use nested compatible envelopes. README reflects the actual implemented surfaces and their limitations. Runtime tool wiring, automatic evaluation producers, live streaming and execution fallback remain required work.

Read-only tool integration: opt-in tools configuration binds an operator-selected absolute directory and bounded 2–32 model turns. `read_file` validates arguments, uses os.Root confinement, rejects nonregular/oversized/non-UTF-8 files and returns bounded typed results through durable tool events. Tests exercise a complete local provider-fixture/tool/provider cycle, replay, traversal/symlink escapes, unknown arguments and cloud rejection before storage. File-tool tasks remain local-only until explicit data-egress approvals exist. No write tools, approval UI, automatic tool-outcome fitness updates, per-turn context admission or worker delegation is claimed. Root directory permissions are operator responsibility; this is not an OS process sandbox.

Per-turn context admission now checks serialized messages, tool catalog and output schema plus a 1,024-token reserve before each provider dispatch. Automatic route selection uses the same estimator. Tool-enabled tasks require configured model context metadata. Tests verify initial overflow blocks inference and oversized tool results remain durable while preventing the next model call; task failure records `budget_exhausted`. This is a conservative byte-based estimate, not a tokenizer or generated-output guarantee. Automatic compaction and recovery from budget exhaustion remain unfinished; explicit non-tool legacy models with unknown context remain unbounded by this check.

Operator feedback integration: `darwin feedback` validates completed durable history and derives final-attempt model/provider/domain attribution and measured turn latency. The operator supplies accepted/rejected and observed attempt cost. Immutable feedback plus fitness are written transactionally through the existing evidence store; deterministic IDs make identical retries idempotent, while changed feedback/cost or an existing evaluation conflicts. Completion time anchors recency; preceding turns are not rated. Tests prove negative feedback attribution, no sample inflation, conflict rejection, and missing-database non-creation. Explicit tasks now persist domain/profile metadata; legacy records default to general/default. HTTP feedback, revisions, automatic deterministic validators and schema-outcome producers remain unfinished.

Authenticated HTTP feedback is now wired to the same application adapter through `POST /v1/feedback`. It enforces a strict three-field body, 4 KiB limit, shared concurrency, cancellation propagation and safe status/error mapping. Real HTTP/provider-fixture/SQLite integration verifies completion followed by idempotent feedback and conflict handling without fitness inflation. Additional application tests reject unfinished/failed/canceled histories and verify explicit domain/profile attribution. Feedback revisions and automatic validators remain unfinished.

User clarification incorporated into PRD §9.1.1: orchestrator audits should evaluate output/results, objective checks should catch empty answers and verifiable coding failures, and user feedback should dominate subjective creative quality. Required implementation includes bounded structured auxiliary reviews, separate validity/quality dimensions, abstention, provenance and compensating evaluation revisions without double-counting. These requirements are specified, not yet implemented; the current single-record-per-attempt store rejects revisions and cannot yet satisfy subjective user override of a prior judge record.

First audit implementation checkpoint: application tasks require nonblank final text. Empty/whitespace-only final answers produce a durable negative `deterministic.nonempty_text.v1` evaluation event and `empty_output` task failure; tool-only intermediate turns remain valid. Tests verify provider-fixture failures cannot receive successful completion feedback. This detects absence of text, not semantic usefulness, and the negative event is not yet folded into aggregate fitness. A bounded strict orchestrator-audit parser pins evaluator/rubric/domain, validates confidence/verdict/findings against supplied evidence references, and preserves abstention as non-success. Actual auxiliary review calls, quality/validity dimensional scoring and compensating user overrides remain unfinished.

Auxiliary reviewer component: `evaluation.Reviewer.Review` performs one provider call with a fixed rubric, JSON-wrapped untrusted task/output/evidence, no tools, a maximum one-minute deadline, context admission and an operator-estimated cost ceiling. It returns a validated advisory audit and reported usage only. Tests cover abstention, malformed/oversized/truncated output, invented refs, tool proposals, provider errors, cancellation, duplicate stream markers/usage, cost/context rejection and no retries. The host must supply a policy-bound provider and enforce privacy/model admission; this component is not yet wired to automatic daemon execution, durable audit persistence or fitness revisions. Cost estimates are not billing guarantees, and prompt separation does not prove immunity to semantic prompt injection.

Durable audit storage checkpoint: schema 5 stores immutable advisory AuditRecords separately from fitness, including task/attempt attribution, evaluator model/provider, rubric/findings/references, duration, timestamp and optional reported usage. Writes require a persisted completed model turn in a completed or failed task; exact retries are idempotent and identity changes conflict. Bounded read APIs validate records and paginate by task/ID. `darwin audits list|show` uses read-only storage without initializing missing databases. Tests cover record validation, migration, restart, immutable writes and inspection. Automatic audit production, credential/content redaction at the producer boundary, dollar-cost pricing and evaluation supersession remain unfinished.

Audit production is now integrated through Service.AuditTask and `darwin audit`, plus opt-in synchronous post-completion auditing via evaluation.auto_review_model/auto_review_max_cost. It reconstructs durable final-attempt attribution and history, blocks same-model self-review and local-history cloud transfer, enforces deployment mode and model cost/context metadata, reserves local capacity, uses an owned provider transport, redacts configured credentials, and persists validated findings independently of fitness. Judge=false disables review. Tests exercise real HTTP reviewer fixtures, durable redacted output, admission denial, automatic abstaining reviews, failure isolation and kill switch. Native task responses/CLI report audit ID/status. Automatic reviews currently cover successful tasks; manual review can cover failed tasks with completed output. Failed audit attempts lack durable lifecycle records; broad sensitive-field redaction, cost pricing, asynchronous scheduling, quality/validity dimensions and compensating user overrides remain unfinished.

Subjective evaluation revisions: schema6 preserves original evaluations, adds immutable revision chains and a compare-and-swap current head, and adjusts only the quality aggregate in the same transaction. Validation allows user-over-judge or user-over-user corrections while preserving task/attempt identity, domain, metrics, schema result and observation time; objective evidence cannot be replaced. `feedback show` exposes the bounded chain and `feedback revise --expected` records an explicit correction. Tests cover policy denial, history/restart, migration, stale/concurrent updates, rollback and idempotent retries without sample inflation. Actual advisory audit-to-fitness weighting, separate validity/quality dimensions and HTTP revision interfaces remain unfinished.

HTTP feedback history and revisions are now wired to the same application rules. GET /v1/feedback/{task_id} returns bounded original/revision history; POST /v1/feedback/revisions requires task_id, expected_id and outcome with strict JSON and a 4 KiB bound. Both use shared authenticated daemon admission. Real HTTP integration exercises completion, initial feedback, history retrieval and duplicate correction, verifying quality changes with one sample. Unit tests reject malformed/unauthenticated submissions, enforce capacity and verify 409/422 errors. Automatic audit weighting and separate validity/quality dimensions remain unfinished.

Advisory audit weighting now participates in automatic route quality only, without inserting evaluations or fabricating execution samples, latency, cost or reliability. SQLite reads at most the latest100 matching audits, one newest review per attempt; abstention/zero confidence provides no signal, and any direct evaluation excludes that attempt's audit. Candidate model/provider and execution profile come from durable events. Ranking exposes advisory sample count/influence, discounts sparse/stale/uncertain reviews, and reduces influence as direct evidence confidence grows. Caps are 0.10 for creative/unknown domains and 0.25 for code/coding/debugging/math/structured_json; these multiply only the quality deviation from neutral, not the whole route score. Judge=false disables the signal. Tests verify SQL filtering, deduplication, user-evidence exclusion, score bounds, and an actual automatic-route change with no execution sample inflation. Configurable domain rubrics/caps, per-observation temporal decay, overhead benchmarks and separate validity/quality dimensions remain unfinished.

Bounded execution fallback: runtime reports retry eligibility only for an explicitly retryable, nonpartial first-provider-turn failure with no text or tool proposal and durable terminal persistence. Automatic service execution re-admits one preselected alternative, subtracts the first model's operator cost estimate, preserves local privacy, and records a distinct task with retry_of_task_id. Source failures remain immutable; previous IDs are exposed in CLI/native responses. Tests cover successful HTTP503 fallback, partial/empty/explicit-request rejection, cost exhaustion, provider eligibility and persistence failure. No tool effects are retried. This is a bounded initial path, not validation-driven fallback, interrupted-task recovery, complete retry accounting or cross-provider live qualification; final usage is not aggregate attempt cost.

Discovery caching: shared application services cache successful provider model discovery for five seconds using provider/endpoint/credential/privacy-specific hashed keys. Concurrent misses coalesce; cancellation, errors and panics are not cached. Execution failure clears the cache, including generation protection against stale in-flight repopulation. Cache entries are bounded and callers receive independent name slices. Tests cover expiry, identity isolation, concurrent requests, cancellation, invalidation races and reuse across two automatic tasks. A local warm-cache-only benchmark measured 19.84 ns/op, 32 B/op and one allocation; this excludes network, SQLite, resource profiling and ranking and does not establish the PRD routing-overhead SLA. Live discovery can be stale within the five-second window; configuration/privacy/resource admission is still rechecked per request.

Review lifecycle persistence: schema7 tracks admitted review attempts independently of candidate events and fitness. The application persists started before reviewer invocation, then completed linked to the validated audit or failed with a bounded generic code. Cancellation uses a separate five-second cleanup context. CLI `audits attempts` provides read-only, bounded inspection; no prompts or raw provider errors enter lifecycle records. Tests verify start-before-dispatch, malformed review and cancellation persistence, successful audit linkage, schema migration, terminal conflicts and restart inspection. Admission denials still have no attempt record. Crashes/storage errors may leave started as indeterminate, not evidence of a live reviewer; automatic reconciliation and atomic audit-plus-terminal persistence remain unfinished. Live provider qualification and Linear status synchronization remain unverified.

Atomic review completion: application execution now commits the validated audit and completed lifecycle in one SQLite transaction through CompleteReview. Shared transaction helpers retain standalone audit/finish APIs without nested transactions. Conflicts roll back the newly inserted audit, and exact completion retries are idempotent. Failure-injection tests abort the lifecycle update after audit insertion, verifying no orphan evidence survives; concurrent failed-versus-completed transitions across two handles must produce one consistent outcome. Automatic crash reconciliation is still unfinished. The native Linear app was re-inspected on the DarwinRouter board; no issue status changes were made.

Objective output validity now affects automatic routing separately from subjective quality. RequireText final turns persist positive as well as negative nonempty checks, excluding tool-only intermediate turns. SQLite reconstructs up to100 checked terminal attempts per execution key, validating check values against completed text, attribution and terminal status. Ranking exposes validity sample/failure counts and a recency/sample-discounted penalty on the quality component; a pass adds no quality bonus and neither result fabricates execution samples, latency or cost. Judge=false does not disable objective checks. Integration tests show a blank model loses the next same-domain route while another domain is unchanged. Legacy missing checks imply no result; older negative-only history can contribute failures without inventing historical passes. This is an initial validity dimension, not compiler/test verification, semantic usefulness detection, per-observation decay, or the full evaluator framework.

Opt-in Go source validation is wired through the runtime, application, CLI --validate and native task API validation field. The bounded standard-library parser accepts only raw complete UTF-8 source and does not compile, resolve imports or run code. Task-start events retain the requested mode; final nonempty output receives a separate deterministic.go_syntax.v1 event before completion or invalid_output failure. The host supplies its redacted validation view so persisted evidence and delivered source agree. Telemetry verifies both required checks from saved output, counts one attempt, and isolates requested validator populations. Tests cover valid/invalid/fenced source, strict API/CLI fields, redaction, objective failure, routing change and generic-task isolation. Compiler/test execution, automatic repairs, arbitrary validator registration and broader output formats remain unfinished.

DAR-44 benchmark checkpoint: reproducible fixed-corpus validity and full automatic-task fixtures now cover 100/1000 histories and two/eight-model pools. Measurements exposed a cold-model scan and a subsequent index-only planner regression. Schema8 adds an indexed model-start lookup; bounded materialization plus sparse/dense join selection retains evidence validation and exact last100 ordering. Cold-model1000 lookup fell from35.00ms to0.59ms in three-operation local samples; full loopback fixture1000 measured34.35ms for2 models and102.72ms for8. Migration/index/append tests and a dense-history window test cover correctness. See docs/benchmarks.md for commands and limitations; no production SLA or hosted CI qualification is claimed.

DAR-32 memory-context integration: operator-configured scope (empty by default), fact-count and serialized-byte budgets now connect the factual store to explicit and automatic tasks. Retrieval independently validates scope, expiry, privacy and provenance records, selects whole facts deterministically, and redacts configured credentials before assembling fixed guidance plus untrusted JSON context. One snapshot is shared by automatic context admission and execution. Local-only memory pins hybrid requests local; explicit/cloud-only cloud requests omit local-only memory, and cloud-eligible retrieval filters to shareable facts. Tests cover loader boundaries, local and cloud-designated HTTP fixtures, persistence, secret filtering, snapshot stability and deletion affecting the next fresh task. This is bounded ID-order retrieval, not semantic relevance, automatic memory creation/correction, last-use updates, or erasure of historical session copies. Existing YAML numeric coercion remains unchanged; no live-provider or Linear-status qualification is claimed.

DAR-33 skill-context integration: configured private root/scope (empty by default), locality and count/byte budgets now connect the validated skill store to task execution. Exact domain tags select metadata; only matching active version bodies are loaded read-only. Workflows requiring unavailable tools or exceeding the whole-message budget are skipped. Redacted procedures/configuration/provenance are untrusted reference data, not permission grants. Automatic admission and execution share one snapshot; local-only selected skills pin hybrid routing local. Tests cover active/draft/tag/scope filtering, required tools, corruption/read-only behavior, redaction, local/cloud-designated fixtures, and context admission. Skill root is redacted from config display. Activation still requires a trusted host validator; automatic drafting/revision, production workflow validation, semantic relevance and regression-triggered rollback remain unfinished. Historical snapshots are not erased from continuations.

DAR-6 integer configuration hardening: schema-derived pre-decode checks now reject fractional, quoted-string, boolean and overflowing values for every int/uint field, including nested model arrays. Each environment/flag layer is type-checked before the next override layer, so malformed shadowed values cannot hide. Tests cover all current integer fields, uint64 precision boundaries, file/override precedence, generic credential-safe errors, and continued support for real float fields. This closes the previously noted fractional integer coercion gap; non-integer-field YAML coercion behavior is unchanged.

DAR-31 compaction preparation: safe-suffix selection now validates the complete provider conversation, rejects malformed UTF-8, and bounds structured summary data to 128 nonblank entries per category and 64 KiB serialized total. Returned summaries, messages, tool-call slices and raw arguments are independently owned; source or checkpoint mutations cannot silently change the other. Regression tests cover malformed histories, summary limits, copy isolation and tool-batch preservation. This remains a selection library: durable compaction records, summary accuracy validation, automatic summarization and continuation integration are not implemented. A parallel audit regression test confirms that maximal advisory approval cannot restore quality credit removed by fully evidenced objective invalidity in either code or creative domains, without inventing execution samples. Reviews remain advisory; objective production checks still cover only nonempty text and optional Go syntax, not semantic usefulness or compilation/test execution.

DAR-31 continuation integration: explicit/automatic application paths, CLI --compact-keep/--compact-summary and native task compaction JSON now accept bounded operator-reviewed structured summaries for completed source tasks. Original system instructions and full recent tool batches remain; no-op, incomplete, uncertain-effect or invalid histories fail admission. Private source history stays private after compaction. Automatic ranking and execution share the same prepared continuation, including redacted summary data. Task-start persistence atomically includes the compacted messages and versioned source task/sequence/SHA-256/removed-count/summary metadata before inference; replay exposes the checkpoint after a fresh database open while preserving the source. Tests cover suffix safety, summary schemas, file/FIFO bounds, source attribution, privacy, credential redaction, real continuation under context pressure, and further continuation after compaction. This is operator-supplied completed-session compaction, not automated summarization, independent summary-accuracy validation, mid-task compaction, interrupted recovery or historical-data deletion. The native Linear app still shows DAR-31 in Todo; no status mutation was made.

Configuration isolation correction discovered by the compaction integration tests: NewService previously unmarshaled its intended JSON snapshot into a struct retaining caller-owned slice capacity, permitting later caller edits to affect live admission. It now decodes into fresh storage. A regression test verifies independent provider endpoints, model context, nested capabilities and estimated-cost pointers in both directions. No hosted CI or live-provider qualification is claimed.

Compaction checkpoint verification: make check and make build passed. A real native HTTP/application/loopback-provider/SQLite test confirms the provider receives the summary rather than removed prompt text, while a separate read-only SQL connection sees task-start compaction before provider output. Authenticated GET exposes matching checkpoint metadata and unchanged source state. An initial journal-failure regression proves no provider dispatch occurs without durable compaction persistence. These fixtures do not establish summary semantic accuracy or production provider conformance.

DAR-31 structured-summary completeness: requirements and cumulative file/tool activity now have explicit bounded categories throughout runtime metadata, CLI/native parsing, independent copying and application redaction. Replay maps each reconstructed message to its immediate source-task event sequence. Compaction adds first-retained recent-message index/event and before/after context estimates, with validation and additive compatibility for older v1 records. Estimates measure history only, not the complete next inference request; absent origins in manually constructed legacy snapshots remain explicitly unknown. Tests cover tool-boundary expansion, exact estimates, invalid/reordered origins, schema limits and credential-safe new fields.

Bounded summary-drafting component: sessions.Summarizer owns one source snapshot, preserves source-hash attribution across provider callbacks, and makes one deadline/context/cost-bounded auxiliary call without tools or retry. Strict versioned response parsing rejects duplicate/unknown keys, invalid categories, malformed/oversized/truncated output and false completion sequences. Drafts include a separate proposed checkpoint with source attribution and context estimates; no fitness update, persistence or activation occurs. Tests cover cancellation, callback-error latching, immutable provenance, malformed output, input/output bounds and proposal isolation. Host-level privacy/resource admission, redaction, durable draft attempts, summary-accuracy validation, CLI/daemon wiring and automatic or mid-task compaction are still required; the component alone is not automatic runtime compaction.

DAR-31 durable summary generation: schema9 adds inspectable started/drafted/failed summary attempts, with immutable source/keep/model/cost identity and atomic proposed-draft/terminal persistence. Storage verifies source digest/sequence and exact reconstructed checkpoint against replay; tests cover migrations, read-only/restart access, forged metadata, idempotency, conflict races and injected rollback. Service.SummarizeTask admits deployment/privacy/model context/cost/local RAM, reserves local capacity and uses a policy-owned transport. Input/output credential redaction preserves original durable source attribution; nested tool JSON preserves numeric precision and rejects redaction collisions. Started persists before the single auxiliary call; cancellation uses independent bounded terminal cleanup. Failures never affect source task fitness or activate compaction.

CLI summary generation and summaries list/show are wired to the application/store. Real application/provider and CLI/provider/SQLite fixtures verify redacted drafts, started-before-dispatch, failure/cancellation state, same-model summarization with judging disabled, unchanged source/event/fitness state and no automatic continuation. Listing supports all tasks or an explicit task with bounded ID pagination. Summary attempts are proposals only, not accuracy-validated context; explicit reviewed-summary continuation remains separate. Automatic activation, semantic validation, draft-source input-digest auditing beyond original-source provenance, crash reconciliation and native HTTP summary endpoints remain unfinished. No live-model or hosted CI qualification is claimed.

Durable-summary checkpoint verification: make check (format/LOC, vet, full race suite, all-package build) and make build passed, including schema9 migration tests and the real CLI integration fixture.

DAR-31 operator review and controlled draft use: schema10 stores immutable approved/rejected review records and a compare-and-swap current head, capped at100 reviews per draft. CLI summary-review/summary-reviews expose explicit operator decisions and read-only history; notes are bounded and redacted. run --summary-attempt and native summary_attempt_id can use only an exact matching currently approved source-bound draft, excluding simultaneous manual compaction. New secret redaction that changes a frozen draft fails admission rather than silently altering approved content. Runtime compaction records carry draft/review IDs. SQLite rechecks the current approved head and exact checkpoint inside the TaskStarted writer transaction; revocation before that transaction blocks provider dispatch and rolls back task creation. Exact retries of already-admitted event writes remain idempotent.

Review verification covers migrations/restart, approval/rejection/CAS/history, rollback,100-record bounds, checkpoint tampering, explicit/automatic continuation, source privacy, redaction, native request validation and a revocation injected between automatic snapshot loading and task start. Review itself never calls a model or changes source/fitness. This is local operator attestation, not automated semantic validation. Rejection does not cancel already-started tasks or erase summary copies in existing continuation histories; ordinary continuation of those histories remains possible. HTTP draft/review management, automatic validation/application and crash reconciliation remain unfinished.

Operator-review checkpoint: make check and make build passed, including schema10 migration and the CLI approval/rejection integration tests. No live-model or hosted CI qualification is claimed.

DAR-40 summary HTTP workflow: the daemon now wires authenticated draft generation, single-attempt inspection, bounded body-based queries, operator review creation and review-chain inspection to the same application/store paths as CLI. All summary endpoints share task slots, preserve origin/query denial and bounded cancellation, and reserve capacity before reading POST bodies. Strict 8 KiB JSON parsing validates exact fields, duplicates, types, identifiers, numeric bounds and 4 KiB notes; errors expose only generic codes and admitted draft IDs. Tests cover forwarding, malformed input, capacity-before-read, authentication, cancellation and safe error mapping. A real HTTP/provider/SQLite sequence generates and inspects a draft, approves it, continues by ID, rejects it and verifies stale-review conflicts and blocked reuse without changing the source. Automatic semantic validation/application and asynchronous/live-streaming generation remain unfinished.

Audit rubric v2 explicitly distinguishes nonblank output from meaningful completion: repeating a request, promising future work or omitting deliverables warrants an evidence-referenced advisory finding, while brevity alone does not. PRD and prompt-contract tests preserve subjective user preference and prohibit treating semantic opinions as deterministic checks. This does not establish reliable semantic detection in live models. Reviewers still lack a dedicated authoritative deterministic-check evidence feed, and automatic reviews remain opt-in after successful execution.

Sparse-feedback correction: opposing advisory influence can now move direct quality at most halfway toward neutral, never reverse its direction; exactly neutral direct evidence blocks advisory shifts. This conservative rule applies across domains and complements same-attempt audit exclusion after direct feedback. Regression tests cover a single direct observation against100 contradictory audits, both directions, creative/code/unknown domains, unchanged evidence counts, accurate route explanations and downstream objective-invalidity penalties.

Lifecycle-streaming foundation: Service.RunStream delivers exact redacted events sequentially only after journal commit. Sink errors or panics cancel execution and stop delivery without misclassifying committed events as persistence failures; independent terminal cleanup remains durable. Tests cover commit ordering, split-secret suppression, callback mutation isolation, cancellation, failed persistence and fallback order. This is an internal application callback, not yet an HTTP SSE endpoint, public SDK or resumable stream; raw token text remains suppressed.

Checkpoint verification: make check passed (format/LOC, vet, full race suite and all-package build). Live-model review quality, hosted CI and Linear status synchronization remain unverified.

DAR-40 native live lifecycle streaming: POST /v1/tasks/stream and daemon wiring now use Service.RunStream with the existing native admission schema, authentication and shared capacity. SSE runtime frames carry task/sequence IDs and exact committed redacted event JSON; a separate terminal result reports success or generic failure. Headers flush before dispatch, bad resume headers are rejected, writes/flushes are bounded to15 seconds and deadlines clear between frames so idle inference is not timed out as a slow write. Sink errors/panics stop delivery and cancel execution; failure results omit partial text. Unit tests cover request rejection, capacity-before-body, framing, error isolation and deadline reset. A real HTTP/loopback-provider/SQLite fixture proves pre-completion delivery, durable-before-observation, redaction, disconnect propagation and restart-readable cancellation. The test provider consumes its request body before waiting so its HTTP server can observe peer closure; bounded cleanup avoids hiding failed assertions behind server shutdown.

This endpoint remains connection-owned execution, not detached durable admission, stream replay, steering or an independent cancellation API. Raw model-token text remains suppressed; completed turn content is redacted. Deadline-less custom response writers are supported for adapters/tests but cannot guarantee bounded blocking; the native daemon writer supports deadlines. OpenAI-compatible SSE is still buffered. The local Linear app was inspected and still lists DAR-40 in Todo; no issue status was changed.

Native-stream checkpoint verification: make check and make build passed. A no-sleep deadline regression verifies set/flush/clear ordering for headers, events and result, including a cleared deadline during model work. Hosted CI, live model behavior and HTTP/2 production qualification are not established by these local tests.

DAR-38 headless JSON lifecycle mode: darwin run --json consumes the same configuration, constraints and prompt as text mode, then emits version1 event/result JSONL envelopes from Service.RunStream. Flags use normal parsing without stripping values; false preserves text output. Success has no appended plain answer; execution errors expose only generic codes and IDs. Short writes, errors and panics latch output failure and cancel the task without retrying partial lines. Actual stdout pipes temporarily register SIGPIPE, use a pollable duplicate with cancellation-aware15-second writes, and restore descriptor flags without closing caller output. Custom embedded writers and regular files retain their own blocking behavior. Argument/config/input errors may precede JSON output; stdin is still read in full before execution cancellation begins.

Real-binary tests observe a committed event before releasing provider output, verify redacted JSON-only completion, close stdout during inference, and leave a large stdout pipe open but undrained before sending SIGTERM. Both interrupted cases exit1 normally with durable canceled state. Runtime callback-boundary checks additionally handle cancellation after committed starts, routes, turns and evaluation records before attempting the next ordinary write; true failed/ambiguous persistence remains an inspection-required error. Interactive input, steering, resumable events and detached tasks remain unfinished.

JSON CLI checkpoint verification: make check, make build and Linux/amd64 cross-build passed. Pipe tests verify caller descriptor ownership, blocking/nonblocking flag restoration and cancellation callback synchronization; Darwin's kernel-maintained FWASWRITTEN bit is excluded from flag equality because F_SETFL cannot restore it. Linux execution, live-provider qualification and hosted CI remain unverified. No Linear issue was marked complete.

DAR-40 bounded event replay: GET /v1/tasks/{id}/events reads one SQLite transaction snapshot through a freshly usable read-only store path, with at most100 events and8 MiB of serialized event data. Metadata includes observed head/state, exact next sequence and has_more. Storage length-probes payloads before loading, validates the complete bounded head even for empty pages, and rejects missing sequences, mismatched attribution, malformed heads and unsupported cursors. SSE validates the entire page before headers, emits exact typed durable events and a metadata-only checkpoint, and shares authentication, capacity and bounded write behavior with live task streaming. Last-Event-ID accepts only the matching task and canonical sequence; replay never dispatches inference or mutates task history.

Replay tests cover restart/read-only access, byte/count bounds, first oversized records, invalid heads including caught-up pages, cursor conflicts, malformed headers, error isolation and sink faults. The real HTTP/provider/SQLite reconnect fixture stops after five events, resumes across pages without duplicates, verifies redaction and unchanged history, and confirms only the original provider call occurred. A capacity retry honors Retry-After when the previous disconnected handler has not yet released its slot. This is bounded snapshot replay, not continuous following, detached admission, steering or task reassignment. A caught-up running snapshot can acquire new events later; checkpoint state is not a reconstructed final task result.

Replay checkpoint verification: make check and make build passed. An additional active-provider integration test reads a running checkpoint, disconnects and cancels replay, then releases inference and verifies durable successful completion. No live-provider or hosted-CI qualification is claimed; DAR-40 is not marked complete.

DAR-14/DAR-40 durable task cancellation: schema11 stores one immutable task-scoped cancellation request with stable ID/time. Request and append transactions acquire the same writer ordering. A request committed before an ordinary append rejects that append with a distinct definitely-not-written cancellation signal; exact retries of previously committed events and ToolCompleted/TaskCanceled cleanup remain allowed. Completion committed first is preserved and does not create a late request. Runtime handles the definite cancellation signal with bounded durable terminal cleanup, clears retryability and does not confuse it with genuinely ambiguous persistence failures.

RunExplicit polls durable cancellation state every100ms with bounded reads, cancels blocked providers and joins the watcher before closing its store. Control-read failures cancel conservatively and return a generic control error. Application control wrappers check task existence read-only before opening a writer, so missing task/database cancellation does not create storage. The authenticated POST task/cancel and GET task/cancellation endpoints use separate two-slot control capacity, strict empty JSON request bodies, stable idempotent status and generic errors. Accepted202 is a durable request acknowledgement, not a claim of stopped execution.

Tests cover schema migration/read-only/restart access, missing resources, request idempotence, rollback, cancellation/completion writer races, exact event retry, preserved confirmed tool effects, all runtime append boundaries, control-read errors and saturated API execution/control capacity. A real HTTP/provider test uses separate application-service instances over shared SQLite: cancellation succeeds with the execution slot occupied, survives retry/reopen, stops a blocked provider and delivers durable task.canceled plus canceled stream result. Orphan/crash reconciliation, detached task submission and post-completion auxiliary-audit cancellation remain unfinished; a pending request alone does not prove a stopped process has reconciled its state.

Cancellation checkpoint verification: make check and make build passed. The real CLI subprocess fixture additionally writes a durable cancellation request from the parent process without signaling the child, then verifies child exit1, stopped provider and durable canceled state. This proves the control path crosses process boundaries through storage rather than an in-memory cancellation registry. Live-provider behavior and hosted CI remain unverified; no Linear issue is marked complete.

DAR-14/DAR-36/DAR-40 detached submission checkpoint: schema12 adds an immutable versioned request ledger, SHA-256 idempotency/configuration binding, a bounded queued population and transactional ownership claims. Same-key retries return the original submission; changed content/configuration conflicts. Claimed work has fresh-owner-only normal event appends, an opaque private token, indexed task-start attribution and bounded effect/terminal cleanup after cancellation or expiry. Generic journal appends cannot bypass submission ownership. New success requires a linked completed task plus fresh uncanceled ownership; exact terminal retries remain safe. Expired running jobs are inspectable but never automatically reclaimed.

The daemon now starts a durable polling dispatcher only after binding its listener. Detached and synchronous tasks share configured execution capacity; queued work is discovered from SQLite after restart. Submission HTTP requests have separate bounded intake slots and do not own worker cancellation. Authenticated status/cancel controls expose safe metadata without request bodies, raw keys or claim tokens. Known credential-bearing serialized requests are rejected before persistence. Active worker cancellation includes its auxiliary review context; this does not undo tools or delete completed evidence. Configuration changes intentionally leave old queued work pinned until cancellation/resubmission under a new key.

Regression coverage includes claim races, migrations/read-only compatibility, digest verification, byte/queue limits, wrong-owner and expired-lease gates, cancellation before dispatch, preserved cleanup, linked-result attribution, failed admission, shared-capacity cancellation, queued restart and one execution for repeated submissions. A real HTTP/application/provider fixture closes the submitting request, starts a fresh dispatcher, observes the same durable submission/task IDs on retry, rejects changed intent, and obtains one final result from one provider invocation. In-flight crash reconciliation, safe operator recovery, queue listing/retention, CLI submission commands and full daemon health remain unfinished. The local Linear app was inspected in the correct DarwinRouter workspace; DAR-40 remains Todo and no status was changed.

Detached checkpoint verification: make check and make build passed. The HTTP idempotency/disconnect integration also passed five consecutive race-detector runs; a queued HTTP cancellation is verified never to dispatch. Final API validation accepts both queued cancellation without an execution result and canceled/failed results carrying only safe linked attribution, rejecting partial output. Provider fixtures run over loopback; production providers, forced process-crash reconciliation and hosted CI remain unverified.

DAR-36/DAR-38/DAR-40 durable-work discovery and CLI: read-only insertion-fenced pagination lists bounded submission metadata without loading request/result/token columns. Opaque versioned cursors bind the state filter and initial maximum row ID; state and lease observations remain live between pages, not a cross-page historical snapshot. Pages are limited to100 items/1 MiB, task attribution to1000 IDs per item, and oversized/corrupt metadata is rejected before copying its contents into Go. GET /v1/submissions alone permits strict state/after/limit query parameters, uses independent control capacity and validates hook results; other routes continue rejecting queries. Missing application-store listing returns an empty first page without creating storage; CLI database commands require an existing store.

darwin submit reuses native run options, records an idempotent queued request and returns JSON without starting a daemon. darwin submissions list/show/cancel provides local inspection and explicit cancellation across independent processes. Invalid and irrelevant flags/cursors fail before storage access; known secrets and raw failure details are not echoed. Output uses SIGPIPE-safe, cancellation-aware pipe writes with deferred descriptor restoration; arbitrary blocking stdin retains existing CLI input semantics. Read-only discovery never marks an expired worker stopped or reexecutes it.

Verification includes real CLI subprocess intake/retry/list/cancel/show over one SQLite store and authenticated HTTP discovery of an expired claim after a fresh service/database open, without runtime dispatch or mutation. Filtering, insertion fences, malformed/duplicate cursors, strict query parsing, independent capacity, missing storage and oversized opaque-column isolation are covered. make check and make build passed. Automatic orphan reconciliation, safe operator reassignment, retention, interactive steering, production-provider qualification and hosted CI remain unfinished; no Linear issue is marked complete.

DAR-14/DAR-36/DAR-43 safe pre-dispatch recovery: schema13 adds an immutable recovery audit trail. A serialized writer transaction verifies matching configuration, expired ownership and absence of any bound task-start event before clearing the former token and returning work to the bounded queue. Cancellation wins; queue saturation defers requeue; after three automatic requeues a further expired undispatched claim fails with recovery_exhausted. Existing task starts, including commits with lost acknowledgement, prevent replay. Old owners cannot append a task start, renew, or finalize after replacement. No request body or idempotency identity changes.

The dispatcher scans one bounded running-submission page at startup and every5 seconds, uses storage as the final eligibility authority and joins its reconciler on shutdown. Expected fenced-owner finalization denial does not poison unrelated work; genuine storage/supervision errors remain visible on close. Recovery history is bounded to four canonical validated records with no raw ownership token and is exposed read-only through GET submission/recoveries and darwin submissions recoveries. Public status/metadata accepts the new exhausted-recovery error code. Migration/read-only and downgrade fixtures cover schema13.

Tests cover real concurrent task-start/recovery transactions, fresh leases, wrong configuration, cancellation, saturated queues, bounded attempts, stale-owner denial, audit rollback/corruption, paginated scanning and read-only history. A paused runtime is fenced before provider dispatch, and its replacement executes once. A separate process commits an expired claim and is killed by the parent; a fresh dispatcher discovers the durable no-task state, records recovery and completes exactly one provider invocation. This verifies a real process-loss boundary before execution, not recovery after partial model/tool activity. make check passed; general interrupted-task reconciliation, safe side-effect reassignment, production provider behavior and hosted CI remain unverified. No Linear issue is marked complete.

DAR-14/DAR-36/DAR-43 lost-terminal-acknowledgement recovery: expired matching-configuration submissions with fully terminal linked histories can now restore their result transactionally without any model/tool/fallback/evaluation/audit execution. Storage bounds and validates event bodies, sequence/head/identity consistency, and current single-fallback lineage (at most two tasks, 10,000 events/8 MiB per task). A pure sessions projection replays tool pairing and checks final model/provider identity and objective nonempty/optional Go-syntax evidence before releasing success. Failed/canceled states omit partial output. A pending submission cancellation overrides delivery, not the immutable task history.

Reconstruction preserves task IDs, final text, turn count and finish reason; complete nonnegative nonoverflowing usage is summed, otherwise omitted. Audit status is explicitly not_recovered; existing audit/fitness evidence is neither invented nor duplicated. Result persistence, recovery audit and old-owner fencing share one transaction. Current recovery-history capacity remains four records, covering three undispatched requeues and one terminal projection. Partial, malformed, oversized or unsupported multi-task histories remain inspection-required rather than being replayed.

Tests cover runtime-generated successful/tool/Go-validation/failure/canceled histories, missing or contradictory evidence, identity corruption, unknown/overflow usage, fallback provenance, storage rollback/cancellation races and stale-owner denial. A real process is killed after durable task completion but before submission finalization; a fresh dispatcher restores the same answer, usage and task ID with one total provider invocation and unchanged journal sequence. Normal runtime aggregation was also hardened to omit overflowing totals, matching projection semantics. make check and make build passed. Recovery after partial execution, semantic quality proof, broader worker graphs, audit reconstruction and production-provider/hosted-CI qualification remain unfinished; no Linear issue is marked complete.

DAR-36/DAR-40 operational health: a bounded public health report now distinguishes responsive daemon, read-only database availability, live supervisor state, resource observations, provider catalog discovery and configured-model presence. Readiness requires healthy core services and at least one enabled model meeting coarse resource/transport checks; unknown supplemental measurements yield degraded readiness rather than invented hardware health. Typed validators reject contradictory status/reason pairs, inconsistent readiness and oversized/duplicate checks. Catalog presence is not inference, context, quota or output-quality qualification.

GET /v1/health uses one independent diagnostic slot, five-second application/four-worker catalog probing with two-second per-provider deadlines and a six-second endpoint context. It preserves local/cloud mode restrictions, checks mixed-provider model locality independently, snapshots credentials once, aliases credential-bearing identifiers and excludes endpoints, paths, raw failures and unconfigured model names. Discovery changes no routing cache, fitness or database records. Existing /health remains lightweight with providers_checked false but now includes live supervisor health. Built-in probes honor cancellation; arbitrary in-process hooks still must cooperate.

The dispatcher tracks each worker and its reconciler independently, including start/stop, latched errors and 15-second heartbeat stalls; heartbeats continue during provider calls and lease renewal. Tests cover active blocked-provider liveness, per-worker stalls, shutdown, panic/error isolation, mode/transport isolation, bounded probe concurrency, canceled/stalled catalogs, unavailable storage, metadata redaction and capacity independence. A real HTTP/service/provider/dispatcher fixture confirms discovery-only requests and a transition from ready to unavailable after supervisor close. make check and make build passed. Full inference/hardware qualification, useful-progress stall detection, metrics export, OS service installation and hosted CI remain unfinished; no Linear issue is marked complete.

DAR-37/DAR-40 durable lifecycle metrics: the public versioned snapshot exposes six fixed identifier-free groups for task/submission/review states and stored evaluation/audit/recovery counts. SQL reads only aggregate metadata in one read transaction; unknown lifecycle states fail without copying their contents. Older schema groups are explicitly unavailable, not measured zero. Missing stores remain missing. Snapshot validation enforces canonical groups, states, availability, serialization-safe timestamps and nonnegative overflow-safe counts.

The authenticated GET /v1/metrics uses independent single-request diagnostic capacity, strict no-body/query/origin rules, generic failures and a five-second route context. The daemon wires the read-only application service; darwin metrics --db path uses the same store with strict arguments and SIGPIPE/cancellation-safe JSON output. Application and SQL contexts are capped at four and three seconds respectively. These are stored-population gauges, not success-quality judgments, process liveness, monotonically increasing counters or full record-integrity validation. Aggregate scans and read-only integrity checks scale with database size despite fixed output cardinality.

Tests cover all groups/states, legacy tables actually absent, reopened storage, missing-store no-create, cancellation, invalid state/overflow/labels, large opaque-payload isolation, API authentication/capacity/panic handling and CLI output failures. A real HTTP/application/Ollama-fixture integration executes a successful answer and an empty-output failure, queues/cancels submissions, reopens through a fresh service and verifies unchanged counts with no extra provider calls or private identifiers. OpenTelemetry export, latency/cost histograms, retention, production-scale qualification and hosted CI remain unfinished. Native Linear was inspected in the correct workspace; no issue status changed.

Metrics checkpoint verification: make check and make build passed. The snapshot validator and HTTP restart integration also passed three consecutive race-detector runs. This verifies the fixture-backed inspection surface, not production-provider behavior or complete observability qualification.

DAR-23/DAR-27 explicit-local resource admission: named-model execution now requires a positive local RAM estimate and uses the same service reservation budget as automatic routing, summaries and reviews. The one-shot RunExplicit convenience creates its own service; concurrent embedded callers must reuse Service, and independent processes still do not share reservations. Automatic routing calls the internal already-admitted executor after its existing reservation, avoiding a second reservation. Explicit admission enforces mode/input/capability/context/cost constraints before profiling; local resource denials happen before task storage and provider execution. Explicit cloud requests skip profiling. The shared explicit/automatic profiler boundary catches panics and uses a three-second cooperative context without leaking a held service mutex.

Tests cover both directions of explicit/automatic contention, missing RAM, stale/invalid profiles, thermal/RAM/VRAM pressure, unknown required GPU capacity, unified-memory double counting, profiler error/panic, provider failure and cancellation after dispatch. Subsequent healthy execution verifies reservation release. Existing positive local fixtures now declare synthetic RAM estimates and initialize actual services; denial assertions and concurrency limits remain intact. A real HTTP/Ollama fixture verifies explicit and automatic requests are denied while an explicit request holds the single local slot, then both succeed after release with exactly three inference calls. The integration passed three race-detector runs. Sample configuration keeps RAM zero with instructions to supply a conservative real estimate instead of inventing one.

This closes the explicit-selection reservation bypass, not full hardware management: adaptive sizing, pressure queues, resident model unloading, cross-process budgets, token-accurate context sizing, production-provider/hardware qualification and hosted CI remain unfinished. Operator memory estimates must include weights and maximum context/runtime overhead; reservations cannot prove those estimates accurate. No Linear issue is marked complete.

Detailed health now marks enabled local models without RAM estimates unavailable with the validated model_metadata_missing reason. Mode-disabled models remain disabled, and health never acquires a resource reservation. A catalog entry alone can no longer make an otherwise metadata-incomplete local configuration ready.

Explicit-resource checkpoint verification: make check and make build passed. Health metadata regressions and shared-budget/cancellation tests pass under the race detector. Loopback fixtures and simulated pressure establish admission behavior; real model memory estimates, backend cancellation/unloading and production hardware classes still require qualification.

DAR-22/DAR-27 adaptive local concurrency: max_concurrent_local_models auto now constructs a budget capped by the configured worker ceiling instead of always using one. Each admission derives a tier from usable headroom after percentage limits, observed host usage and all outstanding reservations: below16 GiB one slot, through64 GiB up to two, above64 GiB the configured ceiling. Positive CPU thread count further caps admission; unknown CPU count permits one. Discrete-GPU candidates use the smaller RAM/VRAM headroom; RAM-only candidates do not inherit an unused discrete-GPU tier. Fixed numeric settings retain their fixed cap and all memory/pressure checks. Existing executions are not preempted when a new observation lowers the tier.

Byte ceilings now floor the exact rational value of the configured float percentage, avoiding float-to-uint64 rounding overflow and over-admission at large boundaries. Tests cover tier boundaries, CPU/worker caps, fresh pressure, prior reservations, GPU/unified memory, unknown measurements, fixed overrides, uint64 limits, concurrent admission/release and mixed RAM/GPU requests. Application fixtures prove two explicit/automatic requests can execute concurrently in the mid tier, a third is denied, and subsequent work succeeds after release without double reservation. Independent review found no blocking issue; resource and application race suites pass.

A local Apple M4 Max microbenchmark measured approximately0.3 microseconds and424 bytes/27 allocations per reservation+release for both fixed and adaptive budgets. This excludes host profiling, discovery, inference and complete routing overhead and is not production qualification. Tiers govern active in-process executions, not distinct resident models; queueing, model unloading, context resizing, CPU load measurement, full GPU/thermal profiling and cross-process resource coordination remain unfinished. No Linear issue is marked complete.

Configuration review also found hardware.auto_profile was previously parsed but ignored by service construction. False now installs an unavailable profile source without host measurements; local execution fails closed until a manual profiler configuration exists, while eligible cloud explicit/automatic routes remain usable. Tests cover disabled profiling with adaptive/fixed limits, no local task storage creation, and cloud-only selection from a hybrid pool.

Adaptive checkpoint verification: make check and make build passed. The resource race suite also passed three consecutive runs including mixed RAM/GPU admission. This establishes deterministic policy and fixture-backed execution behavior, not qualification on every physical hardware class or completion of the wider hardware-management requirements.

DAR-27 bounded pressure waiting: new hardware.local_pressure_policy reject|wait and local_queue_timeout100ms..5m settings default to reject/30s. The application retries only definite resource capacity denials with no task ID, polling every250ms within a separate admission context. Existing service slots bound waiting plus active requests. Automatic routing rebuilds planning state on retry (respecting its discovery cache), tries cloud alternatives before waiting and preserves privacy/model constraints. Invalid configuration/observations and provider errors do not become pressure retries. Fixed/automatic local reservations still release before retry or failure.

The admission context is separate from execution: raw provider/tool loops retain the original caller context and are never replayed by the pressure wrapper. A real service/Ollama fixture holds one local slot, admits a waiting explicit or automatic request after release, keeps its provider running beyond the full queue allowance, then verifies two successful durable tasks from exactly two provider calls. Tests also cover timeout, cancellation, permanent denial, no replay after task identity, cloud alternatives and context cancellation before dispatch. Queue settings require actual YAML strings in each layer, including shadowed lower-precedence values.

Waiters are in-process and non-FIFO, occupy normal execution slots, and do not reserve separate cloud capacity. Direct auxiliary review/summary calls still reject pressure. Durable submissions retain their existing leased claim during admission; configuration fingerprint changes require explicit handling of older pending work. Queue-state events, stronger fairness, cross-process reservation coordination, unloading and production pressure qualification remain unfinished. No Linear issue is marked complete.

Resource data errors now have a non-capacity sentinel, and all metadata validation precedes busy/thermal checks so invalid observations cannot masquerade as retryable pressure. Admission lock acquisition is context-aware, allowing queue expiry while another profiler still owns the mutex. Pressure expiry uses a distinct failure cause rather than context cancellation: a leased-submission fixture verifies timeout produces failed, explicit cancellation produces canceled, both preserve their original submission and create no task or provider call. Custom in-process profiling remains cooperative.

For sub-second queue allowances, polling uses one quarter of the allowance rather than250ms so a100ms wait can actually reconsider capacity before expiry. Normal waits remain capped at four admission polls per second; no resource mutex is held during the timer.

Pressure checkpoint verification: make check and make build passed. Provider-deadline separation, cancellation/timeout of leased waiting submissions, and timeout behind an occupied profiling mutex passed three consecutive race-detector runs. Production provider pressure, fair scheduling and cross-process resource admission remain unqualified.

DAR-22/DAR-37 GPU diagnostic discovery: resources.SurveyGPUs and darwin resources now expose separate NVIDIA/AMD source observations with per-device total/free byte counters. Linux probes run concurrently; unavailable or malformed sources emit no partial devices or raw driver errors and do not erase the other vendor's observation. Unsupported operating systems report unsupported explicitly. This is a diagnostic foundation for device-aware admission, not an aggregate allocator: no VRAM fields, fitness, storage or routing decisions are changed, and survey subprocesses are not introduced into the routing hot path. Model/device binding, per-device reservations, MIG/partition mapping, thermal/utilization measurements and real hardware qualification remain unfinished.

NVIDIA uses a fixed executable/read-only query with a one-second process deadline, 100ms pipe-drain allowance and 64KiB output ceiling; CSV permits at most32 devices and validates UUIDs, duplicates, MiB conversion and capacity bounds. AMD reads at most256 directory entries/32 canonical cards/64 bytes per scalar, validates PCI vendor and byte counters, and deliberately follows kernel sysfs device links. Missing vendor files (including some virtual/non-PCI cards) make the AMD survey unavailable; reads check context but cannot guarantee interruption of a stalled kernel operation. No drivers are installed, model memory moved or device power state changed. Primary references: https://docs.nvidia.com/deploy/nvidia-smi/index.html and https://www.kernel.org/doc/html/latest/gpu/amdgpu/driver-misc.html#mem-info-vram-total .

Fixture tests cover separate and partial source observations, concurrent probing/cancellation, parser overflow/malformed data/device limits, real temporary sysfs symlinks, subprocess timeout/failure/output bounds and preserved CLI host fields with generic failures. Independent review found and fixed an embedded bytes.Buffer.ReadFrom optimization bypassing the output limit; both normal and race tests exercise the copy path. CLI diagnostics now use the shared signal/cancellation and broken-pipe-safe JSON writer. The resource and CLI race suites pass; physical Linux GPUs and hosted CI have not been verified. No Linear issue is marked complete.

GPU diagnostics checkpoint verification: make check, make build and GOOS=linux GOARCH=amd64 go build ./... passed. The rebuilt CLI on this Apple unified-memory host preserves RAM fields and reports both Linux discrete-GPU sources unsupported. Native Linear was inspected in the correct DarwinRouter workspace; issue statuses remain unchanged. This proves cross-compilation and fixture-backed diagnostics, not live Linux driver compatibility or device-aware routing admission.

DAR-22/DAR-23/DAR-27 declared device admission: local model configuration now accepts strict source-qualified gpu_device strings with positive VRAM metadata. Empty bindings omit the JSON field so existing unbound configuration fingerprints are preserved. Bound configurations opt into host+GPU profiling; explicit, automatic, audit and summary paths share per-device VRAM reservations and host RAM/slot limits. Missing/stale/ambiguous observations fail as non-retryable resource data, not pressure. Legacy custom aggregate profiles remain supported but cannot overlap live per-device GPU reservations. Canonical UUID case aliases share one pool, and multiple device capacities are never summed into an overflowing aggregate.

Adaptive binding uses a global RAM/CPU/worker tier and independent device-active tiers; ample host memory can support one execution on each of two small GPUs while still preventing a second execution on the same small GPU. Fixed limits retain shared worker ceilings and byte checks. Automatic route persistence strips the full inventory and IDs, retaining selected-device scalar observations. Coarse model health resolves the configured device, not an unrelated aggregate. Auxiliary profiling now uses the shared cancellation/panic-bounded reservation helper.

This implements accounting for operator-declared backend placement, not affinity control or placement verification. Operators must pin inference backends correctly; wrong declarations can undermine safety. AMD indices are not stable across reboots. GPU probing can add up to the bounded driver-command delay to admissions; routing latency targets are not qualified here. Cross-process reservations, partition/MIG identity, multi-GPU sharding, automatic backend verification, thermal/load data and real Linux driver/hardware qualification remain unfinished. Adding or changing a binding changes queued submission configuration identity and does not silently migrate existing work.

Tests cover source/ID parsing, per-layer YAML string enforcement, inventory corruption/staleness, independent device pressure, shared RAM/CPU ceilings, adaptive device limits, case aliases, mixing prevention, uint64 boundaries and idempotent release. Real HTTP/application fixtures exercise explicit and automatic dispatch under concurrent reservations plus auxiliary review/summary admission and failure cleanup. Health and durable route fixtures confirm matching-device selection without inventory leakage. Resource/config/application race suites pass; no Linear issue is marked complete.

Declared-device checkpoint verification: make check, make build and Linux amd64 cross-build passed. These results establish fixture-backed admission/accounting and build compatibility, not backend affinity verification, driver latency qualification or production hardware safety. All broader PRD requirements remain tracked rather than equating these checks with a finished MVP.

DAR-14/DAR-40 durable active-task steering: schema14 stores up to32 task-scoped messages of at most64KiB each, with hashed idempotency keys, redacted text, acceptance time and atomic applied-event sequence. Queue acceptance serializes against cancellation and completion. Duplicate keys return the same record even after task closure; conflicting text rejects. Pending completion and retryable-failure gates guarantee no append occurred, allowing the loop to recheck guidance or suppress a fallback that would discard it. Legacy read-only stores report no steering without migration; older binaries reject schema14.

The runtime drains guidance before model turns and after full tool batches, preserves preceding assistant output and commits steering.applied before passing the user message to the provider. The final-answer completion race rechecks the queue; last-turn guidance cannot silently expand the budget. Source failures/panics fail closed and ambiguous persistence is not retried. Context checks cap serialized conversation at4MiB plus configured token estimates. runtime.max_turns defaults8/range1–32 even without tools, with enabled-tool max_turns as an additional cap. This new config field changes pending submission fingerprints. Applied means added to context, not model compliance or proof another provider turn happened; failure/cancellation can leave inspectable pending or applied-but-unexecuted guidance.

Authenticated POST /v1/tasks/{id}/steering and GET /v1/tasks/{id}/steering/{message} use independent two-slot capacity, bounded inputs, strict JSON fields and metadata-only responses. Application services redact configured secrets before persistence and expose the same operation across processes. Replay validates safe boundaries/unique message identities and preserves user-message sequence provenance through compaction/continuation. Terminal submission projection refuses stale output predating steering. Output validity now selects only final-attempt checks, preserving duplicate/contradictory evidence rejection without counting a superseded answer as the task's result.

Tests cover enqueue/complete races, idempotence/conflicts, cancellation/closure/limits, schema migration and legacy reads, transactional apply rollback, tool pairing, iteration/context/source errors, persistence ambiguity, fallback suppression, replay/compaction and final-attempt evidence. Real service fixtures queue through a separate service while inference is blocked, verify redaction and revised next-turn output, then replay durable state; a real HTTP fixture verifies accepted guidance reaches the provider and GET reports applied. General interrupted-task recovery, interactive CLI steering, pending-guidance lists and post-terminal follow-up orchestration remain unfinished. No Linear issue is marked complete.

Steering checkpoint verification: final make check, make build and Linux amd64 cross-build passed after the final-attempt scoring/recovery regressions. Fixtures prove durable queue/application ordering and HTTP behavior, not real-provider compliance with guidance or complete interrupted-work recovery. No production database or Linear issue status was modified during verification.

DAR-38/DAR-40 steering CLI and discovery: darwin steer --config/--task/--key reads bounded guidance from stdin and queues it through the same application service, returning metadata only. Separate steering list/show commands inspect an existing database read-only; list omits text and show explicitly exports it. Required flags are accepted once, invalid arguments are rejected before input consumption, and diagnostics do not echo values. A shared public SteeringReceipt detaches metadata from payloads and applied-sequence pointers. GET /v1/tasks/{id}/steering exposes the same bounded metadata list using independent control capacity.

Store.ListSteering reads a consistent lifetime queue in insertion order, at most32 records/2MiB total text, and refuses malformed/oversized records, invalid task heads or applied counters beyond the head. Actual legacy stores without a steering table return an empty list without migration; unknown tasks fail. Input reads cap64KiB and enforce UTF-8/nonblank content. Actual pipes use a borrowed nonblocking descriptor and cancellation-triggered read deadlines, joined before cleanup; original flags and ownership are restored. Terminals/sockets are rejected. Regular files and custom readers remain cooperative at the kernel/reader boundary. CLI input has a five-second allowance and uses shared cancellation/broken-pipe-safe output.

Tests cover strict arguments, rejected input before mutation, output failures/idempotent receipts, legacy/missing stores, record corruption, source bounds, blocked-pipe cancellation and caller descriptor reuse. A real running-task CLI fixture queues while the first provider call is blocked, lists pending metadata, verifies the next provider receives guidance and returns revised output, then shows the persisted applied message. The HTTP integration also lists before and after application without text leakage. This is separate-command steering, not a complete interactive prompt UI, automatic follow-up orchestration or interrupted-task resume; those requirements remain open. No Linear issue is marked complete.

Steering CLI checkpoint verification: make check, make build and Linux amd64 cross-build passed. The actual-pipe input suite also passed three consecutive race-detector runs. No production task database was modified; live-provider semantic compliance, an interactive editor and full recovery remain outside this checkpoint's evidence.

DAR-38 line-oriented chat: darwin chat shares the existing run configuration/routing constraints and application RunStream/SteerTask services. Successful tasks supply persisted continuation for subsequent prompts; failed/canceled tasks retain the prior successful source. Commands expose status, safe-boundary guidance, cancellation, new conversation and quit. Ordinary input while busy is explicitly rejected instead of silently queued. Each task has a five-minute execution deadline; EOF waits for completion, active Ctrl-C cancels without leaving chat, idle Ctrl-C exits, and quit/termination/output failure cancel and join execution. Normal session exit is not an aggregate per-task success code.

The single-writer event loop prints task/turn/tool lifecycle progress and the final answer, not token deltas. Terminal sanitization strips ANSI/clipboard/control/bidi sequences from display without rewriting stored evidence. Input accepts canonical TTYs and UTF-8 files/pipes with bounded64KiB lines. Borrowed descriptors use cancellation deadlines with no idle expiry, joined before restoration/close; terminal echo/canonical state stays unchanged. Custom embedded IO and regular-file kernel operations remain cooperative. Tests cover command races, active/idle signals, failed continuation retention, late completion during audit, rejected steering, EOF, broken output, malicious terminal sequences and an actual Darwin PTY with preserved termios/FD flags. No live provider or production database was used.

A full-screen/multiline editor, automatic context compaction, general interrupted-task recovery and complete interactive feedback/approval interfaces remain open. This checkpoint does not mark DAR-38 or the MVP complete, and does not change Linear statuses.

Chat checkpoint verification: make check, make build and Linux amd64 cross-build passed. An end-to-end local HTTP fixture exercises the real application service through pipe-driven chat, verifies two-task durable parent linkage and exact prior-answer context, checks terminal-only sanitization and EOF cleanup without closing caller input. Fixture-backed checks do not establish production model quality or terminal behavior on every platform.

DAR-29/DAR-38 interactive explicit feedback: chat now offers /feedback accepted|rejected COST, /feedback-show and /feedback-revise EXPECTED_ID accepted|rejected through the existing operator-only application feedback services. Cost remains explicit and finite/nonnegative, never inferred from absent usage. Feedback applies only to the latest displayed successful answer; a new prompt or /new clears the target, active work rejects feedback, and neither failed tasks nor an initial continuation ID silently point feedback at an older answer. Standalone task-addressed commands remain available for older results.

History display contains validated IDs, resolved outcomes and evidence sources only. Conflicting initial feedback requires an explicit prior-ID revision; identical retries do not create extra contributions. Revisions retain immutable evidence and execution measurements, and cannot replace objective validation. User feedback remains separate from automatic audits. Tests cover malformed commands/nonfinite costs, generic errors, metadata bounds, stale target prevention, busy/reset/failure handling and a real local HTTP/app/storage chat flow proving duplicate feedback/revision retries preserve one fitness sample while stale conflicting revisions fail. Cost correction, automatic natural-language feedback interpretation and interactive approval tools are not implemented; no Linear status was changed.

Interactive feedback checkpoint verification: make check, make build and Linux amd64 cross-build passed. The real service fixture also verifies empty history before first feedback. These results prove command-to-storage accounting and target safety under fixtures, not production provider quality or full MVP qualification.

PRD6.1 embedded Go SDK: migrated the module and existing internal imports from the temporary darwinrouter path to github.com/ArronJablonowski/DarwinRouter. sdk/v1 exposes a Client backed by the same application service, with public request/result records rather than aliases to internal structs. Requests require Version1 before execution; results declare Version1. Explicit config files and scalar maps retain layered validation, and secret lookup is caller-supplied rather than implicitly reading process environment. One reused client shares admission budgets; separate clients/processes do not share hardware reservations.

The facade includes run/committed-event streaming, durable cancellation and steering, and explicit feedback/history/prior-ID revisions. Nil/zero clients and invalid versions reject safely. Event delivery uses existing failure/panic cancellation and durable cleanup behavior; callers still own deadlines and cooperative callbacks. Construction starts no daemon/background supervisor or persistent database handle, so Close is unnecessary. A compilable public-import example and SDK usage/safety documentation accompany the client. Application-level pluggable Provider/Tool/ContextEngine/MemoryStore/SkillStore/Evaluator/ResourceProfiler contracts and extension hooks remain unfinished; this client is not represented as full SDK or MVP completion. No stable release/tag or Linear status change is made by this checkpoint.

SDK checkpoint verification: make check, make build and Linux amd64 cross-build passed. A separate temporary consumer module builds with public SDK/runtime imports, GOWORK disabled and dependency downloads disabled, then exercises a real local HTTP fixture. Every streamed callback is checked against an independent read-only database connection to prove prior commit. The consumer also verifies version rejection, safe callback failure without provider dispatch, result/event secret redaction, ordinary Run and immutable feedback revisions. Existing Go-file migration was reviewed as import-path-only (plus gofmt), and make fmt now includes SDK/examples and the previously omitted health/metrics/submissions directories. This establishes external embedding against fixtures, not release publication, production model qualification or all extension contracts.

PRD6.1/6.2 durable task inspection: sdk/v1.Client.InspectTask returns a versioned public snapshot through a shared read-only application adapter. CLI task show and the daemon inspection route now use Store.TaskSnapshot rather than replaying multiple changing read pages. One SQLite read transaction fixes the head while length preflights bound reconstruction to10,000 events/8MiB. Exact sequence, record identity, session and terminal-head checks precede existing semantic replay. Every error returns no partial snapshot; storage is neither created/migrated nor repaired, and no model/tool dispatch occurs.

Replay retains pending/dispatched tool calls, uncertain effects and unfinished-turn flags. An unfinished turn on an active task does not imply the worker stopped. App/SDK inspection has a ten-second maximum context allowance and generic non-echoing diagnostics. Returned content is detached from durable state but remains sensitive. The external-consumer fixture now inspects a completed redacted answer using public imports. Additional tests cover canceled/nil clients, absent database without creation, interrupted callback failure without extra inference, returned-message mutation isolation, corrupt/gapped/oversized records and concurrent append consistency. General interrupted-task recovery/reassignment and full SDK extension contracts remain unfinished; no Linear issue is marked complete.

Inspection checkpoint verification includes full race checks, CLI build and Linux amd64 cross-build. The storage boundary suite also accepts a valid seven-MiB history and rejects an aggregate eight-MiB payload plus event-envelope overhead without partial state, even though individual events are below the limit. Opaque event IDs containing colons remain compatible with canonical runtime validation; they are not confused with URL cursor identifiers. These tests establish bounded coherent inspection, not automatic recovery authorization or production fault qualification.

PRD6.1 durable SDK intake: Client.Submit, SubmissionStatus, ListSubmissions and CancelSubmission expose the existing daemon queue workflow without introducing a second execution path. Request Version1 and nil/canceled context guards run before work. Submission stores only; a separately running matching-config dispatcher performs admission and execution. Public submission records retain schema versions and metadata-only listing; status may contain sensitive completed output. Keys remain explicit/hashes persisted, conflicting reuse rejects, configured secrets in intent are rejected, and running cancellation remains cooperative. No SDK constructor or submission call starts an implicit background worker. Full extension contracts and broader MVP requirements remain open.

SDK intake checkpoint verification: make check, make build and Linux amd64 cross-build passed. SDK tests cover nil/version/cancellation guards without database creation, restarted-client idempotence, conflicting reuse, metadata-only listing, queued cancellation and actual dispatcher consumption of an SDK-created request with one observed provider call. The unrelated external Go consumer also exercises queue/status/list/cancel through public imports with no extra inference. This is fixture-backed interoperability, not a general exactly-once guarantee across uncertain effects or complete recovery qualification. No Linear status changed.

PRD6.1 SDK durable event reads: Client.ReadEvents exposes bounded committed pages through a read-only application adapter with ten-second context allowance. Clients resume from their own last processed sequence without rerunning work. Each page fixes its own head transactionally; active task heads may advance across pages. Invalid cursors and individually oversized events retain public error identities, while storage/corruption errors are generic and return no partial payload. Metadata size preflights now protect event-page session/state/event-ID reads before allocating strings. Existing private-content redaction and per-page payload limits remain unchanged. Live subscription, consumer exactly-once delivery and general interrupted execution recovery are not implied.

SDK event-read checkpoint verification: make check, make build and Linux amd64 cross-build passed. Tests cover new-client cursor continuation, page order/redaction, empty tails, invalid cursor identity, cancellation/missing stores without creation and corrupt second events returning no partial page. The external Go consumer exercises public paged reads with no extra provider calls. Store tests cover oversized metadata and opaque event-ID compatibility. These are durable-read fixtures, not a production resumable-execution qualification; no Linear status changed.

DAR-22 hardware profiler input hardening: Darwin sysctl/vm_stat now use the shared fixed-command probe with64KiB output, one-second per-process and100ms pipe-drain limits under the existing three-second overall allowance. Linux proc-memory reads are bounded before parsing and require a regular proc/file source; kernel read cancellation remains cooperative. Relevant meminfo counters require strict unsigned kB values, unique keys, nonzero total and available<=total; swap counters are either both present or both absent and must be consistent. Unknown counters do not replace missing facts. Darwin reclaimable-page categories reject duplicates instead of double-counting them. Nil/pre-canceled profile calls reject before host operations. Linux cgroup accounting, additional thermal/load measurements and actual memory admission under production pressure remain unfinished.

Profiler checkpoint verification: make check, make build and Linux amd64 cross-build passed. An actual darwin resources invocation completed successfully on this Apple Silicon host with measured RAM/swap and explicitly unavailable discrete-GPU/thermal fields. Boundary fixtures exercise64KiB reads, duplicate Darwin page categories and malformed/overflowing Linux counters. Linux behavior remains fixture/cross-build verified, not tested on physical Linux hardware; no Linear status changed.

DAR-38 headless prompt cancellation: run now installs signal cancellation before configuration/stdin loading and gives prompt input a30-second allowance before the separate five-minute execution budget. Submit uses the same reader under its existing30-second command budget. Both accept at most1MiB valid nonblank UTF-8 and preserve prompt whitespace. The shared reader borrows canonical terminals/pipes through existing pollable descriptor handling, restores ownership/flags and joins cancellation callbacks, without spawning an unjoinable read goroutine. Invalid input fails before task/submission storage is opened. Regular-file kernel reads and custom embedded readers remain cooperative; configuration/summary file operations are not claimed forcibly interruptible. General interactive editing, partial-execution recovery and broad MVP qualification remain unfinished.

Prompt intake checkpoint verification: make check, make build and Linux amd64 cross-build passed. Reader tests cover exact/oversized input, invalid UTF-8, blank/panicking sources and canceled blocked OS pipes with restored caller descriptors. Real run/submit subprocesses use a FIFO configuration handshake to prove signal handlers are installed before SIGTERM; they return handled exit1 with no task database/WAL/SHM while stdin remains open. This proves cancellation at the configuration/input boundary, not forced cancellation of arbitrary configuration file reads. The process suite passed twice under the race-enabled harness; no Linear status changed.

PRD6.1 resource-engine injection: SDK ConfigOptions.ResourceProfiler accepts the public versioned measurement interface and installs it through NewServiceWithProfiler. Existing service construction is unchanged without injection. Explicit engines can provide manual measurements with auto_profile disabled; absent engines retain fail-closed local admission. The adapter rejects callback failures/panics/incompatible versions, detaches bounded metadata and normalizes source text. Existing model requirements, freshness checks, privacy filters, percentages and concurrency reservations still decide admission. Trusted callbacks must honor cancellation and return accurate immutable observations; in-process code is not an isolation boundary. Engines are not persisted into daemon submissions. Wider SDK extension contracts and profiler budget recommendations remain unfinished.

Resource-engine checkpoint verification: make check, make build and Linux amd64 cross-build passed. Adapter tests cover typed nils, callback cancellation/panic/version errors, metadata bounds and detached pointer/GPU data. SDK integration verifies valid manual admission, invalid/stale/unknown/capacity failures before storage/inference, local/cloud privacy and a shared one-task ceiling while inference is blocked, followed by successful reservation release. The external consumer compiles a custom public profiler. These fixtures do not validate the accuracy or egress behavior of arbitrary third-party measurement code; no Linear status changed.

PRD6.1 factual-memory engine injection: SDK ConfigOptions.MemoryStore supplies the existing public CRUD/query/expiry contract through NewServiceWithEngines. Direct explicit and automatic routes use the selected store for opt-in context retrieval, retaining configured scope/privacy, version/provenance/expiry/UTF-8 validation, count/byte limits and credential redaction. Retrieval failures/panics are generic admission failures; queries receive a three-second cooperative deadline. Runtime does not call store mutations, and injection neither enables memory nor changes tool authority. Store lifetime/concurrency and local-only egress compliance belong to the trusted embedding application. CLI/API operator memory remains SQLite; callbacks are not serialized into daemon submissions. Automatic memory creation, semantic retrieval and wider extension contracts remain unfinished.

Memory-engine checkpoint verification: make check, make build and Linux amd64 cross-build passed. SDK integration confirms scoped/redacted fact envelopes reach the provider and durable history without put/delete/touch/expire calls. Disabled memory never queries; wrong-scope/private/expired/version-invalid/error/panic results never reach inference. An automatic-route test verifies exactly one retrieval snapshot is reused through execution. These fixtures do not qualify external store durability, remote-store privacy or automatic memory learning; no Linear status changed.

PRD6.1 procedural-skill engine injection: SDK ConfigOptions.SkillStore now supplies the public skills.Store contract to explicit and automatic execution. Nil retains read-only filesystem retrieval. Injection requires existing opt-in skills configuration and does not call Draft, Activate, Rollback or Close. Independent discovery/version validation enforces exact scope/domain, identifiers, duplicate/count limits, UTF-8, bounded draft structure, pinned-version metadata and SHA-256 agreement before context assembly. Tool availability, privacy, credential redaction and final context-size limits still apply. Automatic routing freezes one retrieved snapshot for execution.

Discovery and loading share a three-second cooperative deadline; callback errors/panics fail admission with generic diagnostics. The trusted host still owns concurrency, cancellation, egress compliance and honest deterministic activation validation: a digest proves integrity, not successful workflow validation. CLI/API management remains filesystem-backed and queued submissions cannot serialize injected stores. Automatic skill drafting/revision/regression detection and broader SDK lifecycle hooks remain unfinished.

Skill-engine checkpoint verification: make check, make build and Linux amd64 cross-build passed. Tests cover malicious metadata/versions, byte/list bounds, cancellation, errors/panics, scope/domain filtering, missing tools, redaction and caller-owned data. Real application and SDK fixtures verify pinned workflows reach provider context and durable inspection, disabled skills never call the store, invalid stores cannot reach inference, and automatic routing retrieves once. External store durability and remote privacy are not qualified; no Linear status changed and the MVP remains incomplete.

DAR-19/DAR-21 model-callable delegation: workers.delegate_model now opt-in exposes a strict delegate tool in explicit and automatic application runs. The operator selects one known-cost/context model, per-parent call limit1–16 and a separate per-call estimated cost ceiling. Children get only the explicit16KiB prompt, no ambient memory/skills/history/tools, one inference turn,30-second deadline and64KiB output cap. Local parent privacy cannot be relaxed. Nonblocking shared execution admission and normal fail-fast resource reservation prevent deadlock while the parent retains capacity; single-slot/single-model hosts return a controlled unavailable result. No recursive tools, auxiliary review or automatic fallback runs inside children.

Each delegation uses a durable supervisor work log plus a separately linked inference log. Canonical self-correlation and ParentTaskID linkage permit ordinary replay/inspection. Worker leases/heartbeats remain active through execution and deterministic acceptance; SQLite now checks worker ownership transactionally with work-event persistence, including acceptance/completion. Child output must pass nonempty or requested Go-syntax validation before release as explicitly untrusted tool content. The application journal retains secret redaction and propagates submission ownership through all logs. Parent/work-task cancellation cancels and joins children; failed validation/admission never exposes candidate text as a successful result. This gate does not prove correctness or automatically contribute user feedback/fitness.

General interrupted-delegation recovery, terminal submission-tree projection, shared-model reservation handoff, read-only child tools, parallel batches, dynamic worker routing and approved side-effecting tools remain unfinished. The separate delegate estimate allowance is additional to parent Request.MaxCost, not an aggregate measured billing guarantee. Supervisor hosts using custom journals must provide LeaseJournal for atomic ownership checks; the application and SQLite implementation do so. No Linear issue is marked complete by this checkpoint.

Delegation checkpoint verification: make check, make build and Linux amd64 cross-build passed. Real Ollama-compatible fixtures exercise parent tool call → isolated child → accepted parent result, plus empty/invalid-Go rejection and one-slot refusal. Submitted runs retain parent/work/inference task IDs and ownership metadata; work and inference logs pass ordinary inspection/event-page validation. Separate race tests cover work cancellation and callback joining, strict/duplicate arguments, call ceilings, failed initial persistence, stale submission tokens, local-to-cloud denial, retained resource reservations, expired/released/reassigned worker ownership and cancellation cleanup. These fixtures do not qualify production models, actual billing, low-memory handoff or automatic recovery of delegated task trees; the MVP remains incomplete.

DAR-17/DAR-21 scoped read-only child tools: workers.delegate_read_tools is an explicit opt-in requiring parent file tools and a local worker. A private process-local capability lends the parent's existing root-bound registry and an inherited policy to the child. The child advertises only read_file, retains ancestor deny/ask rules, cannot recursively delegate or write, and never reopens the configured root pathname. Parent execution joins all child work before closing the borrowed root. No ambient session history, memory or skills are added; file-derived context stays local.

Read-enabled children use workers.delegate_max_turns (2–8, default4), capped by parent runtime/tool iteration limits, with the existing30-second total deadline and64KiB output bound. Configuration checks the known model estimate times the full declared child turn budget against the separate delegate cost ceiling using overflow-safe division. Inference-only children remain one turn by default. Durable tool pairs, submission fencing, worker acceptance and resource reservations remain on the existing runtime paths. Parallel child batches, approved writes, general delegated-tree recovery and shared-model handoff are still unfinished; no Linear status changed.

Read-tool delegation checkpoint verification: make check, make build and Linux amd64 cross-build passed. Real HTTP fixtures verify child-only read_file exposure, scoped file contents, durable tool pairs, path-escape rejection, blocked recursive delegation and bounded multi-turn failure. Policy/capability tests cover nil/deny/ask/ancestor rules, missing authority before storage/inference, and rename-plus-replacement of the workspace path while the inherited handle keeps its original binding; symlink escapes remain denied. Config tests verify local/parent-tool prerequisites, iteration limits, layered overrides and overflow-safe cost bounds. Fixtures do not qualify hostile filesystems, production models, measured billing or full worker recovery; the MVP remains incomplete.

DAR-19/DAR-21 parallel child batches: enabling delegation now exposes delegate_batch for2–4 independent tasks using the same child execution, inherited authority, resource admission, leases, submission fencing and acceptance gates as single delegation. One atomic per-parent counter is shared between both tools. Invalid or over-budget batches start no children; successful reservation charges each task before dispatch. A bounded goroutine set runs tasks concurrently and joins every child before the parent can release its root or reservations. Results preserve input order, with independent validated successes/failures; cancellation suppresses late successes. Per-item encoded envelopes are bounded to128KiB, aggregate responses below1MiB, without truncating JSON or exposing backend errors. Oversized accepted answers remain in child history but are withheld from the batch response.

Parallel batches do not promise all items will acquire capacity: parent-held slots/memory remain reserved and unavailable children fail promptly. Child recursion remains denied, including the new batch tool, and automatic routing estimates include both tool schemas. General interrupted-tree recovery, dynamic child routing, shared-model handoff and approved side-effecting tools remain unfinished. No Linear issue is marked complete.

Parallel-batch checkpoint verification: make check, make build and Linux amd64 cross-build passed. A real provider barrier proves two child HTTP requests overlap before either can finish, verifies ordered envelopes and distinct completed work/inference logs, and rejects insufficient aggregate allowance without dispatch. Mixed single/batch tests verify a shared exact call ceiling and no budget consumption on oversized reservation refusal. Handler tests cover malformed/oversized prompts, canceled preflight, concurrent order, joined cancellation, late-success suppression, panic/error/effect normalization and response-size limits. These fixtures do not qualify production throughput, provider billing or automatic delegated-tree recovery; the MVP remains incomplete.

DAR-14/DAR-19/DAR-43 completed delegation-tree recovery: expired submission claims with matching configuration can now project fully terminal single/batch delegation trees without re-execution. One write transaction fixes task-start membership, preflights an aggregate66-task/10,000-event/8MiB envelope, verifies each head/journal and projects a root plus optional no-output fallback. Strict worker lifecycle replay requires durable acceptance before completion; each accepted worker must have exactly one successful inference child with matching text. Graph checks reject duplicate nodes, recursive/cyclic links, mismatched submission/session/privacy and invalid fallback lineage. External root continuation identity must match the original submission request, preventing orphaned children from becoming roots.

Recovery returns only the parent's answer and usage; PreviousTaskIDs contains fallback attempts, not delegated children. All child IDs stay in submission inspection. Any nonterminal node leaves the ledger untouched; corrupt/unsupported trees require inspection. Existing cancellation override, immutable recovery audit, transactional rollback and prior-owner fencing remain in force. This does not resume interrupted work, rerun tools, reconstruct auxiliary audits or update fitness. Approved writes, general interrupted-tree recovery and broader MVP qualification remain unfinished; no Linear status changed.

Tree-recovery checkpoint verification: make check, make build and Linux amd64 cross-build passed. Fresh-service/dispatcher fixtures recover real single and parallel delegated submissions with identical parent answers and task IDs, unchanged provider call counts and journals, and one idempotent recovery audit. Incomplete worker heads and forged worker output leave the submission and audit history untouched. Projector tests reject missing validation/children, duplicate or cyclic graphs, wrong submission/session/privacy, altered accepted text and invalid fallback attribution. Existing storage corruption, cancellation-race and rollback tests continue to pass, including a new orphan-parent intake mismatch. These fixtures do not establish general process-crash recovery of in-flight delegated work or production durability qualification; the MVP remains incomplete.

DAR-43 completed-tree process-crash qualification: new subprocess fixtures execute and durably finish single-child and parallel-batch submissions, acknowledge that boundary over a bounded pipe, and are explicitly terminated with SIGKILL before ledger finalization. Tests join and verify the killed process, wait for natural claim expiry, and start a dispatcher in a different process. Recovery retains the exact parent answer and all3/5 linked task IDs, performs no additional provider calls, leaves every journal unchanged and writes exactly one terminal-history audit. This proves the tested completed-but-unacknowledged boundary, not recovery of partially executed children or storage-device failure.

Additional recovery-boundary tests accept an exact10,000-event tree and reject aggregate overflow despite individually valid histories, reject more than66 nodes, enforce the8MiB canonical envelope and separately enforce raw SQL-source size even when JSON padding disappears on decode. SixMiB of raw fixture padding across two valid histories is accepted; eightMiB plus event overhead is rejected without ledger/audit changes. Continuation binding accepts the matching original parent and rejects mismatches/non-string values; returned result metadata cannot mutate source histories. make check, make build and Linux amd64 cross-build passed. No Linear issue was marked complete; broader MVP implementation and qualification remain open.

DAR-39 / PRD public streaming interface: OpenAI-compatible streaming now uses a distinct application/SDK RunTextStream callback instead of buffering the final result. Provisional assistant text is delivered synchronously only after its corresponding lifecycle marker commits. Sequential incremental literal redaction matches the existing longest-first secret replacement policy, holds possible secret prefixes across chunks and assistant turns, and retains bounded matcher state without retaining input buffers. UTF-8 boundaries are preserved. Pending tails flush only on durable task completion and are discarded on failure. Raw token text remains absent from the event log; existing RunStream lifecycle/replay semantics are unchanged. Tool contents and delegated child streams are not forwarded.

The daemon wires this into authenticated /v1/chat/completions with X-Darwin-Stream-Mode: live-redacted. Role and content chunks arrive before completion; finish and DONE require successful service return. Post-header failures use sanitized SSE errors without success markers; disconnects and callback/write/flush errors cancel execution. Output remains bounded to1MiB with write deadlines on supported transports. Intermediate assistant turns may be present, so concatenated stream text need not equal the final Result.Text; streamed content is not acceptance evidence. Known-secret redaction is not general sensitive-content classification. Request support remains text-only model/messages/stream; tool-call passthrough, stream_options usage and full OpenAI API parity are not implemented. Token text is not replayable, and cooperative custom callbacks/writers remain trusted host responsibilities.

Live-stream checkpoint verification: make check, make build and Linux amd64 cross-build passed. Real HTTP and SDK fixtures block provider completion until actual content is received, verify split-credential redaction, and cover failure/disconnect without false success. Application tests verify delta-marker durability before delivery, no delivery after failed persistence, callback error/panic cancellation, UTF-8 boundaries and cross-turn redaction. Incremental redactor partition/overlap/replacement tests and a five-second fuzz run passed. API tests cover absent streaming capability, size/UTF-8 bounds, sticky delivery errors, writer panics, short writes and flush failures. Production-provider/client compatibility and broader MVP qualification remain open; no Linear status was changed.

PRD6.1 provider-engine injection: SDK ConfigOptions.ProviderFactory now supplies providers.Factory.Build with a versioned Connection containing the configured identity, endpoint/kind, resolved key and existing policy-bound transport. Nil preserves built-in HTTP adapters. Existing constructors remain compatible through NewServiceWithProviderFactory. Explicit/automatic execution, delegated children, model discovery, audits, summaries and health checks use the same configured factory. Model selection, deployment/privacy admission, resource reservations, tool permissions, redaction and durable outcomes stay in the application. Factory credentials and transport are excluded from JSON serialization.

Custom construction uses a cooperative three-second deadline (shorter enclosing discovery/health deadlines prevail). Returned adapters are guarded against panics, unsanitized error codes, malformed/oversized model catalogs, invalid completion streams and ignored callback errors. Requests and callback metadata are copied; partial output suppresses retryability. Model catalogs are bounded to4096 unique UTF-8 identifiers of at most256 bytes, and custom stream content/arguments are bounded to16MiB before the runtime's potentially stricter budget. Arbitrary Go factories remain trusted code: they must use the supplied transport, reject redirects, honor synchronous callbacks/cancellation and manage concurrency/lifetime; this is not a sandbox or protection against deliberate out-of-band networking. Configuration provider kinds remain unchanged. Custom engines are process-local, not serialized with queued submissions; tool/context/evaluator injection and broader hooks remain unfinished.

Provider-engine checkpoint verification: make check, make build and Linux amd64 cross-build passed. Application fixtures verify custom explicit/automatic/direct-child execution and discovery, plus factory failure propagation through audits/summaries/health. SDK HTTP fixtures verify supplied transport/metadata, live output, secret redaction, explicit capability/mode denial before construction, and nil-factory compatibility. Contract tests cover factory error/panic/typed nil, cancellation, known/unknown failure normalization, partial-output retry suppression, missing/late/invalid completion, ignored callback errors, exact metadata limits, ownership isolation and credential omission from JSON. Custom third-party adapters, production provider compatibility and full MVP qualification remain unverified; no Linear issue status changed.

DAR-22/DAR-27 Linux cgroup-v2 profiling: host meminfo is now bounded by each visible cgroup ancestor's memory.max and memory.high, with available capacity limited by that ancestor's current usage and clamped at zero on overuse. Treating memory.high as capacity is deliberately conservative; it is the kernel's throttle boundary, not an OOM boundary. CPU capacity incorporates cpu.max bandwidth (floor with minimum1) and effective cpuset counts. The resulting snapshot feeds existing admission and adaptive concurrency instead of merely changing diagnostics. Source is fixed linux-proc-cgroup-v2; swap remains host-level and unavailable thermal sensors remain unknown. Semantics were checked against https://docs.kernel.org/admin-guide/cgroup-v2.html and the kernel proc mountinfo documentation.

Membership/mount discovery requires canonical bounded paths, safe mount escapes and an unambiguous matching cgroup2 mount. Traversal visits at most64 visible levels, requires cgroup.controllers as a live-node sentinel, and rejects partial/malformed/unreadable controller records. Proc/mount metadata is re-read to reject observed migration/remount races. Reads share a cooperative three-second budget with64KiB source bounds; kernel reads are not forcibly interruptible. Legacy v1 memory controllers, unresolved namespace '..' paths and ambiguous mounts fail closed. Hidden ancestors, legacy CPU controller limits, external inference-server placement and concurrent kernel changes are not inferred. This is observational accounting, not cross-process reservation or OS isolation.

Cgroup checkpoint verification: make check, make build and Linux amd64 cross-build passed. Pure and fixture-map race tests cover parent/child headroom, memory.high, unlimited/over-limit usage, absent controllers versus missing groups, malformed/oversized numbers, exact quota/cpuset bounds, mount-root mapping, path ambiguity/traversal, cancellation, migration and depth limits. An admission test rejects requests exceeding the measured headroom and proves a one-CPU ancestor quota prevents a second reservation despite ample RAM. Tests do not require or mutate real cgroups. Live Linux container/hardware qualification, v1 memory support, broader thermal profiling and the full MVP remain unfinished; no Linear status changed.

DAR-22/DAR-43 live Linux qualification: the local Docker daemon was available and used for isolated Linux/arm64 verification with cached Alpine3.22 image sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce. The real kernel exposed512MiB memory.max,150000/100000 cpu.max and zero swap allowance; darwin resources reported linux-proc-cgroup-v2,536870912 total bytes, bounded availability and one effective CPU. Separately, cross-compiled resources and internal/app test binaries passed in network-disabled read-only Linux containers with tmpfs scratch storage. Resource tests used512MiB/1.5CPUs; application tests used1GiB/2CPUs. These Linux test binaries were non-race; the full native macOS race suite passed separately.

Added optional make qualify-linux-cgroup with a cached-image-only, architecture-aware shell runner and bounded Go report verifier. It resolves an immutable image ID, disables dependency/toolchain downloads, mounts only its temporary binary read-only and never mounts credentials/workspace/Docker socket. Exact Docker-created container ID and owned temporary files are cleaned up on normal exit; uncatchable signals or a failed daemon remain cleanup limitations. The runtime command has a container-side15-second timeout, while build/daemon responsiveness remains cooperative. Verification rejects missing/null required counters, malformed output, ignored limits and unsupported environments rather than skipping. make qualify-linux-cgroup, make check, make build, Linux amd64 cross-build and shell syntax checks passed. No Alpine test containers remained after the runs. See docs/linux-qualification.md for scope and repeatability; this is not OOM, GPU, thermal, production-model or full Linux MVP qualification. No Linear status changed.

PRD6.1/DAR-16/DAR-17 read-only tool extension: SDK ConfigOptions.Tools and ToolPolicy now install up to16 host-defined tools through immutable tools.Extension. Schema/name/scope/description bounds, reserved runtime names, duplicate identities and remote schema references are checked before execution. Policy resolution snapshots at most32 acyclic ancestors and256 rules; only effective Allow definitions enter the executable catalog. Deny and Ask do not grant authority. Exact resolved tool/scope rules apply only to custom definitions, not built-ins. Compiled schemas are shared immutably while exposed metadata is copied, and registry merges reject collisions atomically. Tool argument and result boundaries now reject malformed UTF-8 instead of silently replacing it.

Explicit/automatic and streaming application runs merge the extension into normal tool execution, context estimation and skill availability. Active tools require local execution with known context capacity and obey configured tool/runtime turn limits. They do not enable ambient filesystem access and are not inherited by delegated children. Handlers remain trusted, cooperative in-process code with caller-owned lifecycle/concurrency, not a sandbox or enforceable proof of read-only behavior. Side-effecting definitions are rejected. Because no durable implementation identity exists yet, Submit and submission-bound execution reject active extensions before storage/dispatch rather than substituting different handler authority after restart. Synchronous/streaming use is supported; durable extension identity, approved writes, evaluator/context engines and broader hooks remain unfinished.

Tool-extension checkpoint verification: make check, make build and Linux amd64 cross-build passed. The Linux/arm64 application test binary also passed in an isolated1GiB/2CPU, network-disabled read-only Alpine container with tmpfs scratch storage (non-race); native macOS race tests passed separately. Tests cover provider→tool→final execution, durable replay, explicit/automatic local admission, context/turn ceilings, child isolation, no implicit read_file, schema/policy mutation isolation, inherited deny/ask, invalid schemas/UTF-8, concurrent compiled-schema use, atomic collisions and queue rejection with unchanged ledger/no provider or handler calls. Custom production handlers, approval-backed side effects and full MVP qualification remain unverified; no Linear status changed.

DAR-17/DAR-20 approval foundation: schema15 adds a durable, one-use tool approval ledger. Versioned requests bind task, turn, call, tool, exact resource scope, argument/schema/policy digests and a maximum ten-minute validity window. Operator-attributed approval/denial and pre-consumption revocation are retained; exact request/decision replays cannot restore spent authority. Consumption holds the SQLite writer transaction while validating cancellation, bounded semantic journal replay, the current dispatched call and an active matching writer lease. Concurrent consumers cannot both succeed. Consumption means authority spent, not proof that a tool effect happened; acknowledgement loss or a crash must not authorize a retry.

This is a storage/contract prerequisite only. Runtime approval suspension, authenticated operator UI/API, digest construction and approved write-tool execution are not wired yet; side-effecting tool extensions remain rejected. The host must authenticate decision actors and supply trusted time/digests. Stored digests do not prove equivalence to redacted journal arguments, and exact resource-scope leases are not hierarchical path locks. No Linear issue is marked complete. Domain-sensitive audit requirements remain in PRD section9.1.1: objective evidence anchors correctness, semantic reviews remain advisory, and explicit user feedback supersedes subjective judge contributions without double-counting an attempt.

Approval-ledger checkpoint verification: make check (including full native race suite), make build and Linux amd64 cross-build passed. Tests cover exact binding, expiry and clock bounds, cross-connection single-use and revocation races, restart persistence, cancellation/lease fencing, forged prior history, bounded replay, SQL rollback and commit failure returning zero authority. Existing evaluation, feedback-revision and API tests pass. These tests do not qualify real approved tool effects, operator authentication or process-crash dispatch recovery; the full MVP remains incomplete.

DAR-17/DAR-20 scoped execution bridge: the runtime now optionally supplies task/session/turn/attempt identities to a scoped tool executor only after ToolStarted commits. Existing executors remain supported. The schema boundary snapshots arguments and inherited policy, denies unknown/denied/invalid proposals, and binds exact argument/schema/policy SHA256 digests before asking a trusted authority. Writes and Ask require scoped authority; Allow read-only behavior is retained. Handler invocation is one-shot, late callbacks are rejected, and authority output cannot replace the handler's result. Errors, panics, invalid output and cancellation after invocation conservatively report uncertain effects without retry.

The internal durable tool gate accepts a cooperative, authenticated-host review callback with a one-minute deadline. It records the decision, acquires an exact-scope writer lease and consumes approval before dispatch. It renews ownership during execution, cancels on observed lease loss, joins the handler and renewal loop before releasing ownership, and retains spent approval after failure. This connects real runtime execution to the approval ledger, but is not yet exposed through application/SDK extension registration or an operator CLI/API. Existing application extensions still reject side-effecting tools. Trusted callbacks are not sandboxed, must honor cancellation and must join spawned work; digest-only review metadata is not a human-readable effect preview. General crash reconciliation, hierarchical resource locking and full operator integration remain required. No Linear issue is marked complete.

Scoped-execution checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed. A real runtime/executor/gate fixture writes a temporary artifact only after durable consumption and writer ownership, then verifies the paired tool result and completed replay. Tests cover dispatch persistence failures, inherited denial/schema rejection, exact digest binding, duplicate/late invocation, ambiguous-consumption errors, cancellation retaining ownership until handler completion, heartbeat lease loss and final ownership recheck for short handlers. Operator identity is a test fixture, not production authentication; real operator interfaces, process-crash effect reconciliation and broader MVP qualification remain unverified.

DAR-17/DAR-20 application and SDK reviewed tools: ConfigOptions.ApprovalReviewer explicitly enables immutable reviewed extensions, preserving effective Ask and inherited denials. Even Allow writes require approval per call; nil reviewer retains the prior read-only registration behavior. The operator callback receives isolated exact argument bytes, description, identity, scope, digests and expiry; preview mutation cannot change handler arguments. Preview fields are excluded from JSON serialization, and the gate checks their digest before opening a pending approval. Only digest metadata and operator decisions enter the approval ledger. Application wiring rejects configured credentials in durable binding identities or returned actor attribution rather than persisting or silently rewriting them.

Explicit/automatic local execution and both SDK streaming paths use the durable approval/lease boundary. Custom tools and reviewer authority are stripped from delegated children, and active custom tools still cannot enter or execute from durable submissions without a durable implementation identity. The host must authenticate the operator, safely render untrusted arguments and honor the cooperative review deadline; raw previews may contain secrets. CLI/API approval controls, inspection endpoints, interrupted-effect reconciliation, hierarchical resource locks and production operator authentication remain unfinished. No Linear issue is marked complete.

Reviewed-SDK checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed. SDK fixtures write a temporary artifact only after consumed approval, verify exact preview hashes and mutation isolation, and deny canceled/negative reviews and secret-bearing metadata. Application tests cover explicit/automatic routing, both streams, completed replay, consumed records, child catalog and adversarial-call isolation, and submission refusal without provider/reviewer/handler calls or ledger changes. These establish the tested embedding interface, not a finished operator UI, production filesystem sandbox or full MVP qualification.

DAR-17/DAR-40 approval inspection: CLI approvals list/show, SDK InspectApproval/ListApprovals, and authenticated daemon GET task-approval list/detail expose validated metadata without dispatch or decisions. Listing uses the existing task/tool-call index and a bounded1–100-record keyset page; each page checks all selected metadata/body bindings, including lookahead, before returning any records. Public validation rejects null lists, unordered/duplicate call IDs, duplicate approval IDs, wrong task/query bindings and invalid cursors. Lexical call-ID pagination is not a frozen multi-page snapshot. Reads do not initialize/migrate missing or old databases; metadata includes actor/scope and should be treated as private, while raw arguments and lease capabilities are absent.

API reads use their own two-request capacity and a cooperative five-second deadline, independent of task execution and cancellation controls. CLI parsing rejects duplicate/unknown flags; all adapters return sanitized failures and no partial records on invalid data. Inspecting pending/consumed state never approves, revokes or repeats a tool effect. Built-in approval decision UI/API and interrupted-effect recovery remain unfinished; no Linear status changed.

Approval-inspection checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed. Real CLI/SDK/API-to-SQLite fixtures verify metadata-only inspection, task binding, pending/consumed preservation and missing-storage noncreation; API tests cover authentication, origin denial, strict query parsing, independent capacity and invalid service responses. Storage tests cover cursor progression, intervening insertion, corruption including lookahead and empty call IDs, cancellation, and read-only nonmutation. These tests qualify inspection, not external approval decisions, effect recovery or the full MVP.

DAR-17 external approval controls: exact-bound Command decisions are now accepted by the SDK, authenticated POST task-approval decision endpoint and approval-decision CLI. The complete expected request is compared inside the writer transaction before idempotency; actors and times come from the trusted control layer rather than JSON. Exact ID/action/actor retries retain the first timestamp and return current state, including consumed/revoked, without restoring authority. Fresh changes require a live, uncanceled, unexpired call. Controls use an existing-schema15 WAL opener which cannot create or migrate databases. Strict16KiB command parsing rejects duplicate/unknown/case-aliased/missing/null keys; credentials in durable identity/actor fields are rejected by application wiring.

SDK ApprovalPresenter is exclusive with the synchronous reviewer. It displays an isolated exact preview, then the runtime polls durable decisions under its one-minute deadline. Only approved state proceeds to one-use lease-fenced consumption; denial, revocation, cancellation, expiry or prior consumption never dispatch. Fresh wall-clock checks before handler invocation reject an observably expired approval window or initial lease after database waits. The CLI attributes the current OS user; api_operator represents shared bearer authentication, not per-person identity. The host still owns safe preview rendering/authentication, cooperative callbacks and consistent scope mapping. These controls neither register CLI write tools nor resume crashed effects; interrupted-effect recovery and full MVP qualification remain unfinished. No Linear issue is marked complete.

External-decision checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed. Separate SDK control/runtime clients prove a waiting task executes once after approval, never after denial, and rejects stale bindings/changed actors while exact consumed retries remain inert. CLI tests cover OS-attributed decisions, unchanged retry timestamps, revocation, bounded input and canceled pipes. Real API/SQLite tests verify fixed-principal decision persistence and idempotency without journal execution; parser and storage tests cover malformed commands, cancellation/expiry, races, current-schema-only opening and unsupported/missing database nonmutation. Gate tests cover presentation failures, terminal decision states, cancellation and dispatch-time expiry boundaries. These do not qualify a production approval UI, hostile in-process handlers or interrupted-effect recovery.

DAR-40/DAR-43 interrupted-write observations: ApprovalExecutionStatus correlates the approval ledger, semantically validated task replay, paired ToolCompleted report (when present), and scope-wide unreleased writer in one read-only transaction. CLI approvals execution, SDK ApprovalExecutionStatus and authenticated GET task-approval execution expose this versioned snapshot without raw arguments, results or lease capabilities. Open/completed call state and none/live/expired writer observations are not process-health or retry authority. RecordedEffect is a tool report, not independent artifact proof. Reads neither create/migrate storage nor release ownership; general interrupted-effect reconciliation remains unfinished.

New real subprocess fixtures SIGKILL the owned runtime after durable consumption, either before a handler effect or after a synced temporary artifact but before completion persistence. After joining the killed process, a fresh database connection sees spent approval, an open uncertain call and retained writer ownership in both cases. Exact artifact absence/presence distinguishes the fixture outcomes; the journal alone cannot. Duplicate consumption and writer takeover remain rejected. The expired-writer branch uses an explicitly advanced observation time, not a natural expiry wait. These tests do not qualify power-loss durability, automatic orphan recovery or hostile handlers. No Linear issue is marked complete.

Execution-observation checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed. Contract tests cover invalid state combinations and metadata; storage tests cover paired completion reports, corrupt histories, duplicate writers and nonmutation. Real CLI/API fixtures preserve journals and pending approvals; SDK integration observes pending review, consumed execution with live ownership, then completed confirmed output with released ownership. Missing storage is not created, and mismatched task bindings return no records. Real SIGKILL fixtures verify the same open/expired observation after both crash boundaries. Hosted CI, production operator UX and general recovery remain unverified.

DAR-39 streaming usage: the native Linear app still shows the endpoint issue open. The HTTP compatibility adapter now accepts stream_options.include_usage only with stream:true, strictly rejecting duplicate/unknown/case-aliased options and nonboolean include_usage. When requested, regular chunks carry usage:null, followed after durable service success by an empty-choices usage chunk and DONE. This shape was verified against https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create. Explicit opt-out, empty/null options and omitted options retain the prior no-usage stream. Missing/invalid/overflowing usage produces sanitized usage_unavailable before finish/DONE rather than invented zero; execution may already be durably complete and must not be repeated solely because metadata is missing.

Accounting scope is explicitly successful-task-model-turns in a response header and README: the existing runtime sums parent-model turns in the successful task, excluding failed fallback attempts, delegated children and auxiliary calls. These are not whole-request billing totals. OpenAI SSE parsing now requires explicit nonnegative integer counts, validates optional total consistency and overflow, and rejects duplicate/case-aliased accounting fields. Unrelated detail extensions remain supported. Ollama omitted counts remain unknown; partial/null/invalid counts are rejected and explicit zero remains valid. Full parameter/tool-call passthrough, whole-request billing aggregation and production-client/provider qualification remain open; no Linear issue was marked complete.

Streaming-usage checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed. Real HTTP→application→Ollama fixtures execute two parent turns and one read-only tool, prove live content before provider completion and independently read the completed journal before final metadata. Known counts sum to8/10/18; an omitted first-turn count yields unknown despite known second-turn counts. The built-in OpenAI-compatible provider client now round-trips through the endpoint with usage and DONE ordering intact. Boundary tests cover exact zero/max values, malformed options, failures/cancellation paths inherited from streaming, and final-usage write/flush/short-write/panic failures without DONE. Provider fixtures cover absent, partial, null, negative, overflowing, conflicting, duplicate and case-aliased counts. Production third-party compatibility and hosted CI remain unverified.

PRD6.1 context-engine estimation component: SDK ConfigOptions.ContextEstimator installs a trusted local measurement adapter, through the public providers.ContextEstimator contract. Automatic routing measures each eligible candidate's actual model under a shared cooperative three-second batch, denying unmeasurable/oversized candidates without excluding viable alternatives. The runtime remeasures every actual turn and steering candidate, including growing paired tool history and delegated tasks. Unknown model windows reject configured estimators rather than silently disabling the measurement boundary. The effective estimate is max(built-in serialized-byte reserve, custom); this is a conservative admission supplement, not a way to increase context capacity.

Custom inputs are isolated JSON snapshots bounded to4MiB with UTF-8 validation and bounded preflight traversal. Panic/error/cancellation/negative results produce generic failure without callback diagnostics. Each callback has a cooperative maximum three seconds, never an abandoned goroutine. Nil preserves the previous built-in measurement behavior. The host must keep computation local, honor cancellation and concurrency, and protect task data; in-process Go callbacks are not sandboxed. Estimators are process-local, not serialized with queued submissions. Auxiliary audits/summaries and canonical compaction remain built-in. Replaceable context assembly, validated automatic semantic compaction and the complete ContextEngine interface remain unfinished; no Linear issue is marked complete.

Context-estimator checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed. Provider guards test typed nil, cancellation/deadlines, exact serialized-size boundaries, malformed/oversized/invalid-UTF8 input, sanitized failures and nested ownership isolation. Runtime tests cover preturn denial, growing paired tool history, steering admission and unknown-window rejection before task creation. SDK tests execute real multi-turn tools, reject oversized/unmeasurable candidates while selecting a viable alternate, and prove mutated callback inputs cannot alter model requests. Delegation fixtures verify both parent and child measurement with scoped child input only, and no child inference after measurement denial. These fixtures do not qualify third-party tokenizer accuracy or the unfinished full context engine.

PRD6.1 auxiliary context-estimator integration: advisory Reviewer and session Summarizer now accept the same optional ContextEstimator as task execution. Application audit and summary services pass the configured adapter their assembled, redacted model requests only after persisting the auxiliary started record. Oversized/error/panic estimates prevent provider inference and follow the existing durable failed-attempt path without modifying the source journal, activating a summary, or changing a completed parent result. Automatic audit failure remains independent from parent execution success. Nil preserves built-in estimation; canonical compaction checkpoint estimates remain deterministic built-in values for portable replay.

The configured auxiliary timeout now encloses estimation and inference together; the estimator's three-second maximum cannot add time beyond that deadline. Canceled/expired contexts preserve cancellation errors and nil contexts reject safely. Estimator snapshots cannot alter the provider prompt, source history or summary provenance. This closes auxiliary admission coverage for the estimator extension, not the remaining replaceable context assembly or automatic semantic compaction requirements. No Linear issue is marked complete.

Auxiliary-estimator checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed. Review/summary unit tests verify assembled model prompts, conservative floors, mutation isolation, nil compatibility, deadline sharing and no dispatch after estimator error/panic/oversize. Real application fixtures confirm redacted estimator input, durable started-before-estimate ordering, generic failed-attempt persistence, zero auxiliary provider calls after denial and unchanged source journals. An automatic-review fixture proves the parent answer remains successfully completed while its denied audit is recorded separately. Production estimator accuracy, complete ContextEngine behavior and broader MVP qualification remain open.

DAR-35 skill rollback correctness: inspection found that scanning historical transitions by current version ID could reuse an already-undone activation after an older version was reactivated. Rollback now replays the durable activation timeline as an undo stack and reverses each activation at most once. The initial active version cannot be rolled back to an empty/unvalidated target. New activations after a rollback form a new valid branch; immutable version files remain available.

All file-store catalog reads now validate transition continuity, known validated targets, bounded UTC timestamps, exact reversal of the latest remaining activation and agreement with the active pointer. Corrupt or previously inconsistent histories fail closed without migration, repair or file modification; the schema remains1. This does not add automatic regression detection, skill drafting or activation. Expected-version checks remain version-based, not activation-epoch fencing; a future automatic detector must distinguish reactivation epochs. No Linear issue is marked complete.

Skill-rollback checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed. Public store tests reproduce repeated-version activation, restart between rollbacks, stale expected versions, new branches and exhausted undo with unchanged catalog bytes. Corruption tests reject discontinuous/missing histories, unknown/unvalidated targets, forged or reused rollback transitions, mismatched final state and invalid timestamps through discovery, loading, history inspection and read-only open. Draft and rollback also reject those histories without changing catalog/version files. Never-activated drafts and valid timestamp boundaries remain readable. Automatic skill learning and production regression qualification remain open.

DAR-35 revision-bound skill controls: the optional skills.RevisionStore interface adds read-only ActivationState and revision-checked ActivateAt/RollbackAt. SHA-256 revisions cover the validated per-key activation timeline, so A→B→A cannot satisfy an earlier observation even though its active version matches. Draft-only changes preserve the revision. Candidate validation remains outside the mutation lock; the full state comparison occurs inside it after validation. Existing version-only Store callers remain compatible, catalog schema remains1, and no automatic learning controller is enabled. Revision tokens are concurrency preconditions, not authorization or proof of semantic correctness.

CLI skills state exposes this metadata without workflow bodies or initializing storage; rollback optionally accepts --expected-revision alongside --expected-version. Malformed, duplicate and command-inappropriate flags reject before opening storage. Tests cover same-version stale observations, a second store changing the timeline during validation, fresh controls across restart, draft stability, exhausted undo, invalid bindings, read-only denial, nil contexts and the automatic-mutation kill switch. Automatic skill drafting and regression detection remain unfinished; no Linear issue is marked complete.

Revision-control checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed; the final nil-context regression also passed the focused skills race suite. Hosted CI and production automatic-learning behavior remain unverified.

DAR-35 deterministic regression rollback: native Linear inspection confirms the issue remains open. FileStore.RevalidateAndRollback now lets a trusted host revalidate a pinned activation and automatically undo it when a deterministic validator returns valid failing evidence. A cooperative three-second deadline covers inspection, validation and commit; callback errors/panics/cancellation are not treated as failures of the skill. Both passed and failed results recheck the full activation revision and automatic-mutation switch after validation. Read-only stores reject before invoking the callback. Rollback evidence is persisted atomically with the transition; passing checks do not create history. The returned state is the checked observation, not a promise that no later concurrent activation occurs.

History now exposes bounded activation metadata and regression evidence, never workflow bodies. Optional evidence leaves legacy activation serialization and revision hashes unchanged; catalog schema remains1. Invalid or misplaced regression evidence fails catalog validation without repair. This implements host-invoked deterministic revalidation rollback, not daemon-triggered monitoring, statistical post-activation outcome regression detection, automatic drafting, or the complete DAR-35 acceptance scope. Trusted Go validators remain unsandboxed and must not execute unapproved side effects. No issue is marked complete.

Deterministic-regression checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed. Tests verify restart-preserved evidence, immutable validator inputs, caller-owned history snapshots, passed-check nonmutation, ABA changes during both passing/failing validation, kill-switch changes, invalid/error/panic/canceled validation, read-only denial, exhausted undo, and rejection of corrupt history proofs without modification. Production validator quality, daemon monitoring and hosted CI remain unverified.

DAR-34 host-driven workflow drafting: FileStore.DraftFromWorkflows admits2–20 successful examples in one domain with distinct task/session IDs. Trusted evaluation checks use Resolve(false), preserving deterministic/tool/user precedence and excluding judge-only success. Bounded UTF-8 input snapshots isolate callback mutation; winning evidence IDs and session IDs are canonicalized independently and replace generator-proposed provenance. Output is bounded, validated structurally, and stored as an inactive draft under the automatic-change switch. Optional SourceEvidence preserves old draft serialization when absent and participates in loaded-version validation. No model-only judgment activates a skill.

The generator has a cooperative30-second whole-operation deadline, with sanitized callback errors/panics and cancellation checked before persistence. Hosts must supply real completed-work evidence, redact examples, and route/budget any model-backed generator; the callback is trusted Go code, not a sandbox. This adds the automatic drafting controller but not journal-based workflow discovery, a built-in routed model generator, daemon scheduling, deduplicated retry identity, or automatic validation/activation. Inactive draft retries may create separate versions. Those remain required for the full DAR-34 acceptance scope; no issue is marked complete.

Workflow-drafting checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed; the tightened30-second deadline assertion also passed the focused skills race suite. Tests cover inactive-only persistence and restart, accepted evidence sources and rejected objective failures/judge-only claims, canonical trusted provenance despite callback mutation, caller ownership, scope/count/size/UTF-8 limits, callback failures/cancellation, read-only and kill-switch denial. These tests do not qualify a real model generator, automatic source attribution, or hosted CI.

DAR-34 built-in model drafting: ModelGenerator now implements the workflow generator using one tools-free provider call with context estimation, finite estimated-cost admission and a shared cooperative timeout of at most30seconds. The stable prompt treats examples as untrusted data and never claims validation. A strict64KiB response parser requires the exact workflow-only schema, rejects duplicate/unknown/case-aliased fields and replaces identity/provenance with host metadata. Stream errors, tool proposals, missing or non-stop completion, post-completion output and invalid accounting reject the proposal. No retries or activation occur.

GenerateDetailed preserves the chosen model, optional copied token usage and whole-operation elapsed time; unknown usage is not fabricated as zero. The draft-only interface remains compatible with DraftFromWorkflows. The host must admit the provider through policy, reserve resources, redact inputs/outputs and persist attempt/accounting records. Automatic daemon route selection, journal workflow discovery, generation-attempt persistence and scheduling remain unfinished, so DAR-34 remains open. A loopback HTTP fixture verifies the Ollama adapter produces an inactive persisted draft; this is not production-model quality or complete egress qualification.

Model-generator checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed; final detailed-accounting tests passed the focused race suite. Parser tests cover every required field, duplicates and escaped aliases, unknown/provenance fields, null/type/UTF-8/trailing-content errors and exact64KiB boundaries. Provider fixtures cover tools-free dispatch, conservative context denial, cost/config rejection, sticky stream failures, tools/aborts/malformed output, usage cloning/unknown/zero/overflow and zeroed failure results. File-store and loopback HTTP fixtures preserve inactive status and host provenance. Real model quality, durable generation attempts, daemon orchestration and hosted CI remain unverified.

DAR-34 durable generation lifecycle: schema16 adds scoped skill generation attempts, with validated started/failed/drafted states and immutable model/input/provenance bindings. Begin is a single-winner execution claim, not an idempotent dispatch permit. Finish permits exact terminal retries but rejects changed inputs and terminal overwrites. Reads cross-check bounded payloads against indexed identity/scope/name/status; list pagination is scoped and bounded. Existing schema15 data migrates transactionally; old-version downgrade test fixtures now explicitly remove the new table before replaying migrations.

ModelGenerator.GenerateRecorded connects this recorder to actual generation: persist started before estimation/inference, persist proposal or sanitized failure before returning, and use a bounded uncanceled terminal write after cancellation. Recorder callbacks receive isolated snapshots. A failed terminal write returns the started observation without a proposal; the caller must inspect durable state rather than rerun. InputDigest binds admitted redacted examples and generation parameters, but source authenticity, policy and estimator identity remain trusted host responsibilities. Drafted records are not published/activated skill versions. Journal-based evidence selection, idempotent publication, daemon scheduling, operator inspection surfaces and full DAR-34 acceptance remain unfinished; no issue is marked complete.

Generation-lifecycle checkpoint verification: final make check (full native race suite), make build and Linux amd64 cross-build passed. Migration15→16 preserves existing task/approval data and supports repeat reopening; an initial test-only local-time/UTC comparison was corrected before the final gate. Real SQLite tests cover single-winner begin/finish races, restart, exact terminal retries, immutable bindings, scoped pagination, read-only inspection and corrupted payload/index rejection. Recorded-inference integration observes started before dispatch, saved drafted/failed output afterward, no redispatch for duplicate IDs and cancellation cleanup. Failure injection verifies begin errors/panics prevent inference and finish failures return uncertainty without a proposal. These are not power-loss qualification, automatic orphan recovery or completed skill publication; hosted CI remains unverified.

DAR-34 verified task-history selection: SkillWorkflowSources now derives2–20 same-domain examples from distinct completed task/session journals and their current accepted final-attempt evaluations in a single read-only transaction. It validates semantic replay, final model/provider/domain/profile binding, bounded evaluation histories, no pending/uncertain effects and deterministic/tool/user evidence precedence without judge-only success. Omitted explicit-route domain/profile use the established feedback defaults general/default. Observed non-system messages preserve paired tool history; canonical snapshot/evaluation digests bind provenance. Selection is a coherent observation, not a promise that feedback cannot subsequently change.

Service.GenerateSkillDraft and its Go SDK adapter connect selected history to policy-bound providers and durable generation. They enforce configured skill scope/auto-draft, model/context/cost metadata, shared execution/resource admission, source privacy and local-only rules. Source text and candidate output are redacted before model dispatch or persistence; secret-bearing identity fields reject rather than rewriting attribution. Started precedes estimator/inference; failures and cancellation are recorded with bounded cleanup. Only an inactive proposal is stored, with no skill-root creation or injected-store mutation. Automatic task discovery, idempotent publication, activation validation, native management endpoints and background scheduling remain unfinished; no Linear issue is marked complete.

Verified-source generation checkpoint verification: final make check (full native race suite), make build and Linux amd64 cross-build passed. Storage tests cover current feedback rejection/reacceptance, missing/judge-only/failed/wrong-attempt evidence, paired tool observations and byte-for-byte journal/evaluation nonmutation. Application HTTP fixtures verify started-before-dispatch, redacted input/output, saved-return equality, duplicate-ID denial, source integrity, no skill-root creation, policy denials, invalid generation, cancellation cleanup and estimator rejection. SDK fixtures create tasks, record feedback and generate a durable proposal with usage/provenance intact. These do not establish real-model workflow quality, automatic source selection, continuous feedback freshness, production scheduling or full DAR-34 completion; hosted CI remains unverified.

DAR-34/DAR-40 generation inspection: application adapters, Go SDK, CLI skill-generations list/show and authenticated GET /v1/skills/generations endpoints now inspect saved attempts without creating/migrating storage or dispatching work. Lists project validated metadata only, excluding draft bodies and source session/evidence IDs. Explicit detail reads include the saved proposal and enforce requested scope/ID binding. Pages use bounded1–100 lexical cursors and are live observations, not frozen snapshots. HTTP uses existing authentication, origin policy and shared capacity; callbacks and response records are validated before output. The daemon routes these reads through the same application adapters.

Inspection exposes uncertainty, not retry or activation authority. Full proposal exports remain sensitive. Generation triggering through native HTTP/CLI, idempotent publication, activation and background learning remain unfinished; no Linear issue is marked complete.

Generation-inspection checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed. Real-store CLI/SDK/HTTP tests verify metadata-only lists, explicit proposal reads, scope binding, cursor bounds, missing/legacy database noncreation/nonmigration, corruption rejection and read-only byte stability. HTTP tests cover auth/origin/capacity, oversized queries rejected before parsing, duplicate/unknown parameters, invalid callback results and suppression of results after cancellation. Summary contracts reject inconsistent state metadata and exclude source IDs/draft text. Daemon callback wiring is compile-verified; production operator UX, hosted CI and the remaining learning lifecycle are not qualified.

DAR-34 idempotent proposal publication: FileStore.PublishGeneration commits an inactive immutable version and a receipt bound to the canonical complete generation attempt. Exact retries return the same version without rewriting catalog/version files, including after activation changes; conflicting IDs reject. First publication upgrades the file catalog to schema 2 to prevent older writers dropping receipts. Ordinary legacy catalogs remain schema 1 until publication; SQLite remains schema 16. Invalid receipts fail closed without repair.

Service/SDK PublishSkillGeneration inspects the actual saved drafted attempt in the configured scope, enforces enabled/auto-draft/root settings and rejects injected retrieval stores. Current raw or JSON-escaped credential collisions reject before opening the skill root. Publication neither rewrites SQLite nor revalidates source-feedback freshness, workflow quality or activation. Pre-catalog failures can leave orphan version files; no automatic cleanup, cross-store transaction or power-loss qualification is claimed. Native publication commands/endpoints and background learning remain unfinished; no Linear issue is marked complete.

Publication checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed. An initial vet failure in the restart fixture was corrected by constructing a fresh Service rather than copying its mutex. Tests cover concurrent/restarted exact retries, conflicting bindings, unchanged activation revisions, catalog upgrades, corrupt receipts, scoped saved-proposal publication, credential guards and rejection without storage creation. Evaluation/routing tests also pass, including meaningful-output audit instructions, creative user-preference priority and objective-validity precedence. Hosted CI, real-model audit quality and production learning remain unverified.

DAR-34/DAR-38 CLI learning controls: skill-generations generate now takes explicit configuration, stable attempt ID, configured model, skill name and 2–20 distinct source task IDs; publish takes configuration and a saved attempt ID. Scope comes exclusively from configuration. Strict command-specific flags and finite cost bounds reject before configuration/storage. Both use signal-aware bounded contexts and the existing service policies, emit structured JSON only on success, and never automatically retry, validate or activate. Inspection remains read-only. Native Linear inspection still shows drafting open; no issue is marked complete.

Focused tests verify real loopback provider tasks with explicit feedback through generation/publication, durable equality, unchanged source history, inactive publication and exact retry. Erroring/panicking stdout leaves committed generation/publication inspectable without redispatch. A subprocess SIGINT during provider execution produces a saved canceled failure, no proposal/catalog, and no redispatch on ID reuse. The interrupt fixture initially omitted the required chat capability; adding it restored intended admission. These are local fixture qualifications, not real-model workflow validation, background task discovery, HTTP mutation support or full learning completion. Configuration file loading remains synchronous like existing CLI commands.

CLI learning checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed. Built binary help exposes both commands. Independent review found no blocking issue in argument admission, secret-safe diagnostics or mutation boundaries. Hosted CI, production provider quality, native HTTP learning mutations and full automatic learning remain unverified or unfinished.

DAR-34/DAR-40 daemon learning mutations: authenticated POST /v1/skills/generations accepts a versioned strict request with stable ID, configured model alias, skill name, distinct task IDs and optional cost ceiling. POST /v1/skills/generations/{id}/publish copies the saved proposal into an inactive version. Daemon callbacks bind scope to configuration and use the same application admission/storage policies. Both share task capacity, reject query/browser-origin inputs and oversized/ambiguous JSON, bound cooperative execution, suppress canceled/invalid results and sanitize callback errors/panics. Generic failures do not authorize redispatch. The model in a saved attempt is its underlying provider name, not necessarily its configured alias.

Version.Validate adds bounded immutable-record validation without pretending to establish workflow quality, permissions or activation. API tests exercise malformed fields, body limits, auth/origin/media/capacity, cancellation, unavailable callbacks and response validation. A real application/loopback-provider integration creates accepted source tasks, generates a durable proposal, denies duplicate dispatch and missing source tasks, and verifies repeat-safe inactive publication. Background discovery, automatic activation, production workflow quality and full learning acceptance remain open; no Linear issue is marked complete.

HTTP learning checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed. Immutable-version tests cover malformed identifiers, timestamp boundaries, UTF-8, collection/raw/serialized size caps and nonmutation without requiring activation. Daemon callback wiring is compile-verified and application adapters are exercised in HTTP fixtures; a deployed daemon/operator acceptance run and hosted CI remain unverified.

DAR-35 validated activation integration: application/SDK SkillActivationState reads the configured file catalog without initialization; ActivateSkillVersion requires enabled automatic activation policy, an exact scope/revision and a trusted deterministic validator. It rejects injected retrieval stores, preflights existing state/candidate read-only, checks credentials without rewriting evidence and binds the core-reloaded candidate to the inspected bytes. Validation is host Go code, not sandboxed or model-authorized; callbacks must be read-only, retry-safe and cooperative. No remote self-declared proof endpoint or background scheduler is added.

Core Activate/ActivateAt now reject nil/canceled/read-only/disabled/stale admissions before validators, validate immutable record structure, sanitize callback errors/panics and bound cooperative work to three seconds. Final revision comparison remains inside the commit lock, preserving protection from concurrent activation/rollback. Successful callback evidence must still be deterministic and passed; syntax validation alone is not workflow acceptance. Credentials are observed around validation to reject proof/candidate collisions, not to claim an atomic credential-rotation transaction. Existing catalog schemas and SQLite schema16 are unchanged.

Activation integration checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed. Tests cover published proposal activation, restart/discovery and actual subsequent provider context containing the skill; stale state invokes no validator. Denials include missing stores, policy/scope/injected stores, nil/canceled calls, judge-only/failed proofs, callback errors/panics, candidate secrets and both new/previous credentials during rotation. Core tests cover the cooperative deadline, kill-switch changes and immutable candidate files. SDK forwarding and read-only inspection with activation disabled also pass. Trusted validator semantics, automatic scheduling, production learning quality and hosted CI remain unqualified; no Linear issue is marked complete.

DAR-35 application regression recovery: Service/SDK RevalidateSkillVersion now connect the configured file catalog to deterministic revalidation and rollback. Rollback policy is independent of new activation policy. Read-only admission pins the current active version/revision before writable access; the shared activation/regression callback guard preserves candidate identity and all observed credential snapshots. Passing checks do not mutate; errors, panic, cancellation and non-deterministic evidence never trigger rollback. Returned State is the checked observation, so callers must inspect current state afterward. Core regression now rejects malformed/oversized immutable records before callbacks. The restored predecessor was previously validated, not freshly revalidated by this operation.

Focused tests cover two activated versions, passing byte-stability, deterministic failure/proof persistence across restart, prior-version restoration, stale/no-callback rejection, scope/config/injected/context guards and new/old credential proof rejection. SDK tests verify forwarding/reopen and rollback with new activations disabled. This remains a host-invoked controller, not continuous monitoring or statistical outcome detection. No Linear issue is marked complete.

Regression integration checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed. Legacy integrity-valid but structurally malformed active-version fixtures are rejected before validators without catalog mutation. Existing activation tests remain green after extracting the shared callback guard. Independent review found no blocking integration issue; deployed monitoring, real validator quality, statistical regression acceptance and hosted CI remain unverified.

DAR-34 durable workflow discovery: DiscoverSkillWorkflows scans a bounded1–20-task lexical window in a single read transaction and returns only accepted candidate metadata/provenance. Current completed replay, privacy, final-attempt identity and deterministic/tool/user feedback qualify candidates; judge-only/missing/rejected evidence does not. Valid unsupported learning labels and source-size/step limits are ineligible rather than corrupt; malformed persisted history aborts without partial results. Full windows advance Next even when no candidates match. The shared per-task extractor preserves explicit generation's same-domain, distinct-session and aggregate-source limits. No schema change or write is introduced.

Application/SDK wrappers require configured enabled drafting, read existing telemetry without opening the skill catalog, validate pages and reject credential-bearing inputs/metadata rather than rewriting identities. Discovery exposes live observations, not a frozen multi-page snapshot or semantic workflow grouping. Generation must still re-read its selected2–20 distinct-session examples under normal admission; no automatic scheduling, inference, publication or activation occurs. No Linear issue is marked complete.

Workflow discovery checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed. Tests cover cursors across ineligible records, current feedback revision, provenance equality, metadata-only payloads, read-only restart/nonmutation, corruption rejection, unsupported labels and oversized valid sources. App integration discovers actual accepted tasks, rejects credential-bearing candidate IDs and verifies no generation record or catalog creation. Page contracts enforce counts/order/cursors and permit empty eligible sets. Existing explicit generation/source tests remain green. Large-catalog production latency, semantic grouping, background scheduling and hosted CI remain unverified.

DAR-34/DAR-38/DAR-40 operator discovery: CLI skill-generations discover and authenticated GET /v1/skills/workflows now expose the configured service's metadata-only scanner. Strict domain/cursor/scan-limit parsing precedes service calls; HTTP query bytes are bounded before parsing and use shared task admission. Both surfaces preserve cancellation and sanitized failures without creating storage or invoking inference. Scope is a destination skill catalog setting, not source-project or tenant ownership: source discovery is database-wide single-operator inspection.

Lifecycle review identifies the next automation requirements: persist selection/scan epochs or a change sequence so later feedback and lower-sorting new tasks are not missed; bind selections to source/evaluation digests and derive stable attempt identities to avoid duplicate billing; establish repeated-procedure grouping beyond shared domain. A timer alone would not satisfy those requirements. Background selection/scheduling is not implemented by these adapters, and no Linear issue is marked complete.

Operator discovery checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed. CLI and API integrations now take discovered task IDs through real fixture generation and inactive publication. Boundary tests cover strict parameters, authentication/origin/capacity, cancellation, invalid service pages, read-only storage and output failures; daemon discovery callback is wired to the shared application service. Hosted CI and deployed operator qualification remain unverified.

DAR-34 durable learning selections: version1 WorkflowSelection binds a trusted grouping algorithm/group, destination key, configured model, opaque policy digest and ordered2–20 same-domain/distinct-session source metadata. SHA-256 identity excludes creation time, so repeat saves preserve the first committed record. Constructors own/sort inputs; validation rejects changed material under an old ID. A golden identity fixture protects version1 serialization. Group labels and hashes do not establish semantic repetition, source ownership, freshness or execution authority.

SQLite schema17 adds immutable scope-indexed workflow_selections with bounded payload/index validation and scoped reads/pagination. Save is single-winner and returns actual committed metadata. Existing generation data remains unchanged; migration gates, approval control, metrics and downgrade/future fixtures are updated. This is the ledger prerequisite, not a scheduler, generation claim, budget reservation or automatic grouping implementation; no Linear issue is marked complete.

Selection ledger checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed. Tests cover canonical identity, immutable-field tampering, concurrent saves with different timestamps, read-only inspection/restart, scope/pagination bounds, malformed indexed bodies, oversized records and no partial list output. Migration16→17 preserves existing tasks/generation and supports repeat reopening; read-only16 inspection does not migrate. Independent review found no blocking identity issue. Generation binding, durable scan epochs, automatic grouping/budgets, production orchestration and hosted CI remain unverified or unfinished.

DAR-34 selection-bound generation: Service/SDK PlanWorkflowSelection now derives source metadata from accepted durable histories and saves a content-addressed selection without inference or opening the skill root. GenerateSkillSelection rechecks policy and exact source metadata against the same snapshot consumed by the generator, then uses selection.ID as the existing single-winner generation attempt ID. Changed accepted feedback revisions reject, not just negative feedback. A dispatched ID cannot authorize another inference; explicit lower-level generation with a fresh caller ID remains a separate operation.

The policy fingerprint conservatively covers all validated configuration plus model alias, explicit cost ceiling and contract version. Unrelated configuration changes also invalidate plans. Credentials are not fingerprinted; injected provider/context estimator implementations lack durable identity and remain trusted host code. Group/algorithm labels are operator attribution, not semantic repetition or source-tenant ownership. Freshness is snapshot-based rather than continuous across inference. Planning/publication/activation remain separate; durable scan epochs, automatic grouping, scheduler budgets/cooldowns and full DAR-34 acceptance are still open. No Linear issue is marked complete.

Generation redaction now retains credentials observed during planning/policy inspection, the actual provider-key lookup, provider construction and inference completion. Previously observed values remain in the cumulative set after rotation; metadata collisions reject while draft text is redacted and revalidated. These boundary observations are not an atomic credential-rotation transaction. Independent review caught a policy helper dropping its locally observed keys; the caller now retains those observations as well. Native Linear inspection confirms DAR-34 remains Todo pending the full automatic-drafting acceptance requirements.

Selection-generation checkpoint verification: final make check (full native race suite), make build and Linux amd64 cross-build passed. App/SDK tests cover saved-return equality, repeat planning across client restart, zero-inference planning, success and failed-attempt deduplication, unchanged skill roots, accepted feedback revision invalidation, policy drift before claim, forged but structurally valid source metadata and invalid admissions without ledger writes. Rotation tests cover transient actual/helper-only keys, source-prompt redaction, post-inference output redaction and invalid redacted tags. Independent final review found no remaining blocking issue in this diff. Production learning quality, autonomous scheduling, deployed operator qualification and hosted CI remain unverified.

DAR-34 observed-procedure grouping: WorkflowProcedure and WorkflowGroup now represent exact successful tool-name sequences, domain and profile under the versioned observed_tools_v1 rule. Group identity excludes source membership; selections still bind precise source/evaluation observations. Grouping preserves ordering and repeated calls, selects the lexical first task for each repeated session, and omits singleton/no-tool groups. It never treats shared domain or imported conversation tool claims as proof of execution.

Read-only telemetry extraction derives tool sequences from actual validated dispatch/completion events, rejecting failed/uncertain and mixed-domain/profile trajectories. GroupSkillWorkflows exposes bounded groups through the application/SDK. PlanGroupedWorkflowSelection requires every requested task to belong to exactly one distinct-session group, reserves the built-in algorithm identity and saves a selection without inference. Both planning and generation recheck actual procedures inside the same read transaction as source extraction, because the existing snapshot digest omits some tool-event semantics. SQLite schema17 remains unchanged.

This is deterministic candidate grouping, not semantic workflow equivalence or tool implementation identity. Raw arguments/results do not enter group identity; sensitive tool names and metadata still pass credential rejection. The source database remains single-operator, and grouping does not establish project/tenant authorization. Text-only grouping, durable scan epochs, automatic scheduling, aggregate budgets/cooldowns and full learning acceptance remain unfinished. No Linear issue is marked complete.

Observed-grouping checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed. Core fixtures verify golden group identity, order/repetition/profile separation, session deduplication, bounds and defensive slice ownership. Store tests verify actual events versus imported claims, current feedback, failed/uncertain tools, read-only nonmutation and corruption. App/SDK integrations execute real fixture tools, record acceptance, group/plan without inference, generate once and leave the skill root absent. A regression changes a completion failure code while preserving candidate digests: grouped generation rejects before claiming or invoking the model. Independent review found no blocking issue; production semantic quality, autonomous lifecycle, deployed operator qualification and hosted CI remain unverified.

DAR-34 durable scan epochs: schema18 introduces append-only task insertion membership, scan heads and immutable observed pages. A migration backfills membership from existing task IDs; a transactional task-head insertion trigger records future tasks. Each epoch captures an insertion-sequence fence and lexical upper bound, making membership finite even while new lower-sorting IDs arrive. Evidence remains a live observation within each page transaction, not an epoch-wide snapshot.

AdvanceWorkflowScan atomically stores the bounded metadata page and next checkpoint. Scope/name/domain and requested revision/limit bind retries; exact retries return the original saved page, including after later advances. Revision counters remain monotonic across epochs. Completing an epoch allows the next call to restart at the beginning, revisiting changed feedback and later arrivals. Ineligible tasks advance the scanned cursor; corrupt records abort without advancement. Empty catalogs commit an empty completed epoch. Read-only inspection does not migrate or repair.

These are trusted-host storage primitives, not an automatic scheduler or public application/SDK scan interface. No inference, publication or activation is dispatched. Operator-policy/redaction admission adapters, cross-page grouping consumption, aggregate budgets/cooldowns and scan retention remain unfinished. The task catalog is currently append-only; eventual task retention must coordinate insertion membership. A poisoned source can halt a scan and needs explicit operator handling. No Linear issue is marked complete.

Durable-scan checkpoint verification: final make check (full native race suite), make build and Linux amd64 cross-build passed. Tests cover empty epochs, persisted historical retries, concurrent callers, insertion fencing, late feedback, lower-ID arrivals in the next epoch, read-only inspection, bounded records, corrupt head/page/source rejection and contiguous positive integer revision checks. Migration17→18 preserves task/evaluation/selection records, repeat reopening is stable, read-only17 does not migrate, and rolled-back task insertions leave no membership record. Fault injection after page insertion proves a failed head insert/update rolls back both records and permits a single clean retry. Large-catalog scan latency, retention, scheduler integration, deployed operator qualification and hosted CI remain unverified.

DAR-34 guarded scan application integration: Service/SDK AdvanceSkillWorkflowScan now binds scope to configuration and checks cumulative credential observations before metadata persistence. A bounded cooperative storage guard receives an isolated copy of the exact page under the transaction; rejection, panic or cancellation prevents page/head writes. Historical retries re-establish admission without changing records. Returned metadata is checked again after commit; a late rejection can suppress a committed result, so callers must retry the same revision rather than assume no write occurred. Credential observations do not provide an atomic rotation transaction.

SkillWorkflowScan provides read-only current progress inspection, including with auto-draft disabled. Advancement requires enabled drafting and an existing current-schema WAL database. The shared control opener uses SQLite mode=rw and does not initialize or migrate, eliminating the read-only-preflight/general-Open creation race while preserving approval-control behavior. Trusted private filesystem paths remain required; this is not path sandboxing. Neither operation opens the skill catalog or invokes a model. Cross-page grouping consumption, budget/cooldown scheduling, retention and full DAR-34 acceptance remain unfinished; no Linear issue is marked complete.

Guarded-scan integration checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed. Storage guard fixtures cover owned-copy isolation, retained-slice mutation, nil guard, denial, panic, cancellation, cooperative deadline and repeated historical admission without mutation. App tests cover initial/transient guard credentials rejecting before save, denied historical metadata, post-commit response suppression with exact retry, input guards and missing/legacy database noncreation/nonmigration. SDK integration persists pages, reopens a client, replays an old revision and inspects the current head while auto-draft is disabled, without inference or skill-root creation. Existing approval-control tests pass after sharing the opener; independent final review found no blocking issue. Hosted CI, large-catalog behavior and deployed scheduler/operator qualification remain unverified.

DAR-34 aggregate generation admission: optional skills.generation_budget now configures a rolling estimated-cost/attempt ceiling, in-flight capacity and per-skill-name cooldown. The shared application generation path uses a budgeted claim for explicit and saved-selection generation alike. Storage holds the writer transaction across complete scope-history inspection, policy check and single-use insertion, preventing concurrent callers from spending the same remaining capacity. Known budget denials return skills.ErrGenerationBudget with no claim or inference.

Accounting includes earlier unbudgeted records and failures. Unresolved started attempts retain slots regardless of age; terminal outcomes free slots but do not refund the rolling reservation. Exact rational addition of configured floating-point estimates prevents rounding-away reservations; these are not actual provider invoices. Current claim time, not caller-backdated timestamps, governs admission, and future-clock history remains conservatively chargeable. Configuration is disabled by default for manual compatibility; a future automatic scheduler must explicitly require it enabled.

Budget policy is trusted client configuration, not immutable database authority: differently configured clients can intentionally bypass or loosen it. Scope history is currently bounded to1000 records/8MiB with a ten-second check, and overfull/corrupt history fails closed; indexed accounting and retention are still needed for long-lived high-volume use. Scope budget state is derived from existing schema18 records; no schema migration is added. Cross-page grouping consumption, automatic scheduling, durable policy audit snapshots and full DAR-34 acceptance remain open; no Linear issue is marked complete.

Generation-budget checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed. Core tests cover exact cost sums, rolling boundaries, future/backdated clocks, old unresolved attempts, failure charges and cooldowns. Storage tests exercise two-store concurrent claims, earlier unbudgeted history, terminal slot release without cost refund, duplicate IDs and corrupt/oversized history rejection. Application tests prove attempt/cost/cooldown caps prevent a second inference and saved selections use the same scope budget. Layered configuration, disabled-policy validation, zero/day cooldown parsing and redacted display pass. Independent review found no blocking integration issue; actual billing reconciliation, indexed long-lived accounting, automatic scheduling and hosted CI remain unverified.

Orchestrator-audit requirement follow-up: two independent read-only reviews confirmed existing rubric-v2 semantic completion checks, objective nonempty validation, bounded domain-sensitive advisory routing and direct-feedback precedence. README now describes these distinctions explicitly; no runtime behavior or operator configuration was changed. Automatic auditing remains opt-in and applies to successful Service.Run results using an independently configured reviewer, not automatically to delegated children or failed runs. Blank required answers already fail deterministic validation. Current Go output validation parses syntax only; it does not compile or run tests. Supplying separately attributed typed validator/tool metadata to the review prompt and qualifying real-model review quality remain follow-up work. This documentation checkpoint does not complete the broader evaluation MVP or change Linear statuses.

Audit follow-up verification: make check passed (format/LOC, vet, native race suite and build; test cache reused). Targeted application Audit/Feedback/Validity tests also passed with -count=1. No live-provider quality claims or new runtime implementation are made.

Grounded audit execution evidence: AuditTask now projects actual ToolCompleted and EvaluationRecorded metadata into individually citable execution_<sequence> references. Each projection retains turn/attempt identity and the relevant error code, effect or explicit acceptance boolean, while excluding raw payloads. Stored audit references exactly match the review envelope. Collection checks contiguous task/session history through the replayed terminal sequence and fails closed above250 records or64KiB projected metadata. Review instructions distinguish earlier tool work from final validation and explicitly prohibit treating tool completion/nonempty/syntax checks as test success. Conversation fields and tool arguments are now redacted before JSON serialization using the existing structured redaction helper, covering escaped credentials.

This advances the PRD audit requirement and evaluation work without changing fitness precedence, retry authority or review activation defaults. It does not add compiler/test execution, external receipt attestation, automatic auditing of failed tasks/delegated children, or real-model quality qualification. Native Linear was re-inspected in the DarwinRouter workspace; no issue was marked complete.

Grounded-audit checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed. Tests cover metadata bounds, explicit false acceptance, escaped credential redaction, contradictory conversation/tool outcomes, final candidate identity, exact durable references and invented-reference rejection. Independent review identified that session replay alone does not validate evaluation attribution; AuditTask now checks validation turn/attempt against the current observed turn, with wrong-turn/attempt fixtures proving no dispatch or review claim. Final independent review found no remaining blocker. Hosted CI and live-provider audit accuracy remain unverified.

DAR-34 cross-page consumption: schema19 adds a durable consumer head, immutable consumption receipts and epoch-specific workflow buckets. Consumption uses its own revision cursor against already saved discovery pages, refreshes source acceptance and actual paired tool procedures inside one writer transaction, and atomically saves touched buckets with the receipt/head. Exact historical retries are guarded and return the original result without reapplying the page. Changed/ineligible candidates are skipped; malformed source evidence aborts advancement. Core buckets preserve the observed_tools_v1 identity and keep lexical first tasks from up to20 distinct sessions; singletons persist until another session matches. A scan epoch admits at most1000 patterns.

Application/SDK operations expose guarded consumption and read-only receipt/bucket inspection with cumulative credential checks. They require configured scope and existing storage; they neither create the skill root nor invoke models. Draft planning/generation still revalidate bucket source tasks. These operations do not establish source tenancy, semantic equivalence, automatic scheduling or skill activation. Long-history indexing/retention, scheduler budget enforcement and full DAR-34 acceptance remain unfinished.

Consumer integrity review identified that structurally valid bucket rows alone could silently lose or replace accumulated sources. Buckets now carry their latest receipt revision, loaded contents must match that receipt, and catalog membership/latest-touch revisions are re-derived from immutable receipt history. Deletion, valid-looking replacement and rollback to an older valid revision fail closed. Catalog reconstruction scans historical JSON within the ten-second transaction deadline; long-lived/high-volume scans need indexed provenance and retention rather than claiming unbounded throughput.

Cross-page consumption checkpoint verification: make check (full native race suite), make build and Linux amd64 cross-build passed. Tests cover cross-page and epoch grouping, restart and exact retry, concurrent compare-and-swap, current feedback/tool-failure changes, malformed source/ledger state, cloned guard rejection/rollback, bucket provenance corruption and bounded inspection. App/SDK tests cover credential rotation, no inference/root creation, disabled-auto-draft inspection and restart. The schema18-to19 migration preserves source evidence, selections, scan heads/pages and old retry results; read-only/control opens do not migrate legacy storage. Independent final review found no remaining correctness blocker. Hosted CI, long-history performance, scheduled learning and complete Linear issue acceptance remain unverified; no issue is marked complete.

Initial testing priority: the user selected GPT-5.6 Sol as coordinator directing local Ollama workers. docs/initial-hybrid-test.md records the supervised, non-sensitive first-test scope and readiness checks. Official model documentation confirms function calling; local Ollama inventory and48GiB host memory were inspected. OPENAI_API_KEY was absent from this shell; no credential value was read and no cloud inference was attempted. Live interoperability, actual account access, memory fit and latency are not yet qualified.

Scheduler prerequisite checkpoint: disabled-by-default skills.learning configuration now validates intervals, scan size, model metadata/privacy and mandatory generation budgets when enabled. LearningState defines phase/cursor shape for future durable orchestration. No scheduler loop, state persistence or daemon wiring exists yet: enabling this configuration does not schedule work. Further scheduler implementation is deferred in favor of the user's first hybrid test. Existing procedural-learning implementation remains unchanged.

Prerequisite verification: make check passed (format/LOC, vet, full native race suite and build). Configuration layering and invalid cursor/model/privacy/budget cases are covered. These tests do not qualify a live coordinator/worker run or implement scheduled execution.

CLI coordinator preference and first live checks: the operator requested Codex/ChatGPT CLI instead of requiring direct API credentials. Official noninteractive/authentication documentation and installed CLI help were checked. Codex0.153.4 reports ChatGPT login; an ephemeral read-only gpt-5.6-sol call returned DARWIN_SOL_READY without reading/extracting credentials. A separate DarwinRouter run against installed gemma4:12b-it-q4_K_M returned DARWIN_LOCAL_READY with durable task/validation events in approximately7seconds; Ollama reported about8.1GB loaded afterward, not measured peak usage. These are two successful component checks, not an integrated CLI coordinator/worker loop.

Added a clearly optional direct-HTTP Sol/local sample profile and simulated two-provider delegation tests covering exact model/auth handling, local credential isolation, untrusted child results, durable parent/work/execution linkage, empty output rejection and privacy denials. The sample disables filesystem tools, memory, automatic learning/activation and auxiliary judging. Its cost estimates are not billing caps. The preferred Codex CLI provider adapter and full live round trip remain next work; API-key setup is not a prerequisite for the standalone authenticated CLI route.

Initial connection checkpoint verification: make check passed (format/LOC, vet, full native race suite and build), as did sample configuration validation and focused hybrid delegation race tests. Live local task data remains in git-ignored SQLite storage; neither credentials nor session databases are committed. No complete live coordinator/worker loop, production readiness or completed Linear issue is claimed.

Codex coordinator protocol groundwork: added a bounded JSON-line RPC codec and single-use paused-tool correspondence binding using the installed0.153.4 experimental app-server schema. The binding only admits configured Darwin-namespaced tools for the exact thread/turn and matches the subsequent model/catalog/history/assistant/result before constructing a response. It never executes tools or claims approval. Independent repository inspection confirmed the adapter needs explicit task-owned process cleanup and privacy checks before launch, including resumed sessions; the existing HTTP factory lifecycle is insufficient for a paused subprocess.

These packages are not yet wired into configuration or Service.Run. No Codex subprocess is launched by the new code. Ambient capability/configuration isolation, initialization/turn correlation, cancellation/cleanup, usage accounting and the full live Sol/local round trip remain unfinished. docs/codex-coordinator-integration.md records the implementation boundary and remaining acceptance checks. No Linear issue is marked complete.

Protocol-groundwork verification: make check passed (format/LOC, vet, full native race suite and build; unaffected packages reused cached results). Focused race tests cover fragmented/bounded frames, strict envelopes, serialized writes, wrong attribution, changed history/catalog, duplicate resolution and caller-slice ownership. Review identified pre-serialization bounds, malformed initial history and lossy UTF-8 comparison gaps; these were corrected with regression tests. No real app-server protocol exchange or complete live delegation is claimed by these unit tests.

Codex transport lifetime groundwork: StartProcess now owns a trusted-host, explicitly configured direct subprocess, with absolute executable/directory paths, non-inherited environment, discarded stderr, bounded protocol IO and static failure errors. Cancellation and idempotent Close release pipe operations and reap the direct child. Normal exit leaves buffered frames available until Close; it is not inference success. Review caught a cancellation watcher stopping too early on leader exit and an unsafe numeric process-group signaling race. Cancellation now remains active until transport closure, and shutdown uses Go's direct-process lifetime synchronization instead of raw group signals.

This does not yet provide descendant supervision, sandboxing, Codex capability/configuration isolation, admission wiring or a launchable coordinator profile. A real app-server launcher must resolve those requirements; the internal transport must not be mistaken for permission to run arbitrary model commands. Tests invoke only the test executable and do not use Codex, credentials or model/network inference. Full live Sol/local delegation and complete Linear issue acceptance remain outstanding.

Direct-process checkpoint verification: make check passed (format/LOC, vet, full native race suite and build), with unaffected packages cached; Linux amd64 cross-build also passed. Fresh subprocess race tests cover explicit/empty environment, working directory, invalid/pre-cancelled launch, round-trip framing, sanitized stderr/nonzero exits, blocked read/write cancellation, idempotent Close and suppression of buffered frames after Close. A bounded self-helper descendant fixture verifies cancellation still closes retained stdout after leader exit; it does not prove descendant termination by the transport. Real Codex containment, live protocol interoperability, hosted CI and the complete hybrid test remain unverified.

Controlled coordinator session: internal/codexbridge.Session now implements provider-shaped single-turn interaction over an already-admitted wire: initialize, ephemeral thread/turn creation with the exact model, namespaced dynamic tools, streaming text, paused tool RPCs, matched continuations and verified final completion. It accounts for observed cumulative usage by segment and does not emit reasoning as answer text. Constructors remain inert; application configuration/HTTP factories do not expose this adapter yet. Fresh single-user-message requests are supported; historical import, steering and compaction are explicitly rejected rather than losing role/authority boundaries.

Independent review found mutable request slices, ambiguous case-insensitive/duplicate controls, unknown message-phase handling and permissive pre-turn request queuing. The session now snapshots input before callbacks, validates canonical control keys, distinguishes unknown phase from explicit final output and restricts startup traffic. Task owners must close the session even when the runtime cannot return a tool result. Controlled-wire tests run the actual runtime loop and SQLite store, proving tool results are withheld before persistence and remain withheld after injected persistence failures. These tests do not start Codex, execute an actual delegated model or qualify launcher isolation. The real Sol/local round trip, descendant containment and broad PRD/Linear acceptance remain unfinished.

Session checkpoint verification: make check passed (format/LOC, vet, full native race suite and build; unaffected packages cached), fresh bridge race tests passed, and Linux amd64 cross-build passed. Coverage includes exact model/namespace, handshake/request attribution, EOF/failed turn, duplicate/case-aliased controls, callback mutation, unknown-phase delegation, incremental usage, streamed/completed text mismatch, reasoning suppression, callback cancellation and SQLite restart-visible failure boundaries. Final independent review found no additional controlled-wire blocker. Hosted CI, actual CLI protocol notification compatibility, real account/model interoperability and full end-to-end live delegation remain unverified; no Linear issue is marked complete.

Launch observation checkpoint: an explicit no-inference CLI0.153.4 probe successfully initialized app-server and read effective configuration without creating a thread/turn, copying credentials or editing user settings. Of135 requested feature controls,134 matched; apps_mcp_path_override was absent and remains unknown. Project-document loading, notify and web search were observed disabled, but empty MCP/plugin table overrides retained two MCP and twelve plugin entries. Table merging is not isolation; callable capabilities remain unqualified. Strict config rejected legacy tools.view_image, so the probe retains only the feature control. An observed emittedAtMs notification field is now accepted as nonnegative integer metadata without granting ordering authority. Raw configuration, notification payloads and remote errors are withheld from diagnostic output.

Launch-observation verification: make check passed (format/LOC, vet, full native race suite and build; unaffected packages cached), Linux amd64 cross-build passed, and the opt-in initialize/config-read probe passed. Default CI skips the live probe. Independent review confirmed conservative missing-value handling and removed temporary diagnostic subprocess machinery. CLI version/inventory output is captured before a size check, so this diagnostic is limited to the trusted pinned executable, not an untrusted-process memory-containment test. Application admission/wiring, extension isolation, descendant supervision and the integrated Sol/local/Sol test remain unfinished; no Linear issue is marked complete.

Explicit launch controls: bounded helpers now generate process-local per-entry MCP/plugin disables and skill-path disables, without copying source settings, logging private identifiers or editing user configuration. Names are quoted within TOML values rather than interpreted as CLI dotted paths; output ordering and size limits are deterministic. Missing/null catalogs, malformed entries, duplicate paths/keys, unsafe controls and oversized inventories fail statically. Explicit false is required for disable evidence. An independent agent implemented and adversarially tested skill projection; review confirmed the second process must be re-observed because configuration can change between launches. CLI metadata capture is now bounded during reading, resolving the prior diagnostic allocation limitation.

The no-inference probe now uses three sequential processes, closing each before the next starts: discovery, explicit extension controls, then explicit skill controls. Live CLI0.153.4 results: both MCP entries and all twelve plugins disabled; MCP inventory contained two servers with zero tools; hooks inventory contained zero hooks/errors/warnings. Six skills remained enabled after feature controls alone; explicit skills.config overrides made all six report disabled in the third process. No thread, turn or model request was submitted. These inventories do not prove absence of built-in tools or inherited prompt context. Same-process admission, configured provider lifecycle, descendant containment and the full Sol/local/Sol round trip remain unfinished.

Explicit-control verification: make check passed (format/LOC, vet, full native race suite and build; unaffected packages cached), Linux amd64 cross-build passed, and the opt-in three-process inventory probe passed. Tests cover identifier quoting, deterministic projection, source-content exclusion, malformed/missing fields, path validation, duplicate detection, output limits and bounded metadata capture. No Linear issue is marked complete.

Same-wire preflight checkpoint: NewCheckedSession now retains a cloned trusted-host feature profile and verifies configuration plus MCP/skill/hook inventories on the task's existing connection before thread creation. Prepare permits no-input initialization/checks; Stream rechecks after an earlier Prepare without repeating initialization, using distinct control request IDs. Invalid configuration, unknown inventories, enabled tools/skills, hooks/errors/warnings, mismatched cwd and unexpected requests fail closed and close the wire. Neither constructor launches or takes credentials; host privacy admission and the full supported feature profile remain required. These observations are not atomic configuration locking or built-in-tool/OS isolation.

Installed CLI0.153.4 feature metadata classifies apps_mcp_path_override as removed. The pinned probe now excludes that obsolete flag and observes all134 supplied feature controls matching. Its unsolicited startup notice was identified as remoteControl/status/changed; checked RPC calls accept only explicit disabled status, without exposing identity metadata. Other statuses fail. Such a notice during later streaming remains an unqualified fail-closed availability case. A fourth live process used the actual checked-session Prepare path successfully; no thread, turn or input was sent.

Same-wire verification: make check passed (format/LOC, vet, full native race suite and build; unaffected packages cached), focused bridge race tests passed, Linux amd64 cross-build passed, and the opt-in no-inference probe passed. Tests cover constructor inertia and feature-slice ownership, one handshake with repeated checks, changed configuration after preparation, failed-preflight task-data withholding, unexpected server requests/IDs, remote status and cancellation. Independent review found no blocker in this documented preflight scope. Task-owned configured launcher, built-in context/tool qualification, containment and live Sol/local/Sol delegation remain unfinished; no Linear issue is marked complete.

Task-owned launcher checkpoint: LaunchChecked now validates explicit cloud-allowed privacy/mode, the pinned Sol model, absolute executable/empty cwd and an explicit four-key environment allowlist before starting metadata processes. It captures bounded CLI version/feature metadata, builds a deterministic pinned profile, closes discovery before final launch, applies explicit extension/skill overrides and performs checked-session preparation. Setup has a15-second deadline while the returned session retains the task lifetime. The host must resolve actual resumed-session privacy before invoking this internal entry point; it is not yet available through configured API/CLI providers. Neither credentials nor user config are copied or edited.

Independent profile work initially assumed a single-word stage; real CLI metadata established under development as a two-word stage. The parser now validates the five observed stages and135 unique rows, omitting the removed flag and generating134 controls. Independent lifecycle review found no blocker in the documented direct-process scope. Startup extension effects, descendant containment, built-in context/tool qualification and complete live delegation remain open; this is not a production isolation claim.

Launcher verification: make check passed (format/LOC, vet, full native race suite and build; unaffected packages cached), focused bridge race tests passed and Linux amd64 cross-build passed. Tests cover no-process privacy/environment/model denials, bounded metadata, deterministic profiles, discovery-before-final closure, failed discovery/final-start/final-check cleanup, task cancellation and separate setup/task lifetimes. Opt-in TestLiveCodexCheckedLauncher passed using the existing signed-in CLI and rechecked the returned session; no thread, turn or task data was submitted. No Linear issue is marked complete.

Application integration checkpoint: experimental codex_app_server configuration now pins a cloud Sol model and absolute executable, forbids HTTP credentials/endpoints, and redacts executable paths in display. Explicit tasks use a separate task-owned provider path after resolved-session privacy and fresh-message-shape checks; cleanup spans model, tool and journal failures and removes only the task-created temporary cwd. HTTP transport/factory contracts are preserved, but construction now follows DB/context assembly, so failed HTTP creation can leave a migrated/initialized DB. Automatic discovery/health, auxiliary use and historical/multi-role Codex context remain unsupported. The CLI sample disables filesystem tools, memory, skills and judging and selects the coordinator explicitly.

Fixture verification passes for application-level coordinator delegation, local HTTP worker execution, untrusted-result return, durable parent/child state, privacy denials and process/directory cleanup. Full make check and Linux cross-build passed before the live attempt. The first real application attempt (task BQJR6VQWJRF4GCVVMDL3G4R4SR) persisted task.started, turn.started and task.failed; no worker/tool execution occurred. Separate bounded opt-in inference diagnostics confirmed real Codex thread/turn acceptance followed by strict rejection of queued deprecation/warning notifications. Known feature mentions were use_legacy_landlock, web_search_cached, web_search_request, skip_host_skill_discovery and code_mode_host. Raw warnings/credentials were withheld; these warnings are not assumed harmless. Live proposal and full Sol/local/Sol qualification remained failing/pending at that checkpoint. Default tests skip live inference, and no Linear issue was marked complete.

First live CLI-backed hybrid success: task 6BE4WNSJV2MLSJMAFQSSOJKTKU completed one actual Sol → Ollama → Sol round trip using the existing ChatGPT login, without API credentials. Its single delegate call requested raw Go source. Child execution XKZTM6QHSJDNICFUU2VYI3XLOV ran local gemma4:12b-it-q4_K_M, returned package answer with Answer() returning 42, passed deterministic.go_syntax.v1, and completed. Parent tool completion durably carried the untrusted child output, Sol reviewed it, and the parent completed. Persisted parent duration was approximately12 seconds; this is not a benchmark. Syntax parsing and model review do not constitute compilation or executable test coverage.

Compatibility changes admit only exact observed deprecated-feature summaries and the thread-attributed skip_host_skill_discovery notice on a checked session. Bounded sparse account-rate updates are discarded, not recorded as task usage or budget. Unknown/malformed warnings fail closed. A host-disabled live diagnostic completed a turn but produced zero proposals; its self-reported attempt is not counted as tool execution. Enabling the installed code_mode_host, while keeping other controls disabled, produced a real paused dynamic-tool proposal and enabled the complete application test. The preflight verifies this control true; a disabled-host warning now fails. No user configuration or credential files were copied or changed.

Process snapshots after live completion showed no remaining standalone task Codex process, but did not capture the exact Code Mode helper lifetime. Descendant containment, cancellation/negative-result/restart qualification and complete MVP/Linear acceptance remain open. Background learning remains off. Regression tests cover known/unknown notices, malformed/duplicate/case-aliased controls, thread binding, bounded sparse metadata and no notice-derived output, tool action or usage.

Live-hybrid checkpoint verification: make check passed (format/LOC, vet, full native race suite and build; unaffected packages cached), Linux amd64 cross-build passed, the opt-in live proposal test passed with exactly one unexecuted proposal, and the application smoke test exited zero with durable parent/child acceptance evidence. Default CI still skips live inference. Native Linear still shows no issues Done; this checkpoint does not establish full issue acceptance.

Application-level negative qualification: new controlled-coordinator tests exercise actual HTTP worker cancellation, the application task lifetime and durable parent/work/execution records. Caller cancellation and a durable request from a newly-created service instance both disconnect an active worker request, stop without coordinator continuation or repeated delegation, close the coordinator once, and remove the private directory. Reopened storage proves all three task records canceled with the correct lineage. The terminal tool record retains the bounded rejection, not accepted output. Invalid Go has separate regression coverage proving failed local validation/work records and a bounded rejection returned for coordinator review without releasing the invalid candidate as accepted output.

This is fixture evidence, not additional real Codex inference, helper-process containment or recovery after killing a process. Native Linear remains readable in the DarwinRouter workspace, but issue/search activation and keyboard navigation did not change its page; a coordinate attempt returned noWindowsAvailable. No issue update was submitted or marked complete; local docs retain this checkpoint pending native UI availability.

Negative-qualification verification: final make check passed (format/LOC, vet, full native race suite and build), Linux amd64 cross-build passed, and the focused cancellation/rejection tests passed ten fresh race-enabled repetitions. Independent review prompted exact go_syntax rejection checks, whole-page history coverage and an explicit required parent rejection record; all passed after tightening. Live negative/cancellation tests, helper lifetime qualification and full PRD/Linear completion remain outstanding.

Live cancellation checkpoint: an opted-in macOS test ran real signed-in Sol inference through the application and held a controlled loopback worker request open after delegation. It observed the direct task-owned Codex process and its Code Mode host by parent/descendant ancestry (not global name matching). A fresh Service requested durable cancellation; the application returned context cancellation, the worker HTTP request disconnected, exactly one worker request was made, the private directory was removed, and reopened storage validated the canceled parent/work/execution tree with retained rejection. Both observed process IDs then disappeared. Two live runs passed, including the final stricter requirement that the Code Mode host must actually be observed before cancellation.

The process observer is test-only, read-only and bounded during stdout capture; it reads PID/parent/command names, never argv or credentials, and never signals PIDs. Unit tests cover byte/line/count bounds, malformed and duplicate records, names with spaces, transitive ancestry and cycles. The live test is macOS-only because Linux comm truncation needs a separate identity strategy. It uses a controlled worker, not real Ollama generation, and cannot prove absence of unseen/reparented/later-spawned descendants, upstream inference cessation, all cancellation windows or process-crash recovery. No production containment mechanism was added; graceful turn/interrupt remains unimplemented. Full PRD and Linear completion remain open.

Live-cancellation verification: make check passed (format/LOC, vet, full native race suite and build); focused observer race tests passed; Linux amd64 production cross-build and application test-binary compilation passed without claiming Linux test execution. Default CI skips the opt-in live test. The final macOS live run passed with both required processes observed and subsequently absent; independent review confirmed the bounded snapshot claim and prompted explicit platform scoping.

Coordinator failure feedback: failed dispatched delegations now project an additive version-1 rejection envelope with a fixed reason, verified work/execution IDs and terminal event references. The helper replay-validates work parent/session, execution ancestry, terminal state and sequence; only allowlisted static codes are returned. Raw output, prompts and provider errors are excluded. Projection is capped at2KiB with a one-second cleanup read deadline. Missing/nonterminal/misattributed/corrupt/unavailable evidence retains the generic rejection; completed execution does not imply accepted work. Cancellation still prevents coordinator continuation, and no retry authority or fitness rule changes are introduced.

Standalone auxiliary audits do not yet traverse the new child references, and batch cancellation retains generic suppressed items. These remain explicit follow-up work rather than implied completion of the full audit requirement. Integration tests now bind rejection references to reopened terminal events and check invalid-output/cancellation reasons; existing admission, resource, read-tool and hybrid tests accept the additive fields while still rejecting raw output leakage.

Failure-feedback verification: make check passed (format/LOC, vet, full native race suite and build), Linux amd64 cross-build passed, and focused delegation/cancellation/rejection race tests passed. Cases cover wrong parent/session/child, missing records, unknown codes, nonterminal/completed work, completed-but-unaccepted execution, and payload exclusion. Independent review found no concrete new disclosure/provenance blocker and confirmed that terminal invalid_output is not specific validator/test evidence. No new live inference was run for this change; full PRD/Linear completion remains open.

Standalone delegated-audit checkpoint: AuditTask now follows up to eight canonical rich single-delegate failure references before reviewer construction. It independently checks parent/session and execution lineage, terminal identities/codes/reason, and model-turn attribution for child validator events. Namespaced child metadata is supplied and its references are persisted with the audit; raw child output, prompts and provider failure details are not added to reviewer input. Legacy generic failures and successful-child envelopes remain untraversed. Combined parent/child execution evidence fails closed above 252 records or 64 KiB. Caller cancellation and a five-second traversal deadline apply; storage replay and paged payload bounds are documented separately in docs/delegated-audit-evidence.md.

Focused TestAudit race tests passed, including actual Go validator evidence, exact terminal references, foreign parent/session/execution, forged sequence/code/reason, duplicate/aliased fields, misattributed child validation, and the supervisor's distinct turnless validator identity. Full checks for this checkpoint are recorded separately after completion. No live inference was needed for metadata-only traversal. Native Linear was inspected but no issue update was submitted this turn. Sibling work under the same parent lacks an independent tool-call binding; current attribution is a reference from the trusted parent journal, not a claim of stronger ownership proof. Batch/success traversal, interrupted/canceled-history qualification and full PRD/Linear completion remain open.

Delegated-audit verification: make check passed (format/LOC, vet, full native race suite and build), and Linux amd64 production cross-build passed. Independent review identified and resolved case-aliased envelope detection, caller-respecting traversal deadlines, accurate replay/page budget documentation and legitimate turnless supervisor validation. No new live inference or Linear completion claim is made.

Successful/batch audit checkpoint: AuditTask now traverses canonical successful single delegations and mixed two-to-four-result batches. Success requires completed work/execution, same-worker ordered supervisor acceptance and completion, and exact agreement between published, accepted-work and final-execution output. Batch metadata retains zero-based item indices with unique evidence IDs. Requested cardinality comes from the replay-paired tool call; only the count is retained. Dropped/extra slots, duplicate child references and malformed shapes fail before reviewer dispatch. Generic unavailable items add no invented evidence. Shared traversal and evidence limits remain unchanged; no raw child output is added to metadata and no fitness/retry policy changes are introduced.

Success/batch verification: make check passed (format/LOC, vet, full native race suite and build), Linux amd64 production cross-build passed, and focused TestAudit race tests passed. Seeded-journal/HTTP-reviewer fixtures verify single and mixed results, persisted citations, private child-payload exclusion, foreign lineage/session, missing/false acceptance, mismatched worker identity, altered output, failed execution, duplicate references, slot cardinality and strict argument parsing. Independent review prompted terminal worker identity and paired cardinality checks and found no remaining concrete blocker in this scope. No new live inference or Linear update was performed. Batch cancellation still suppresses child references; independent call-to-child ownership, interrupted/canceled history and full PRD qualification remain open.

Durable delegation-origin checkpoint: each application worker now records versioned originating turn, attempt, tool-call ID/name and optional batch index in its TaskStarted payload. The scoped executor supplies immutable argument-free identity after schema/policy admission and masks inherited identity on unscoped calls. Application delegation requires the correct parent/session identity; host fan-out supplies each batch position. The supervisor clones and validates origin metadata before waiting for capacity, and persists it before dispatch. Audits require exact call/turn/attempt/name/index matching in addition to existing lineage and acceptance checks, closing sibling-reference substitution under the trusted journal model. This is provenance, not an approval token or cryptographic attestation.

Origin verification: make check passed (format/LOC, vet, full native race suite and build), Linux amd64 cross-build passed, and focused application/runtime/tools/workers race tests passed. Tests cover single/batch pre-execution persistence, pointer ownership, origin shape/placement, nested identity replacement and masking, approved-context isolation, unscoped/foreign invocation rejection, missing/mismatched audit origins, and reopened coordinator failure/cancellation records matched to their actual tool call. Independent review found no concrete blocker. Old records remain readable but missing-origin referenced work is not auditable; no origins are inferred or backfilled. No new live inference or Linear update was performed; batch cancellation, interrupted-history recovery and complete PRD qualification remain open.

Interrupted-delegation recovery checkpoint: expired claimed submissions can now recover the narrow boundary where all delegated workers are terminal but the parent tool-result event was not committed. A bounded pure planner verifies exact durable origins, call cardinality, acceptance and terminal execution evidence before reconstructing canonical single/batch results. One transaction appends the recovered tool result and linked parent interruption, fences the old owner, clears the claim and records a recovery receipt. Submission and parent-task cancellations are honored. Recovery invokes no provider, tool or evaluator, does not requeue work, and never invents successful parent output. Explicit continuation creates a new task from the restored history through a history-capable provider; Codex CLI remains fresh-task-only. See docs/interrupted-delegation-recovery.md for limits.

Interrupted-delegation verification: make check passed (format/LOC, vet, full native race suite and build), Linux amd64 production cross-build passed, and focused application/storage/submission/session race tests passed. Runtime fixtures inject a SQLite failure at parent tool-result persistence, verify unchanged child histories and no recovery inference, and exercise explicit continuation with restored single/batch results. Storage tests cover cancellation, stale ownership, concurrent recovery, transaction rollback and bounded input; planner tests include input immutability and uncertain-effect rejection. Independent review found no remaining blocker after cancellation and uncertain-effect fixes. This is fixture fault injection, not live inference or actual process-kill qualification. Running children, missing slots, general interrupted inference, automatic continuation, Codex historical continuation and full PRD qualification remain open. Native Linear remained unresponsive to issue navigation; no issue update or completion is claimed.

Interrupted-delegation process-crash qualification: new single/batch subprocess tests run the actual service/runtime against loopback HTTP fixtures, then pause a test-only SQLite BEFORE INSERT function inside the parent tool-result transaction. The parent kills only its owned child process and verifies SIGKILL before reopening storage. Terminal worker evidence survives; the parent remains at ToolStarted with no committed result. After fixture-trigger removal and manual claim expiry, dispatcher reconciliation restores exactly two parent events, preserves every child journal and the original parent prefix, records one recovery receipt, and performs no further provider call. Repeating reconciliation changes neither receipt nor journals. A separate real-process test kills an owner during an unfinished child request and verifies disconnection, three unresolved running journals, no redispatch/receipt, and denied continuation rather than invented completion.

Process-crash verification: both boundary tests passed five repeated runs with race detection. Full make check passed (format/LOC, vet, native race suite and build; unchanged packages cached), and Linux amd64 production cross-build plus application test-binary compilation passed. Execution evidence is macOS; Linux tests were not run. Independent review found no concrete blocker and prompted complete-history and platform-scope assertions. These tests qualify fixture-provider SIGKILL at specified boundaries, not power loss, live Codex inference, automatic daemon restart, all crash windows, remote generation cessation or automatic continuation. No production hook was introduced. Native Linear still did not respond to search activation; no issue update or completion is claimed. Full PRD qualification remains open.

Continuation-readiness checkpoint: a shared versioned metadata assessment now exposes observed task ID, sequence, state, history eligibility and a closed reason vocabulary without messages, tool arguments or results. CLI `task continuation`, SDK `InspectTaskContinuation` and authenticated GET `/v1/tasks/{task}/continuation` use a bounded, replay-validated read-only transaction containing both snapshot and recovery tail. The application uses the same exact recovered-tail assessment for failed-task continuation; ordinary admission rules and recovered compaction restrictions are unchanged. Pending tools, uncertain effects and interrupted turns remain ineligible. The HTTP route validates method/body/query (including bare query delimiters), task binding and backend metadata, with bounded control slots, cooperative deadline and sanitized errors. History readiness is not provider readiness, retry authority or automatic resume; Codex CLI still rejects historical continuation.

Readiness verification: full make check, make build and Linux amd64 production cross-build passed. Focused race tests cover shared state/checkpoint semantics, malformed/corrupt records, no partial metadata on errors, read-only inspection, real HTTP/SQLite integration, CLI argument/output failures, SDK cancellation, and actual runtime recovery transitioning from pending_tools to recovered_delegation before explicit continuation. Independent review found no concrete blocker. The rebuilt local CLI inspected existing smoke-test task 6BE4WNSJV2MLSJMAFQSSOJKTKU successfully: sequence73, completed history, history_eligible true; it did not dispatch inference or print conversation content. This is not evidence that Sol accepts resumed history. Native Linear remained on DAR-42 after prior search attempts; no issue update or completion is claimed. Automatic resume, Sol historical continuation and complete PRD qualification remain open.

Explicit Sol historical-continuation checkpoint: the application now admits eligible completed or exact recovered-delegation histories to the experimental Codex CLI provider. Following the official app-server documentation and installed CLI0.153.4 generated schema, prior messages and tool pairs are projected into typed thread/inject_items on a new ephemeral thread; only its empty-object acknowledgement permits turn/start with the new prompt. Historical tool output is context, not permission or redispatch. Invalid history, unknown notices, failed imports and oversized complete injection frames fail closed. Delayed thread metadata is identity-bound, and existing checked compatibility-notice rules remain required. One injection frame must fit1MiB; no silent flattening, truncation or automatic compaction occurs. Explicit continuation is a new parent-linked Darwin task, not native thread-ID resume.

Import privacy and redaction: source privacy gates remain before launch. Current credentials are scrubbed from owned decoded history without rewriting the source journal. Structured object/array tool content is strictly decoded before scrubbing escaped strings and keys, preserving numeric precision and rejecting duplicate keys, redacted-key collisions, malformed JSON, excessive depth and size. This closes a concrete rotated-secret leak in JSON delegation envelopes, not arbitrary nested encoding or obfuscation. Fresh-task multi-role context, compaction/summary and automatic routing/auxiliary use remain unsupported by this provider. Independent review found no remaining concrete blocker in this scope.

Live history evidence: the opt-in synthetic Sol test passed with one import, one inference turn, zero tool proposals and the exact saved marker. Two initial attempts stopped before turn/start on observed startup-notice orderings, now covered by fixtures. An actual application CLI continuation of prior live Sol/local-worker task6BE4WNSJV2MLSJMAFQSSOJKTKU completed as HUXCS3QPL2IPQQDAIIDFN6FXXS with answer42, reviewing the saved Go function with new delegation disabled. Reopened CLI inspection confirms completed sequence6 and the source parent link; the original source remains completed at sequence73. This was not new Ollama inference, Go execution, or live crash-recovery continuation. Application fixtures additionally qualify restored-delegation import, source/child journal immutability, no repeated local work, privacy denials and escaped-secret rotation. See docs/codex-history-continuation.md. No Linear update or completion is claimed. Automatic resume, in-flight steering, compaction, stronger containment and complete PRD qualification remain open.

Historical-continuation verification: full make check passed (format/LOC, vet, native race suite and production build; unchanged packages cached), make build passed, and Linux amd64 production cross-build passed. Focused bridge/session/history and application/Codex/redaction race tests passed. Live inference preceded the final import-redaction hardening; that hardening is covered by strict unit tests and an actual application/local-worker fixture with a synthetic Codex wire. The final rebuilt CLI reopened the live task/source metadata successfully without dispatching inference. Default CI skips the opt-in live test; Linux execution, broad CLI-version compatibility and all crash windows are not claimed.

Signed-in Sol output-audit checkpoint: AuditTask now supports the experimental Codex CLI provider through an inert, single-use adapter. A checked native session launches only from Reviewer.Stream after source/privacy/cost/evidence admission, durable BeginReview and assembled-context estimation. Its one-minute timeout includes startup; no tools, native repository review or automatic retries are introduced. Shared invocation-owned launch cleanup handles returned-provider errors and panics and removes only the private working directory. System rubric and untrusted user evidence remain distinct typed inputs. Current-secret redaction decodes structured historical tool envelopes before evidence serialization, without rewriting source journals. Reviewer.StructuredOutput is opt-in, enabled for native audits, and adds a closed host-pinned schema before context estimation; ParseAudit remains independently authoritative. HTTP review defaults and existing domain-sensitive advisory ranking/user-feedback precedence are unchanged.

Sol-audit qualification: a live CLI0.153.4 review of a synthetic promise-only Go response passed with verdict reject and two findings, persisted as a separate audit while preserving all six source events. The candidate was produced by a loopback fixture declared cloud-eligible, not live Ollama inference. Initial prompt-only native attempts completed but returned non-JSON and were correctly rejected; documented turn/start.outputSchema resolved generation format without accepting invalid output. One earlier fixture setup attempt failed before inference because of an unsupported validation selector. No raw model/audit payload or credentials were logged by the live diagnostic. Native automatic-audit fixtures verify one post-completion review, no recursion, and unchanged successful candidate text/state on malformed audit. Other tests cover durable-before-launch ordering, BeginReview failure, context denial, self/privacy/cost rejection, schema ownership and estimation, tool proposals, cancellation, panic/returned-provider cleanup, and source/measured-fitness immutability.

Sol-audit verification: full make check passed (format/LOC, vet, native race suite and production build), make build passed, and Linux amd64 production cross-build passed. Focused evaluation/schema and application/audit/lifecycle race tests passed. Default CI skips the opt-in live audit. See docs/codex-output-audits.md for supervised use and the distinction between advisory routing influence and measured evidence. Stronger isolation, broad CLI compatibility, live audit crash recovery, automatic auditing of every delegated child and full PRD qualification remain open. Native Linear still displayed DAR-42; search activation exposed only a tooltip rather than issue navigation, so no issue update or completion is claimed.

macOS thermal-admission checkpoint: the built-in host profiler now queries Foundation NSProcessInfo.thermalState through a fixed absolute-path osascript invocation, without application automation, user-supplied scripts, administrator access, power changes or model inference. Nominal/fair map to false pressure and serious/critical to true, activating the existing new-local-reservation denial. Optional probe failure or unknown/malformed output preserves memory observations and leaves thermal pressure null; caller cancellation and the aggregate deadline still fail the profile. The optional thermal_state diagnostic has a closed vocabulary; custom profiler labels must agree with their pressure boolean, while the previous boolean-only contract remains valid. Fixed probe deadlines/output bounds and the three-second overall profile deadline remain in force. Extracted macOS profiling is now fixture-testable on other build hosts.

Thermal verification: full make check passed (format/LOC, vet, native race suite and production build), make build passed, and Linux amd64 production cross-build passed. Injected-probe tests exercise all states through the actual Budget.Reserve path, optional failures, malformed/oversized/extra output, memory preservation, fixed arguments and cancellation. Custom-profiler tests reject arbitrary or inconsistent thermal metadata. Independent review prompted removal of an unreachable profiler tail and validation of extension-provided labels. The rebuilt CLI on this Mac observed nominal/false thermal pressure, 16 CPU threads and48GiB unified memory. The older pmset thermal query reported no recorded data and is not used as evidence of cool conditions. Apple documents that unsupported/unknown sensor conditions can be reported as nominal, so the live result is an OS report, not physical-temperature or hot-state qualification. See docs/macos-thermal-profiling.md. Linux thermals, fair-state progressive throttling, active-inference preemption and complete PRD qualification remain open. No Linear issue update or completion is claimed.
