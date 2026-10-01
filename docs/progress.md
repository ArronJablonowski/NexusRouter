# Implementation evidence

This file tracks recent work and remaining gaps. Earlier entries are preserved,
unchanged and in original order, in the following archive parts:

- [Part 1](progress-archive/2026-10-01-part-01.md)
- [Part 2](progress-archive/2026-10-01-part-02.md)
- [Part 3](progress-archive/2026-10-01-part-03.md)
- [Part 4](progress-archive/2026-10-01-part-04.md)
- [Part 5](progress-archive/2026-10-01-part-05.md)

Concatenating those files reproduces the former `docs/progress.md` byte for byte.
Its SHA-256 was `e09b5a56b456791660adde6d16cd4b97c4c1d5eef996c2615ddb448ed8f628d4`. Archive entries are historical
checkpoints; they do not establish current completion. Recent entries below are
retained here for ongoing work. Keep each progress file below the release
snapshot limit of 1 MiB; archive older entries before the limit is reached.

### 2026-10-01 — DAR-132 opt-in SDK Pi host-tool routing

Connected NativeTools/native_tools Pi registrations to the ordinary application
registry, rooted file handlers, Workboard/custom-tool definitions, durable scoped
approval executor, redacting submission journal, privacy transport and combined
model/harness reservation. The actual runtime catalogue must equal the selected
catalogue. Identity binds schemas, turn ceiling and extension scope/decision rules;
automatic selection excludes text-only pairs when tools are configured and cloud
models in tool mode. Existing text registrations retain their prior behavior.

The host validates final response contracts before success. Result turns and usage
come from the validated native journal without an aggregate duplicate. Constructor
ordering snapshots settings and installs extensions before computing native
identities. Queue generation 6 fences this new capability; branch/resume storage
readers recognize the envelope version without enabling native continuation.
Custom process-local extensions and replace operations retain queue restrictions.

Installed Pi fixture SDK qualification passed for read/create/deny/contract/escape,
automatic selection, durable queue duplicate suppression and YAML registrations
under race (19.121s). Measured usage totals and secret redaction are verified.
Additional tests with an existing outside-root file and final-response rejection
passed (5.599s). App native identity/configuration tests passed under race (25.412s)
and focused approval/extension/file-tool regressions passed (35.489s); targeted vet,
source formatting/size and diff checks passed. A broader SDK regression run exposed
a duplicate-usage fixture error; the strict gateway correctly refused it and the
corrected native tool cases passed. No production check was relaxed.

Full repository gate/push is queued behind the live SDK gate. No daemon was
reconfigured or deployed. Native tools in the other four harnesses, dedicated
CLI/API tool-mode qualification, broader fault qualification and held-out real-model
accuracy comparisons remain open; this fixture evidence does not close DAR-132.

### 2026-10-01 — DAR-132 authenticated HTTP Pi tool qualification

Added installed-Pi integration through the real authenticated chat handler and
application service with configured native_tools, rooted reads and a pinned
Ollama-wire provider fixture. Plain and SSE output preserve final text and measured
30-input/6-output usage across two turns. Provider bodies and HTTP output exclude
the secret read from the file. Independent durable reads verify the native agent
journal and actual Pi adapter identity.

Client cancellation after a completed tool leaves TaskCanceled; invalid final JSON
leaves TaskFailed. Neither exposes the final answer, outcome or SSE DONE marker.
Unauthenticated calls cannot dispatch inference. Chat and native-task decoders also
reject caller-supplied native_tools, preserving operator-only registration authority.
Updated stale embedding comments to reflect opt-in SDK integration.

Native and adjacent HTTP authority/usage race tests passed (11.846s), including
installed Pi; targeted vet, source and diff checks passed. No real-model benchmark
or deployed daemon was used. CLI process qualification, API operator-approval
workflow qualification, other harness tool adapters and held-out quality evidence
remain open. Full repository gate/push remains queued behind the live SDK gate.

### 2026-10-01 — DAR-132 compiled CLI qualification and release-gate repair

Built cmd/nexus and qualified its actual subprocess configuration-loading path
with installed Pi, production resource measurement and private home/owner state.
Plain output and JSON events/results retain rooted tool execution, secret redaction,
canonical Pi identity and measured 30-input/6-output usage. Configured create without
an approval adapter returns admission_denied before any inference or task database,
and creates no file. CLI qualification passed under the race test driver (4.081s);
the subprocess is a normal compiled CLI. No actual model inference or deployment.

The still-running older SDK full gate reported two releasepack failures. Reproduced
the cause: full go-list JSON reached 1,074,708 bytes and exceeded the existing 1 MiB
bounded command output. Commit dbdb477 requests only the complete set of fields
used by package/graph/module/license validation in both notice derivation paths.
It does not increase output limits, weaken validation or omit dependency nodes.

An actual-Go comparison independently proves the projected closure equals the full
reference: 319 packages, 14 legal modules, identical graph and legal evidence.
Comparison passed under race (7.434s); repository derivation passed (11.008s), and
freeze/verify from a clean committed clone passed (98.020s). Targeted vet/source/diff
checks passed. The old checkout remains untouched while its full gate finishes.
The corrected branch will receive a full gate; its supervisor accepts the older
failure only if the terminal log contains exactly the two reviewed license tests
and only releasepack failed. Any additional failure requires investigation.

Remaining DAR-132 scope includes API operator-approval workflow qualification,
other harness native tools and held-out real-model accuracy evidence. No full-gate
pass or push is claimed for the corrected checkpoint yet.

### 2026-10-01 — DAR-132 native Pi durable HTTP approvals

Qualified installed Pi through the authenticated streaming HTTP task path and the
ordinary durable approval presenter/decision API. While the task occupies the
single execution slot, operators can inspect and decide its pending file create.
No write occurs before approval; unauthenticated and incorrectly bound decisions
are rejected. Replaying the approved command leaves exactly one decision and one
confirmed effect. Denial and canceled requests leave no file or successful harness
outcome, and cancellation rejects subsequent decisions. Independent read-only
journal inspection verifies terminal state and operator attribution. Raw proposed
content is absent from approval inspection.

The installed-Pi fixture integration test passed under race; targeted API vet,
source and diff checks passed. This is protocol/authority qualification, not a
real-model quality result or browser UI verification. Other harness native tool
adapters and held-out accuracy evidence remain open. The earlier SDK full gate
is still running; its known releasepack failures have a separately verified
correction on this branch. A fresh complete gate is required before pushing.

### 2026-10-01 — DAR-132 OpenHands host tool embedding

Implemented native SDK 1.50.1 tool execution through the existing host-owned agent
runtime and authenticated gateway. The bridge exposes only host schemas, maps
native action IDs without trusting child arguments, enforces sequential execution,
returns concrete registered observations, and rejects unsupported/error lifecycles.
Its final text must equal the independently verified gateway result. Resource
admission, scoped execution, journal-before-effect, cancellation and per-turn usage
use existing host mechanisms. Native tools have a separate configuration-bound
identity and immutable configuration snapshot; legacy text mode stays separate.

Initial installed-SDK agent race qualification passed (25.363s). Expanded coverage
adds cancellation inside a tool, turn-budget rejection and identity snapshot checks.
Application/SDK registration remains Pi-only; OpenHands integration there, real
approval-controlled writes and held-out quality evidence are still required.
No live service or model inference was used. Full checkpoint validation/push will
queue behind the existing SDK gate; its frozen checkout remains untouched.

Expanded full OpenHands package race suite, including all installed-SDK native
cases and legacy text regressions, passed (45.135s). Targeted vet, source formatting
and diff checks passed. No full-repository pass or push is claimed yet.

### 2026-10-01 — DAR-132 OpenHands SDK tool routing

Enabled explicit native_tools registrations for OpenHands alongside Pi. Both use
one host agent lifecycle with exact registry matching, local-model restrictions,
context estimation, durable approvals, response validation and usage projection.
OpenHands selection/execution bind its runtime digest, bridge/schema/turn identity
and extension policy rules; other harness tool modes remain rejected.

Shared installed-runtime SDK race qualification passed for Pi and OpenHands
(48.137s): rooted reads, approved create, denied create with no file, existing
outside-root escape rejection, redaction, final response-contract rejection,
automatic selection, YAML startup and queued idempotency/outcome reconciliation.
This uses controlled provider fixtures, not real-model quality comparisons.
Dedicated OpenHands HTTP/CLI/browser approval qualification, remaining harness
tool adapters and held-out quality evidence remain open. Full gate/push remains
queued behind the older SDK validation; no deployment is claimed.

Constructor/identity race checks also passed for both tool modes, including
extension-scope binding, immutable settings snapshots, refusal to upgrade legacy
registrations implicitly, and refusal of cloud tool models. Targeted vet, source
formatting and diff checks passed.

### 2026-10-01 — DAR-132 OpenHands HTTP and compiled CLI qualification

Extended the existing native Pi integration fixtures to installed OpenHands SDK
1.50.1 with executable and dependency-manifest pins. Real authenticated HTTP
plain/SSE execution preserves rooted file results, redaction, measured usage and
actual tool-mode identity. Invalid final JSON and cancellation cannot emit a
successful outcome. Durable operator decisions are tested while the task holds
the single execution slot: pending writes do not occur, unauthenticated/forged
bindings are rejected, repeated approval records one decision/effect, denial
creates no file, and cancellation rejects late approval.

The compiled CLI runs both harnesses with production resource measurement and
private home/owner state. Plain and JSON results retain canonical usage/journals;
configured writes without an approval handler fail before inference or storage.
Race test drivers passed HTTP/operator cases (41.998s) and compiled CLI cases
(9.789s). The CLI subprocess is a normal build. Targeted vet/source/diff passed.
All provider output is controlled fixture data: no real model quality ranking,
live service change or browser presentation qualification is claimed.

Full repository gate/push remains queued behind the older SDK run. Its telemetry
child was freshly observed consuming CPU; no restart or frozen-checkout edit was
performed. Other harness tool adapters and held-out comparative evidence remain
required for DAR-132.

### 2026-10-01 — DAR-132 ordered native tool rendezvous for Goose

Installed Goose 1.52.0 interface probes showed that MCP tool requests can arrive
in reverse provider proposal order. Added an opt-in ordered tool bridge: the host
registers verified proposals in canonical order, and concurrent native arrivals
wait for their predecessor. A waiting call never launches an unrequested one.
Exact retries use cached results; cancellation, timeout, failed execution and
terminal tool results fence later effects. This includes cancellation of the
first call before it acquires the execution slot. Existing Pi/OpenHands bridge
construction retains its existing behavior.

Focused race tests passed (1.522s), covering reverse arrivals, duplicate requests
and registrations, missing predecessors, cancellation/close/timeout, terminal
and recoverable failures, and the canceled-first-call regression. Targeted vet,
source formatting/size and diff checks passed. This is a shared prerequisite;
Goose MCP transport, child-only tool aliases, native lifecycle reconciliation,
SDK opt-in and installed-binary tool qualification remain to be implemented.
No Goose tool-execution readiness or comparative model quality is claimed.
Full repository validation/push is queued behind the frozen SDK gate.

### 2026-10-01 — DAR-132 private Goose MCP transport qualification

Added a private stateless MCP handler over the ordered bridge. It authenticates
loopback requests with a per-run token, snapshots the host tool catalogue, and
supports only initialization, tool listing and verified tool-call retrieval.
Goose's original provider call ID must match a registered host proposal; tool
name and structured arguments must also match, preserving large integer values.
Native session/cwd metadata confers no authority. Changed calls, duplicate JSON
keys, invalid IDs, batches, foreign origins and unauthorized transports reject
without effects. Exact native retries return the cached host result.

Installed Goose 1.52.0 passed the native MCP rendezvous fixture (race driver,
1.630s): its named extension exposed only nexus__lookup, executed two distinct
same-argument call IDs in host order, returned both correctly bound results and
completed a second provider turn. This fixture uses controlled provider data and
a test executor, not SDK journal qualification or real-model quality evidence.
The toolbridge and Goose package race tests passed (1.554s and 2.197s); targeted
vet/source/diff checks passed. Gateway alias projection, native lifecycle/output
reconciliation and application opt-in remain outstanding.

The frozen SDK full gate completed with exactly the two previously reviewed
releasepack license-evidence failures; no additional package/test failures or
panic were found. The current branch includes the validated dbdb477 correction.
A fresh full gate will run against this clean committed checkpoint before push.

### 2026-10-01 — DAR-132 Goose host tool runner and canonical reconciliation

Reused the clean former SDK checkout on codex/goose-native-tools after its gate
terminated; the prior native-adapter checkout remains untouched under full check.
Added Goose embedding entry points using the normal RunHarnessAgent session,
ordered private MCP extension and a separate goose-mcp-tools-v1 identity. The
native child sees namespaced aliases while provider requests and journals retain
canonical tool names. The gateway re-verifies projected streams and exposes an
owned completed transcript for native-output reconciliation. The runner compares
every native tool proposal/result and final answer, rejects missing/replayed or
altered evidence, and preserves per-turn journal usage and process cleanup.

Installed Goose passed nine durable-journal modes (31.646s race driver), plus the
concurrent two-call case and identity ownership checks (3.236s). Cases cover normal,
recoverable and terminal tool results, denial, wrong model, provider/tool
cancellation, turn bounds and Ollama delivery. The gateway race suite passed
(18.642s), including canonical journal, redaction, result retries and concurrent
MCP calls. Projection rejection and legacy runner unit tests passed (1.602s).
All inference is controlled fixture data; no accuracy ranking is claimed.
Normal SDK/config opt-in and HTTP/CLI approval qualification remain open for Goose.

The complete Goose package also passed with native qualification enabled
(45.620s), including the legacy text adapter and private MCP fixture. Targeted
vet, source formatting/size and diff checks passed. Full repository validation
and push will follow the already-running predecessor gate; no deployment is
claimed.

### 2026-10-01 — DAR-132 Goose SDK routing and operator qualification

Enabled explicit native_tools registrations for Goose in config validation and
the closed application adapter registry. Identity selection and execution share
the Goose agent configuration; schemas, turn limits and extension policy remain
bound to the queue/learning identity. Existing local-model, resource, exact
catalogue, approval and response-contract controls are reused. Legacy text
registrations do not acquire tool authority; Hermes/OpenClaw still reject this
flag. No live configuration or deployment was changed.

SDK qualification exposed an overstrict native-output check: provider response
IDs may repeat across turns, while the verifier incorrectly required global
message-ID uniqueness. Removed that unsupported constraint; transcript order and
verified tool-call IDs still reject duplicate/missing/altered lifecycle records.
Added a repeated-response-ID regression and retained replay rejection tests.
The constructor fixture was also updated to expect Goose's own adapter identity.

Installed Goose passed all eight SDK modes (25.425s race driver): rooted reads,
approved create, denied create, response contract, escape rejection, automatic
selection, queued idempotency and YAML configuration. Authenticated HTTP plain/SSE
and durable operator decisions passed (22.972s): pending writes remain absent,
forged/unauthenticated decisions reject, duplicate approval has one effect, denial
and cancellation leave no successful outcome, and late approval rejects. Compiled
CLI qualification passed (4.462s), preserving plain/JSON output, redaction and
canonical usage while rejecting unreviewable writes at admission. Targeted
projection, constructor and config race tests passed (1.444/1.710/1.697s), as did
vet, source formatting/size and diff checks. These are controlled fixtures, not
real-model quality rankings or browser presentation qualification.
Full make check/push remains queued behind the prior branch's live gate.

### DAR-132: Hermes native host-tool embedding checkpoint

Added the pinned Hermes 0.21.5 embedding runner with a host-only namespaced tool
catalogue, original call-ID binding and ordered host execution. The embedding
preserves distinct IDs with identical arguments instead of Hermes's default
name/argument deduplication. Provider requests, tool policy, admission, durable
starts, results and usage remain owned by NexusRouter; native transcripts must
match the completed canonical transcript before accepting final text. Native
memory, context-file discovery and tool-search catalogue rewriting are disabled
in the private run configuration. Installed source is unchanged.

Installed Hermes passed nine native race-test modes (25.615s): normal,
recoverable tool error, end-tool-use, denial, wrong-model rejection, provider and
tool cancellation, turn ceiling and Ollama protocol. A separate two-call fixture
passed (4.161s), checking distinct IDs and original execution order for identical
arguments. Identity/ownership and negative transcript tests passed (1.500s),
including changed result, failure flag, model, final text, call ID and large JSON
integer rejection. Targeted vet, formatting/source limits and diff checks passed.
These tests use controlled providers, not real-model accuracy evidence.

This checkpoint supplies RunAgent/RunAgentTask only. Hermes native_tools SDK/app
registration, broader approval/HTTP/CLI qualification and real-model comparisons
remain outstanding. Full repository validation and push are queued behind the
previous frozen branch's live gate; no deployment is claimed.

### DAR-132: Hermes tools through SDK, HTTP and CLI

Hermes native_tools registrations now use the common native agent lifecycle in
SDK/application routing, with source/runtime pins, local-model policy, tool-scope
identity and queue binding. The text-only adapter remains separate. Configured
Hermes registrations retain hermes_source_dir when constructed through the SDK.

Installed Hermes passed all eight SDK fixture modes (25.350s race driver): read,
approved create, deny, contract, root escape, automatic selection, queue and YAML
configuration. HTTP plain/streaming/contract/cancellation and durable operator
approve/deny/cancel qualification passed (23.285s); compiled CLI plain/JSON output
and admission rejection of unreviewable writes passed (7.327s). Shared constructor,
identity and config race checks passed (1.892/1.371s), along with targeted vet and
source/diff checks. Tests use controlled providers; they do not rank real models.
Full make check and push are queued behind the previous live frozen gate. No
service was deployed. OpenClaw native host tools, real-model comparative runs and
remaining remote-routing product/physical-host qualification remain outstanding.

### DAR-132: OpenClaw native host-tool embedding

Added OpenClaw 2026.9.7 RunAgent/RunAgentTask with a pinned private plugin,
explicit host-only tool catalogue and disabled native tool-search rewriting.
Original callback IDs authorize calls through the ordered loopback host bridge;
normalized native provider IDs never replace canonical journal identity. Bounded
plugin receipts are reconciled against canonical calls, exact JSON arguments,
results and failure flags. Native turn/tool/failure counts and final payload are
also checked against host evidence. Recoverable errors use details.status=error;
OpenClaw's overall ok flag alone never proves successful execution. No installed
package or production configuration was changed.

Installed OpenClaw passed ten native race fixture modes (61.608s driver): normal,
identical-argument distinct calls, recoverable failure, end-tool-use, denied tool,
wrong model, provider/tool cancellation, turn ceiling and Ollama. Negative receipt
and summary reconciliation plus identity/ownership tests passed (1.416s); targeted
vet, source formatting/size and diff checks passed. Direct answers with no tool
calls are accepted only with an empty receipt stream and canonical final answer.
These are controlled fixtures, not real-model comparative evidence.

This checkpoint exposes embedding only. OpenClaw native_tools SDK/application
registration and HTTP/CLI/approval qualification remain next. Full make check and
push are queued behind the preceding frozen branch's live validation; no
production deployment or completed DAR-132 claim.

### DAR-132: OpenClaw tools through SDK/application routing

Enabled explicit OpenClaw native_tools registrations through the shared native
agent lifecycle. Constructor identity and queued authority bind the actual host
tool catalogue and policy; unknown harness kinds still reject. The normal local
model, rooted tools, approval, redaction, response contract and resource controls
remain shared with Pi, OpenHands, Goose and Hermes. Documentation now describes
all five tool-mode adapters and OpenClaw's original-ID/receipt boundary.

Installed OpenClaw 2026.9.7 passed eight SDK modes (52.865s race driver): rooted
read, approved create, denied create, response contract, escape rejection,
automatic routing, queued idempotency and configured registration. HTTP plain/SSE,
contract/cancellation and durable operator approve/deny/cancel passed (46.700s).
Compiled CLI text/JSON and rejection of unreviewable writes passed (14.015s).
Constructor/config race checks passed (1.913/1.350s), plus targeted vet, source
formatting/size and diff checks. Controlled provider fixtures qualify execution
and attribution, not real-model comparative quality or completed DAR-132.

Full make check and normal push are queued behind the preceding live frozen
validation. Its application package passed in 2365.757s and CLI testing continues;
the running checkout remains untouched. No production deployment. Comparative
model/harness learning qualification and remaining remote-routing requirements
are still open.

### DAR-132 real-provider gateway compatibility — 2026-10-01

A separately recorded Pi/Devstral diagnostic captured Ollama 0.34.4 returning
`prompt_eval_cached_count: 0` on its terminal native tool response. The strict
native tool gateway rejected this documented metric before releasing any tool
proposal. Preserve both failed executions and zero quality feedback; they are
infrastructure evidence, not model-quality rejections. An intervening admission
was a zero-dispatch resource deferral, with no provider requests.

Accept the optional cached prompt metric only as a terminal integer within the
measured prompt total. Do not add it to token usage. A regression failed before
the fix; the captured synthetic file-read response now verifies offline with
705 input / 14 output tokens and the original tool arguments. Zero/nonzero cache,
invalid types, negative/oversized values, missing totals and premature metrics
are covered. Gateway race suite passed (18.946s), captured/metric regressions
passed (1.412s), targeted vet and source checks passed. Ollama's upstream
openai/openai_test.go TestToUsage confirms cached tokens are within prompt total.

The queued validation was stopped before it started; the preceding frozen gate
remains untouched. Queue the corrected commit for full make check before push.
Real-provider success and joint held-out accuracy qualification remain open;
no completed inference was replayed and no deployment occurred.

### DAR-132 native Goose streamed fragments — 2026-10-01

The first real Goose/Devstral case completed two provider turns and a canonical
file read, then failed native projection reconciliation. Its failed task remains
infrastructure evidence with zero quality feedback, despite the provider's final
text matching the expected answer. The serial qualification coordinator stopped.

Offline replay with installed Goose reproduced a coverage gap: native stream-json
emits adjacent text deltas with one message ID. Single-record fixture responses
had concealed it. Join only adjacent assistant records with matching IDs and
envelopes (native timestamps may tick); preserve every content fragment, then
validate the full result against the host transcript. Changed metadata, altered
text, extra message IDs and duplicate completion remain rejected. Repeated text
is retained rather than deduplicated, so canonical comparison detects duplicates.
Both text-only and native-tool projections accept the documented stream shape.

The offline native regression failed before correction. Captured synthetic native
stream/transcript fixtures and malformed-fragment checks now pass. Installed
Goose full race suite passed (46.553s), with new token-fragment native mode;
targeted vet, formatting/size and diff checks passed. No new model inference was
used for diagnosis. Full corrected-commit gate and push are queued after the
preceding frozen validation; no deployment or real-provider retry is claimed.


### DAR-133 fresh scoped remote observations — 2026-10-01

The opt-in remote info endpoint now probes only the authenticated peer's permitted
model/cloud catalogue before any provider discovery. The CLI uses normal privacy
transports and bounded inventory requests, shared per provider/locality within one
request, never cached across calls. Model states distinguish present, absent and
unknown; missing credentials or failures do not become positive availability.
Configured capabilities remain configuration claims, not weight/tool attestation.

Info also reports a timestamped normal-host-profiler RAM snapshot, with absent
values for unknown measurements. It grants no reservation or execution authority;
dispatch rechecks admission and the existing available field means dispatcher
health. Stale/future observations and inconsistent resource reports fail closed.
Inputs/outputs are copied so observers cannot mutate the configured catalogue.

Tests cover fresh lookup after model removal, per-request provider sharing,
missing credentials, provider failure, cancellation, scoped discovery through
actual mTLS, and stale/invalid observations. Full focused remote race suite with
native isolated SSH enabled passed (20.632s); CLI discovery race tests passed
(1.488s). No live service or SSH account changed. Full repository validation and
push are queued behind prior frozen gates. Physical two-host qualification,
native-harness capability attestations, pairing UI, automatic remote joint
selection and operational rate/retention policy remain open.

### DAR-133 — caller-owned remote task inventory

Added `remote.Client.Tasks`, `GET /v1/remote/tasks` and `nexus-remote tasks` with
request-ID pagination. The destination queries only the authenticated caller's
retained control-journal requests under the existing inspect scope, and projects
current lifecycle metadata without prompts, output text or configuration digests.
Unbound/lost-response reservations and temporarily unavailable backend status
remain explicitly unknown. Listing never dispatches or retries work. This is a
live ordered traversal; new keys before an earlier cursor require a fresh scan.

Focused tests verify exact caller separation, bounded 100-item pages, malformed
cursor rejection at both client and server, permission revocation, audit records,
cancellation visibility, unknown delivery and no replay. Native OpenSSH loopback
qualification now includes task inventory. Full remote/CLI race tests with native
SSH enabled passed (20.774s/1.367s), as did targeted vet, source formatting/size and
diff checks. Full repository validation and push are queued behind the preceding
remote-observation checkpoint; no full-gate pass is claimed for this change.
Physical two-host qualification, device discovery/pairing UI, automatic remote
accuracy-first selection and operational policy remain incomplete.

### DAR-133 — explicitly scoped remote external harness dispatch

Remote requests can now name a configured external harness registration and
explicit task difficulty. Both caller and destination peer registries require an
additional harness allowlist; existing model permissions do not grant external
harness access. The backend rejects a registration bound to another model and
passes the exact harness/difficulty into the ordinary durable SDK submission.
Optional JSON fields preserve old native request hashes. Automatic remote
selection remains unsupported rather than falling back or bypassing peer scope.

Scoped discovery exposes permitted registration metadata without executable,
source or credential paths. The remote host now opens the configured evidence
store and attaches it to SDK and dispatcher before serving; external registrations
require that evidence directory. The integration test initially caught missing
ledger attachment; this is fixed. Native requests use the supported 8192-token
fixture budget after the 4096-token native fixture was denied before execution.
No production admission constraint was weakened.

Installed Pi over actual isolated OpenSSH plus pinned mTLS and a local synthetic
provider passes durable intake, lost-response recovery, exact duplicate handling,
result/events with actual Pi/model revision/hard difficulty, evidence recording,
and queued/running cancellation. It is a protocol fixture, not real-model quality
or a physical two-host claim. Scope, registration/model mismatch, legacy digest,
invalid auto/difficulty and disclosure tests also pass. The complete remote/CLI
race suite with native Pi and SSH enabled passed (31.083s/1.725s); targeted vet,
source formatting/size and diff checks passed. Full repository check/push is
queued after the task-inventory checkpoint. Other four harnesses inherit the
shared SDK path but are not claimed remotely qualified by this Pi fixture.

### DAR-133 — five installed harnesses through native SSH

Extended the durable remote lifecycle fixture to operator-pinned Pi, OpenClaw,
Goose, OpenHands and Hermes registrations, each using an isolated SSH server,
mTLS, a synthetic local provider and disposable journals/evidence. Pi (9.98s),
OpenClaw (15.40s), Goose (9.83s) and Hermes (11.90s) passed the initial matrix.
OpenHands initially failed after launch: its SDK rejects an 8192-token context
below its required 16384. A fixture-only stderr overlay identified that precise
exception; production stderr handling was unchanged. The OpenHands adapter now
rejects smaller contexts before its admission callback/process launch, with a
regression test. The corrected OpenHands remote case passed (12.31s). Original
failure/diagnostic logs remain in reporting outputs.

All five qualified cases exercise committed-intake and queued-cancel response
loss, same-key recovery without duplicate execution, results/events and exact
actual harness/model revision/difficulty, evidence recording and running cancel.
These are protocol/lifecycle fixtures, not model quality or physical two-host
qualification. Host tools remain covered separately; this matrix is text-only.
The OpenHands race package passed (2.151s); targeted vet/source/diff checks passed.

Consolidated the three waiting remote validation supervisors before any began
running tests. Their prior phases/commits are preserved with superseded status.
One full check of this descendant branch will cover all remote changes after the
currently running e44dacf harness gate, followed by a normal branch push if clean
and successful. No active gate, inference coordinator, provider resident or live
service was stopped. Automatic remote accuracy-first selection, pairing UX,
physical two-host tests and operational policy remain open.

### DAR-133 — bounded per-peer remote request rates

Added destination-side fixed one-minute allowances per authenticated peer and
operation, with bounded defaults and explicit validated registry overrides.
Dispatch, info, inspect and cancellation use independent allowances; throttled
requests return 429/Retry-After without backend execution or journal reservation.
The Go client exposes ErrRateLimited and never retries automatically. Live trust
updates preserve consumed counts; revocation still authenticates first. Counter
storage is bounded by registered peers/operations, with removed-peer pruning.
Only the first throttled request per operation/window is audited to avoid journal
amplification. Existing global concurrency limits still apply; cancellation has
an independent rate budget, not a reserved execution lane.

Remote/CLI race tests passed (10.680s/cached), targeted vet, source formatting/size
and diff checks passed. Tests exercise concurrent exact allowances, expiry,
policy changes, peer isolation, no dispatch after throttling, cancellation,
bounded audit and revocation. The initial revocation assertion expected an HTTP
denial, but fresh connections are rejected during TLS; the test now checks
failure without mistaking throttling for revocation. No production permission
check was weakened. Native OpenSSH loopback plus the new limit tests passed (1.853s).

Counters reset on restart and are not durable billing quotas or deployment-wide
rate coordination. Retention, physical two-host validation, discovery/pairing UI
and automatic accuracy-first remote destination selection remain open. Full
repository validation and normal branch backup are queued before completion.

### DAR-133 — context-bound remote harness identity preview

Added `nexus-remote harness-identity` and the matching Go/HTTPS/SSH discovery
operation. It derives the exact effective native harness identity for a scoped
model/registration/context through the same constructor used by execution.
Client and server enforce model/harness/context scopes; destination rejects cloud
metadata outside peer scope and inconsistent registration/model identity. The
existing info permission, rate allowance and durable operation audit apply.
No task, reservation, provider connection, secret lookup or subprocess is created.

All five adapter previews passed context-isolation and constructor-identity tests
(1.629s race package). Fixtures deliberately use nonexistent executables, no
provider server and a secret lookup that fails if called. The first test fixture
incorrectly enabled native tools with an empty tool catalogue; corrected the
fixture to include the normal read tool, preserving production validation.
Remote/CLI native-SSH race suite passed (20.705s/1.611s); targeted vet and source
checks passed. Identity endpoint scope/no-dispatch tests also run through actual
isolated OpenSSH as well as HTTPS. This is same-host protocol qualification,
not physical two-host evidence.

Responses are configured identities, not installation/availability attestations
or authenticated quality scores. Cross-instance evidence provenance, durable
chosen destination and automatic ranking remain unimplemented. Full repository
validation and backup of this descendant checkpoint are queued behind the
active harness gate; no active gate or comparison input was edited.

### DAR-133 — expected harness identity through durable dispatch

Added optional expected_harness_identity to remote dispatch and the SDK request.
It is validated/copied during ordinary native admission and rechecked against the
actual effective configuration before native execution. Explicit model/harness
and context are required; native-without-harness and auto reject pins rather than
ignoring them. Remote request hashes bind the field while absent fields preserve
legacy encoding. SDK queue payloads retain it across durable dispatch. Submission
contract generation 7 fences prior queued interpretations; upgrade boundary is
documented and no live queue was changed.

Installed Pi durable queue qualification passed (16.619s race), including wrong
identity rejection before provider traffic, exact actual identity on completion,
duplicate handling, registration drift, and queued/running cancellation. Automatic
follow-on evaluation explicitly clears the prior explicit pin. All-five preview/
pin admission and queue-generation focused tests passed (3.128s). Remote/CLI
native-SSH race packages passed (20.934s/1.704s), targeted vet/source checks passed.
The remote installed-Pi SSH test now pins identity and compares canonical completed
provenance exactly, in addition to its lost-response and cancellation checks;
that installed-Pi SSH qualification passed (11.557s race package).
Full repository validation/normal backup remain queued behind the active harness
gate. Per-instance evidence provenance and durable automatic destination selection
remain the next integration work; this is not a completed cross-instance ranker.

### DAR-133 — durable caller destination bindings

Added private caller RouteStore, DispatchRecorded, CLI dispatch --routes and
route-binding inspection. Immutable records bind request ID to destination,
caller leaf-certificate fingerprint and exact task digest before dispatch.
Synced temporary files publish through atomic no-replace hard links, followed by
directory sync; store opening also syncs its existing parent. Exact retries can
reopen safely, while changed destination/task/caller, corrupt records and symlinks
fail closed. The actual TLS client certificate is checked against the saved pin,
including if credentials change after the initial read. No prompts or credentials
are stored. Filesystem failures prevent dispatch; there is no automatic cleanup,
rerouting, retry or overwrite. Rotation/reconciliation and retention boundaries
are documented; callers must preserve the same store for retries.

Concurrent competing choices, reopening, lost-response recovery, altered payload,
caller pin and unsafe-file tests passed (1.683s race). Full remote/CLI race tests
with installed Pi and native OpenSSH passed (31.864s/1.438s). The real SDK lifecycle
now records caller choices, reopens them after committed-intake socket loss and
recovers the same submission, checking exact pinned actual identity and queued/
running cancellation. Targeted vet, source and diff checks passed. These are
same-host fixtures, not physical two-host or power-loss filesystem qualification.
Full repository validation and backup are queued. Automatic ranking and trusted
cross-instance evidence remain separate unfinished requirements.

### DAR-133 remote outcome reconciliation

Added caller-side `RecordedOutcome` / `VerifiedOutcome.Record` and CLI `reconcile`.
Saved destination/caller/task bindings and exact harness identity are checked
against authenticated succeeded status, complete stable-head event pages,
submission/context/prompt/privacy and output. Receipts precede immutable execution
writes in destination/caller-separated ledgers. Completed output creates pending
evidence, never automatic quality credit or imported remote scores. This advances
the evidence prerequisite; bound reviews, cross-instance automatic ranking,
pairing UI and physical two-host qualification remain open. Native Pi SSH reconciliation and the remote/CLI race suites passed (31.910s /
1.807s), with focused caller-change rejection (1.491s), vet, source formatting
and diff checks. Full repository validation is still required before push.

### DAR-133 bound remote review integration

Added operator/evaluator-only `ReviewRecordedOutcome` and `nexus-remote review`.
Reviews bind both the destination/caller/request/event receipt and exact execution;
fresh authenticated canonical reads precede writes. Separate destination ledgers
retain expected-current-head revisions, identical retry behavior, withdrawals and
AI advisory classification. No remote write endpoint, evaluator inference or
imported score totals are introduced. CLI requires strict owner-private review
files. Revision/race/binding tests passed, including stale concurrent updates and
withdrawals. Native Pi/OpenSSH remote and CLI race packages passed 33.471s /
1.483s; vet, source formatting and diff checks passed. Full make check and push
remain queued behind the running preceding gate. Automatic evaluation scheduling, cross-instance ranking,
pairing/discovery UI and physical two-system qualification remain open.

### DAR-133 destination-specific accuracy ranking

Added `harness.SelectScoped` and `Client.RankRecordedCandidates`, preserving
ordinary eligibility/accuracy ordering while separating identical configurations
on different nodes. Ranking reads only caller-owned destination evidence, checks
paired scope and fresh exact configured identity, and returns chosen configuration,
samples and exclusions without dispatch. Missing evidence stays unknown; corrupt
existing ledgers fail closed. A read-only evidence opener cannot append or create
a database. Host-supplied capability/capacity/credential checks remain explicit
requirements, not inferred from raw free RAM. Discovery/admission orchestration,
persisted automatic dispatch and UI integration remain open. Focused isolation,
ordinary-score parity, exploration, read-only/WAL and remote binding tests passed;
native SSH-enabled race suites passed: harness 1.583s, remote 33.782s, CLI
1.705s. Vet, source formatting and diff checks passed. Combined full repository
validation and normal branch backup remain queued behind the live earlier gate.

### DAR-133 durable automatic remote dispatch

Added `AutomaticRequest`, immutable `AutomaticChoice` and `DispatchAutomatic`.
New requests rank then persist destination/caller/intent/exact identity and selected
score provenance before route binding and network dispatch. Retries reconstruct
the saved task without reranking; two concurrent node proposals share one winner.
Lost responses, changed candidates, admission errors and policy changes never
trigger alternate dispatch. Corrupt/manual/changed-intent/changed-caller bindings
fail closed. Deterministic two-endpoint race and recovery tests passed (1.865s);
installed Pi plus native SSH/SDK integration passed in the remote/CLI race suite
(34.281s / 1.628s). Pre-route-binding crash recovery passed (1.582s); vet, source
formatting and diff checks passed. Full repository validation/push remains queued. Host candidate admission
collection, evaluator scheduling, UI and physical two-host qualification remain.

### DAR-133 measured remote harness capacity

Added scoped `harness-capacity` CLI/HTTP/Go observations and same-dispatcher-service
capacity planning using context-scaled model memory plus fixed harness overhead.
Cloud harnesses still account for their local process. Two-second bounded planning
reads live reservations without allocating resources or running a provider.
Automatic ranking now requires the measured admit result; caller flags cannot
bypass wait/unknown capacity. Native SDK/OpenSSH tests use the same planner and
worker service and verify its running reservation is visible. Remote/CLI race
suite passed (34.887s / cached); context/overhead/cloud and inconsistency tests
passed (1.962s / 1.485s). Final SSH capacity/ranking tests passed 2.322s;
vet/source/diff passed. Combined full repository validation and push remain queued.
Capability/credential observation, evaluation scheduling, UI and physical two-host
qualification remain open.

### DAR-133 remote harness readiness

Added scoped `harness-readiness` CLI/HTTP/Go observations and same-service
executable-pin, credential-presence and model-inventory checks. Two-second
callback context and fifteen-second freshness apply. Negative observations cannot
be overridden by positive caller flags during automatic destination selection;
context/capabilities intersect and cost retains the larger estimate. No inference,
reservation or quality evidence is produced. Runtime dependency/version/tool
attestation remains an execution responsibility.

Remote/CLI race suites with native SSH and Pi passed (36.465s/1.682s), including
negative-readiness ranking tests. App prerequisite test passed (1.711s); its first
fixture omitted mandatory harness overhead and was corrected. Vet/source/diff
checks passed. An earlier broader app test process remains live; its binary
contains the pre-correction fixture and is not a passing validation claim.
Full repository gate and push will queue behind the running capacity revision.
Evaluation scheduling, pairing/discovery UI, physical two-host qualification and
production held-out model/harness comparisons remain open.

### DAR-133 discovery-driven automatic routing

Added a configuration-only paired catalogue and bounded candidate discovery.
Ineligible cloud/policy/budget combinations are excluded before readiness or
capacity probes; changed caller credentials/trust abort discovery. Candidate
collection measures prerequisites and capacity, while final ranking rechecks and
uses caller-owned destination evidence. CLI catalogue/candidates/rank/auto-dispatch
and offline automatic-choice operations expose the path. CLI exploration remains
explicitly unsupported; the existing Go evaluator API retains bounded policy.

Recovery checks the durable choice before discovery, preserving same-key replay
without fallback when metadata becomes unavailable. Native Pi/OpenSSH SDK tests
now use discovery for the first automatic dispatch. Remote/CLI race packages
passed (36.635s/1.777s); final policy-change/cloud/no-probe/CLI input tests passed
(2.729s/1.458s), with vet/source/diff checks. Full make check and push remain queued
behind running capacity validation. Pairing/discovery UI, background evaluation,
physical two-host and production held-out qualification remain open.

### DAR-133 automatic result reconciliation and review

Added original-request APIs and CLI commands for automatic status, cancellation,
verified output, reconciliation and bound content review. Both durable choice and
route binding must agree with the saved request; original caller identity is
retained. These paths never discover, dispatch, repair missing bindings or reroute.
Verified output is available in memory for evaluators; persistent evidence still
contains only hashes and attribution. Completion adds no quality vote, repeated
bound review counts once, and existing expected-head/advisory review rules apply.

Remote/CLI race packages passed (38.044s/1.483s), including installed Pi over real
isolated OpenSSH using the new automatic outcome/review APIs, changed-intent and
choice corruption rejection, credential rotation, cancellation, and no repeated
inference. Initial rotation test expected denial rather than the existing identity
conflict error; corrected assertion before the passing suite. Vet/source/diff
checks passed. Full make check/push remains queued behind running capacity work.
Background evaluation, UI, physical two-host and held-out production qualification
remain open; evaluator identity and truthful content grading are host duties.

### Submission version 7 branch/resume regression

The full 810648d repository gate exposed HTTP resume returning invalid_submission.
Reproduced independently: application envelopes had advanced to version 7 for
remote harness identity pins, while both durable branch/resume parsers still
accepted only versions 2–6. Extended those explicit allowlists to version 7;
historical readability, canonical request checks, source/privacy fences and the
application's exact current configuration fence remain intact. No live queue was
upgraded or resumed.

The failing authenticated API test now passes (4.253s package). New durable
admission matrix accepts branch/resume versions 2–7 and rejects versions 1/8
(3.161s). Four application branch/resume execution/restart/source-drift tests pass
(12.063s), with vet/source/diff checks. The older full check is still running and
its failure remains preserved. A corrected descendant full check must run before
push; targeted passes do not relabel the prior failure as a successful gate.

### DAR-133 remote commands in the release executable

Moved the remote CLI implementation/tests into internal/remotecli and wired
`nexus remote` through the normal CLI input/output and signal lifecycle. The
standalone nexus-remote command remains a thin compatibility entry point. Existing
release builds already package cmd/nexus and derive its exact dependency closure,
so remote support no longer depends on a separately built companion executable.
No remote listener starts implicitly; explicit trust/configuration remains required.

Shared CLI and main-entry integration race tests passed (1.427s/1.613s), including
private expected-digest trust replacement, validation, help and supplied stderr.
The first test fixture lacked a private parent directory; corrected its permissions
without weakening trust validation. All four release targets (macOS arm64/amd64,
Linux amd64/arm64) compiled with CGO disabled; native packaged remote help returned
success. Standalone compatibility help and all shared CLI tests also passed.
Vet/source/diff passed. Full repository validation/push remain queued. This is
build inclusion, not signed release publication, native service installation or
physical two-system qualification. OpenSSH is still an external SSH prerequisite.

### DAR-133 current remote review inspection

Added validated snapshot review-state copies and `nexus remote auto-review-state`.
The original route and completed canonical output are authenticated before reading
only that caller/destination's local ledger. Missing evidence is reported without
creating a store. Current head IDs support deliberate expected-head revisions;
inspection does not reserve the head or authorize an overwrite. Classification
separates pending/unverified/withdrawn/advisory/confirmed/non-quality evidence.
No output text, quality judgment, evaluator call or inference is produced.

Remote/harness/shared-CLI race packages passed (38.110s/1.923s/1.933s), including
installed Pi over native isolated SSH. Tests cover missing-store noncreation,
inspection-driven revision, stale-head conflict, immutable snapshot copies and
all classifications. Vet/source/diff passed. Full repository gate/push queued;
background evaluator scheduling, UI, physical second-host and production held-out
qualification remain outstanding.

### DAR-133 — local control-audit inspection (2026-10-01)

Added `nexus remote audit` and `remote.ReadAuditPage` for administrators to read
existing private control journals without SQL tooling, runtime startup or network
credentials. Read-only/query-only transactions return at most 100 bounded records
per page, with a fixed upper sequence for traversal during concurrent appends.
Missing stores remain missing. Instance identity, cursor, path/permission and
record checks fail closed. Replay reservations and all audit history are retained.
This is inspection, not automatic retention, tamper-proof export or peer access.

Focused race tests passed (remote 1.724s, shared CLI 1.933s), covering stable
pagination under new appends, repeatability, preserved request binding/conflict,
missing-store noncreation, unsafe paths, invalid cursors, identity mismatch and
oversized records. Full remote/shared-CLI race packages passed (14.353s/1.785s), and
vet/source/diff checks passed. Byte bounds also reject embedded-NUL oversized
SQLite text. Full repository validation and push remain queued
behind the existing gate. Physical second-host qualification, operational archive
retention, pairing UI and remote evaluator scheduling remain open.

### DAR-133 — explicit archive-bound control-audit retention (2026-10-01)

Added local administrator `audit-archive` and `audit-prune` operations. Export
captures a consistent oldest prefix of up to 10,000 entries into canonical private
JSON, syncs it and publishes without overwriting another archive. Prune requires
its exact hash and compares the entire current prefix before deleting audit rows
and appending an archive-hash marker in one transaction. Requests, submission
bindings, runtime evidence and learning ledgers remain untouched. Same-archive
retry recognizes a retained marker; a marker already archived by subsequent
maintenance yields a conflict. No maintenance was run on live data.

Remote/shared-CLI race suites passed (15.977s/1.782s); vet/source/diff passed.
Tests cover new events after export, retained replay identity/conflict, exact
archive retries, marker failure rollback, tamper/omission/hash/instance rejection,
unsafe archive paths, canceled operations, 10,000-entry batching, no overwrite,
missing-journal noncreation and CLI hash requirements. This provides explicit
retention with operator-managed archive backups, not scheduled deletion, SQLite
file compaction, cryptographic administrator attestation or replay-ID expiration.
Full make check and push remain queued behind the live earlier gate; physical
second-host qualification, pairing UI and background remote evaluation stay open.

### DAR-133 / DAR-132 — durable remote evaluator attempts (2026-10-01)

Added trusted embedding-host APIs `EvaluateRecordedOutcome` and
`EvaluateAutomaticOutcome` using the existing evaluator extension contract.
Canonical output is reauthenticated before evaluation; exact input/receipt,
evaluator descriptor, locality and timeout are durably bound before the sole
invocation. Concurrent/restarted calls never replay an uncertain evaluator.
Validated terminal results persist before ledger reconciliation, allowing a
later authorized call to repair feedback without invoking the evaluator again.
Private tasks reject nonlocal evaluators. Failures/abstentions never become passes;
accept/reject remains advisory AI evidence. Concurrent operator heads win.

Focused remote evaluation/retention race tests passed (5.952s), including explicit
policy conflicts, one-call/one-sample retries, concurrent admission, missing
terminal persistence, evaluator panic/failure/abstention, private-data rejection,
revocation between evaluation and feedback, and operator review races. The common
bounded private-file publication helper now serves archives and evaluator records;
retention regression tests remain included. Full remote/shared-CLI race packages
passed (18.570s/1.937s), and vet/source/diff passed. Main CLI evaluator configuration,
background scheduling, review UI and production evaluation qualification remain
open; no live task or evaluator inference was run. Full repository gate/push
remains serialized behind the current capacity validation.

### Release licensing regression — progress-file snapshot limit (2026-10-01)

The older full gate and a focused latest-source reproduction both failed
TestFreezeAndVerifyLicenseEvidenceFromCleanCommit. Temporary stage diagnostics
located the rejection in immutable source snapshotting: progress.md had grown to
1,066,362 bytes, exceeding the unchanged 1 MiB per-blob capture bound. No dependency
license, trust requirement or release limit was relaxed. Removed the temporary
diagnostics and split the original progress file into bounded historical parts;
verified their ordered concatenation is byte-for-byte identical to the original.
The main progress file retains recent checkpoints and links the preserved history.
Clean-commit license freeze/verification is being revalidated on this correction.

### DAR-132 / DAR-133 — configured remote content evaluator CLI

Added explicit `nexus remote evaluate` and `auto-evaluate` commands to connect
verified remote output and durable evaluator admissions to configured providers.
The original task/automatic request and saved route remain authoritative; no
original inference or discovery is replayed. Required reviewer/config/cost flags
bind evaluator policy, private tasks prohibit cloud evaluators, and local review
uses shared auxiliary resource reservations with provider cleanup before release.
Known secrets are redacted, tools are absent, and review has a one-minute/4096-token
bound. Estimated cost checks do not claim measured billing guarantees. Valid
advisory results reconcile through existing current-head protections, and CLI
status remains visible if reconciliation fails after an admitted attempt.

Verification: provider-backed remote integration called twice produced one
reviewer HTTP call, one original dispatch and one advisory sample with zero
confirmed samples. Full remote and shared CLI race suites passed (18.263s and
1.702s); configured evaluator admission/redaction/reservation race tests passed
(1.576s). Targeted vet, source format/size and diff checks passed. Invalid CLI
input tests cover unknown/trailing/oversized JSON and missing/nonfinite policy
values without creating route/evidence state. Integration uses synthetic local
provider output, not real-model grading accuracy or physical two-host validation.
Full repository gate/push is pending behind the active earlier validation job.
Background review scheduling, review UI, physical two-system qualification and
production accuracy comparison remain incomplete. No running service changed.

### DAR-132 / DAR-133 — shipped evaluator CLI process qualification

Added a test building the actual `cmd/nexus` entry point and launching separate
CLI processes against a real loopback mTLS remote server, private saved route /
trust / credential files, a YAML evaluator config and a synthetic Ollama reviewer.
Both `remote evaluate` and `remote auto-evaluate` return completed/applied status
on the initial invocation and on a new-process retry, with only one evaluator
request per receipt. The tests independently inspect advisory-only learning and
original dispatch counts. A changed automatic prompt is rejected without another
provider request or dispatch. This closes the prior main-CLI integration gap;
it does not qualify real evaluator accuracy or a physical second system.

The initial automatic fixture reused the already-reviewed canonical execution
from the direct test and correctly encountered a current-head conflict before
reviewer dispatch. The final test uses a fresh destination fixture to exercise
both independent CLI paths without weakening the execution/head invariant.
Focused race qualification passed (3.073s package), as did targeted vet, source
format/size and diff checks. Full make check and conditional normal push remain
queued behind the running earlier gate; no deployed service was changed.

### DAR-133 — explicit local peer membership operations

Added `nexus remote peers`, `pair` and `revoke`, backed by `TrustFile.Pair` and
`Revoke`. Operators can inspect registry plus canonical digest, add one verified
and explicitly scoped peer, or remove one peer without reconstructing unrelated
entries. Existing atomic replacement/current-digest checks remain authoritative;
existing IDs, pin collisions, stale writers and missing removals fail closed.
Pairing is offline administrator registration after trusted identity exchange,
not automatic discovery, certificate issuance, or a pairing UI. Credential
rotation/scope updates still use explicit whole-registry replacement. Revocation
blocks future requests without retroactively canceling admitted work.

The CLI shares strict bounded JSON decoding for pair and replace-trust; unknown
fields no longer disappear silently during whole-registry decoding. No remote
membership write endpoint or live-machine trust change was introduced.
Tests cover preserved scopes, failed-update preservation, concurrent writers,
strict CLI inputs and live mTLS revocation. The initial revocation fixture lacked
the private parent-directory permissions required by atomic replacement; fixed
fixture permissions without changing production policy. Full remote/CLI race
suites passed (20.464s / 2.006s), targeted vet, source format/size and diff checks
passed. Full repository validation and push remain queued. Discovery/pairing UI,
physical two-system qualification and other recorded DAR-133 gaps remain open.

### DAR-132 / DAR-133 — bounded remote completion/review watcher

Added `Client.WatchRecordedEvaluation` plus `nexus remote watch-evaluate` and
`auto-watch-evaluate`. A host or supervised process can wait for an explicitly
selected saved request, checking fresh authentication and immutable route intent,
then automatically review successful canonical output through the durable
one-attempt protocol. CLI polling is 15 seconds with an explicit positive total
wait/review deadline capped at 24 hours. Failed/canceled tasks return distinct
ungraded statuses; unknown state, identity change, revocation or transport errors
stop observation. No discovery, task retry/cancellation, missing-result reclaim
or automatic evaluator retry is introduced. Restarts preserve replay protection
only with the same request, routes, evidence root and evaluator policy.

Race tests exercise queued-to-running-to-success, repeated watcher calls with
one evaluator invocation and one original dispatch, failed/canceled/unknown
states, changed intent, revoked trust and deadline exhaustion with no quality
writes. Actual main CLI process tests also cover both watch command spellings
reconciling prior durable results without provider replay. Full remote/CLI race
suites passed (22.989s / 1.971s); targeted vet, source format/size and diff passed.
Full repository check and conditional push are queued. This is per-request
supervised review, not whole-fleet automatic enrollment, service installation or
review UI; those and physical two-host/real-model qualification remain open.

### DAR-133 — native SSH review watcher and shipped CLI qualification

Extended the shipped evaluator CLI subprocess test with opt-in actual OpenSSH
transport (`NEXUS_REMOTE_EVALUATION_SSH=1`). Both direct and automatic evaluation
and watch commands now run through isolated sshd plus pinned mTLS; repeat CLI
processes reconcile the same advisory result without new evaluator or original
inference calls. Added a native SSH watcher test covering queued/running/success,
restart reconciliation, wrong host-key rejection with zero remote polls (despite
a directly reachable HTTPS endpoint), and NexusRouter revocation through SSH.
Transport failures write no review evidence. Disposable keys/listener only;
system SSH service and live routing configuration remain untouched.

Native qualification passed with `NEXUS_REMOTE_SSH_NATIVE=1` and
`NEXUS_REMOTE_EVALUATION_SSH=1`: watcher 2.95s, main CLI 3.99s, race package 8.540s.
Targeted vet, source format/size and diff checks passed. This qualifies actual
SSH lifecycle on one Mac with synthetic evaluator output, not physical two-host,
network partition, cross-platform or model-quality behavior. Full repository
gate/normal push remain queued with the preceding implementation checkpoints.

### DAR-132 / DAR-133 — combined remote dispatch and review workflow

Added `nexus remote dispatch-evaluate` and `auto-dispatch-evaluate` so submitting
a request can enroll its successful output for bounded automatic content review
without a second CLI command. Reviewer configuration/privacy/declared-cost policy
is checked before original dispatch; actual evaluator resources/credentials are
admitted only after canonical completion. Automatic routing uses the existing
accuracy-first discovery/default policy with exploration disabled. The original
request key, route/choice and durable evaluation attempt remain authoritative on
restart; no alternate destination or second evaluator invocation is introduced.
Output separates dispatch, optional saved choice and evaluation, preserving a
useful phase even if review fails after successful submission. Failed/canceled
original tasks remain ungraded and observer exit does not cancel them.

Extended actual cmd/nexus subprocess qualification to begin with a fresh direct
request and a freshly discovered automatic route, then repeat combined commands
and standalone review/watch commands. Original dispatch/evaluator counts remain
one per new request; bad reviewer policy fails before dispatch; changed automatic
intent is rejected. Native SSH-enabled full remote/CLI race suites passed
(40.826s / 1.766s); targeted vet, source format/size and diff checks passed.
Synthetic provider/loopback evidence does not establish real-model accuracy or
physical two-host behavior. Full repository gate/push queued. Global enrollment
of other clients' tasks, installed background service and review UI remain open.

### DAR-132 / DAR-133 — reject unusable review storage before dispatch

A shipped-CLI regression demonstrated that direct dispatch-evaluate accepted an
absolute but publicly accessible evidence root, submitted original work, and only
then failed review persistence (backend submits rose from fixture baseline 1 to
2, reviewer calls stayed 0). Added a common storage preflight before either
direct or automatic submission. It reuses the strict private-root checks and
verifies synced temporary-file creation/removal without generating receipts,
evaluation attempts or quality votes. Existing post-completion checks remain;
preflight does not promise immunity to later capacity/permission/I/O changes.

The same CLI regression now passes with zero extra dispatch/evaluator calls.
Focused race tests also cover private-root reuse, no residual records, public
mode, regular-file and symlink roots, relative paths and missing parents.
Race package passed 4.211s; targeted vet, source formatting/size and diff passed.
Original failure is retained in this note; no production inference was involved.
Full repository validation and conditional push remain queued. This fixes an
admission-order defect, without changing evidence ownership or grading policy.


### DAR-133 — authenticated browser membership (2026-10-01)

Added opt-in `web_ui.remote_trust_file` and a Settings membership panel for
configured HTTPS/SSH peers. Existing browser session/CSRF and strict JSON guards
protect pair/revoke, with explicit identity verification and expected-current
registry digests. The configured path is fixed at daemon startup; browser input
cannot choose it. Existing private registry required; no discovery grants,
credential creation, network probes or service deployment. Conflicts/uncertain
writes clear actionable stale UI state until refresh. Revocation preserves
unrelated peers and does not cancel already admitted work.

Verification: complete internal/webuiapp, internal/config and webui race packages
passed (4.360s / 4.486s / 9.495s), including real private-file CAS, rejected
unauthorized/CSRF/foreign-origin/unverified/duplicate-field inputs, missing/public
registry denial, SSH preservation and browser no-replay recovery behavior.
Updated reviewed embedded-asset digest; resource/CSP/storage restrictions still
pass. Initial new GET fixtures incorrectly carried empty bodies and were fixed
to use the existing strict browserGET helper. Targeted vet and source gate passed.
Full repository validation/push will follow the active preceding check; no full
pass or live deployment is claimed. UI currently accepts a complete peer object;
guided discovery/onboarding, live capabilities/tasks, separate-machine and
cross-platform native qualification remain open.

The preceding 810648d full gate finished with the known branch/resume and license
snapshot failures plus two SDK branch/resume cases with the same version fence.
Both exact SDK failures passed on this descendant with race detection/count=1
(6.903s); existing version-seven correction suffices. The preceding gate remains
failed, not relabeled. Its replacement 3d1a065 full gate is freshly live and is
not interrupted by this UI work.

### DAR-133 — explicit browser remote inspection (2026-10-01)

Added opt-in fixed remote_client certificate/key/CA paths alongside the existing
trust registry. Settings can explicitly inspect each HTTPS/SSH peer's live
capabilities/resources or caller-owned task pages through the normal remote
client. No automatic network probes, task dispatch, cancellation or retry.
Session/CSRF, closed bounded requests, ten-second context, operation slots,
transport pinning/scopes and fresh revocation remain enforced. The configured
certificate is the caller shared by authorized browser operators; no per-session
remote identity is implied. Results remain advisory and are cleared on failure.

Verification: webuiapp race package passed 4.162s, config 4.796s, webui 9.142s.
Includes an actual browser-handler -> remote.Client -> mutual-TLS server fixture
for capability/task reads and revocation before further contact. Initial fixture
omitted required X-Nexus-Instance response header; client correctly denied it,
fixture corrected without weakening validation. Browser behavior tests prove no
implicit network calls, exact pagination cursor, restart traversal on refresh,
no retry and stale-result clearing. Authority tests reject missing CSRF,
foreign-origin, unsupported operations and browser-supplied endpoints; disabled
configuration cannot probe. Targeted vet/source/diff passed. Reviewed embedded
asset digest updated; full check/push queued after the running preceding gate.
No live credentials/service configured. Guided discovery/onboarding, browser
remote task controls, physical two-host and cross-platform qualification remain
incomplete, as does real-model remote accuracy qualification.

### DAR-133 — browser remote task controls (2026-10-01)

Added separately opt-in remote_task_controls, requiring existing remote client
credentials. Each inspected task page can load current status/successful output
and request cancellation only after current active submission inspection and
explicit user confirmation. Browser BFF re-reads original request, checks expected
submission ID and active state, and calls cancellation once. Same session/CSRF,
fixed credentials, bounded input/context, separate control slots, fresh peer
scope/revocation and destination ownership remain authoritative. No task dispatch,
automatic retry or inferred cancellation success. Uncertain response clears the
cancel capability until explicit status reload; terminal tasks cannot be canceled
from this UI. Status is manual rather than event streaming.

Verification: webuiapp race package passed 4.180s; config 4.555s; webui 9.236s.
Tests cover authority/invalid IDs, changed submission/terminal rejection,
projection without configuration digest, explicit-only control, uncertain-response
no replay, and completed result text. Real mutual-TLS browser-client fixture
initially denied cancellation because the peer had only info/inspect scope.
Preserved that rejection as a regression, then explicitly added cancel scope to
verify one authorized call and fresh revocation preventing further calls. Reviewed embedded assets retain
CSP/resource restrictions. Full repository check/push will follow the unchanged
running gate; no live deployment. Guided onboarding, browser dispatch/event
streaming, physical two-host/cross-platform and real-model qualification remain.

### DAR-133 — guided HTTPS/SSH pairing (2026-10-01)

Replaced JSON-only peer entry with grouped identity, transport, scope, privacy,
context/cost and optional request-limit fields. Exact configuration preview
updates on edit. Verification clears when any field changes and after an
uncertain/conflicting write. Defaults grant info only, no model/harness access
and no private/public-network/cloud permissions. SSH and custom-limit fields
are included only when explicitly selected; fingerprints normalize to the
existing canonical lowercase format. No discovery, connection, credential read
or trust-on-first-use occurs while filling the form. Server peer validation and
existing expected-digest/session/CSRF guards remain unchanged.

Verification: full webui race package passed 9.461s; webuiapp race package passed
4.196s. Focused guided form/consent/browser tests passed; a Node-to-Go contract
test generates HTTPS and SSH peers with the actual form builder and validates
both with remote.Peer.Validate. Covers explicit scopes, optional-field exclusion,
duplicate IDs/fingerprints, invalid ports/numerics, verification reset and
uncertain-save no replay. New module included in reviewed asset digest and
resource/CSP guard lists. An example URL placeholder triggered the existing
literal external-resource guard and was replaced with descriptive text; guard
unchanged. Targeted vet/source/diff passed. Combined full check and normal push
remain queued behind the live preceding gate. No live trust/service edits.
Unpaired discovery, physical two-host onboarding and other remote qualification
requirements remain open.


## 2026-10-01 — DAR-133 inline remote confirmations

Replaced browser-native confirmation dialogs for cancellation and peer revocation with accessible inline groups naming the request/peer and effect. Opening/dismissing makes no request; status/membership refresh invalidates confirmation, busy guards prevent duplicate submissions, and uncertain cancellation still requires status reload. Existing backend authentication, CSRF, scopes, expected-submission and registry-digest checks are unchanged.

Web UI race suite passed (9.755s), targeted vet/source/diff checks passed after explicitly updating the reviewed embedded asset digest. Behavior tests cover dismissal, stale confirmations, duplicate clicks and uncertain responses. A real in-app browser exercised production assets against a disposable synthetic HTTP API: exactly one cancel despite a simulated applied-but-503 response, status recovery showing cancellation requested, and one explicitly confirmed revocation. Reporting evidence: outputs/remote-settings-inline-qa. This closes the prior native-dialog browser-check obstruction; it is not live deployment, backend security qualification or physical two-system testing. Full repository validation remains required before push.


## 2026-10-01 — DAR-133 browser lifecycle progress

Added authenticated, CSRF-protected remote-task-events BFF and bounded browser
progress pagination for task IDs from current submission status. Each page
rechecks task ownership and validates the full remote EventPage before emitting
only lifecycle kind/time/sequence. Raw event payloads remain excluded. Uses the
existing inspect permission and HTTPS/SSH client; no new live configuration.

Focused UI and BFF race suites passed (9.753s/4.287s), including actual pinned
mTLS client calls and fresh revocation. Initial fixture used an invalid config
hash and was correctly rejected; corrected fixture preserves strict validation.
Node behavior tests cover pagination, malformed sequences, no duplicate loads,
stale clearing and no retry. Real in-app browser traversed two synthetic progress
pages using production assets (reporting outputs/remote-events-browser-qa); this
is UI fixture evidence, not physical remote-system qualification. Reviewed asset
digest updated after the display-label correction. Full repository check and
normal backup remain queued requirements. Browser dispatch, continuous streaming,
unpaired discovery and physical two-host qualification remain incomplete.


## 2026-10-01 — DAR-133 opt-in browser dispatch

Added remote_dispatch_directory configuration and RecordedRemoteDispatcher wiring,
with session/CSRF-protected bounded dispatch BFF. Exact destination/certificate/
task digest persists in the private RouteStore before network dispatch. UI
requires explicit model selection, request review and send; only opaque peer/key
recovery references enter the URL before sending. No automatic retry or prompt
persistence. The initial session-storage approach was rejected by the existing
shell guard and replaced; the guard remains unchanged.

Actual mTLS BFF fixture covers persisted binding, changed-task conflict and
revocation with zero additional dispatch on rejection. UI tests cover review
invalidation, single send, failed recovery-link persistence, reload without
resending, and queued status with no task IDs. Browser testing exposed membership
unlock incorrectly enabling independently disabled task buttons; fixed by
restoring only buttons locked by the membership operation, with regression test.
Real IAB test with synthetic HTTP fixture confirms recovery URL, same request ID
after reload and status recovery, with one dispatch for the final test identity.
Evidence: reporting outputs/remote-dispatch-browser-qa. Initial storage experiment
remains separately present in its call log, not relabeled as final behavior.

Focused webui/webuiapp/config race suites and vet/source/diff checks pass; full
repository validation and push remain pending. No live daemon, SSH account or
trust changes. This exposes explicit routing only; automated accuracy selection
in the browser, continuous streaming, discovery and physical two-host
qualification remain incomplete.


## 2026-10-01 — production recovery-link boundary correction

Production-handler audit found the prior synthetic browser fixture accepted
Settings query parameters that the real shell rejects. Remote dispatch recovery
now uses only URL fragments, retaining the unchanged query-string restriction.
The fragment contains peer/request references, not prompts or authority.
Added a real HTTP client/server test with authenticated production Handler: the
fragment URL loads the shell without dispatch; the former query URL stays 400.
The Node workflow verifies a query-free fragment before sending and reload
recovery without another dispatch. This corrects a deployment gap not covered by
the earlier synthetic UI fixture; prior browser evidence alone was insufficient
to prove production recovery. Reviewed asset digest updated; full combined gate
remains required before push.

## 2026-10-01 — saved remote request status without prompt persistence

Added read-only `Client.InspectRecorded` and `nexus remote recorded-status`.
The existing immutable route binding supplies destination and caller certificate;
current peer authorization and transport checks remain in force. No original
prompt, candidate discovery, inference, repair, or directory creation is needed.
This supports recovery of explicit and automatic selections without persisting
prompts in browser state; automatic browser integration remains unfinished.

Real mTLS package and CLI fixtures cover saved destination, unchanged record,
no stdin, original caller pin, live revocation, forbidden destination override,
and uncertain intake with no repair or extra dispatch. The initial revocation
fixture failed because its parent directory was not private; corrected the
fixture, preserving the production security check. Full repository gate and
normal push remain pending; no deployment or physical second-host claim.

## 2026-10-01 — opt-in automatic remote browser API

Connected the production discovered accuracy-first remote selector to a bounded
CSRF/session-protected browser API, with separate private evidence-directory
opt-in and startup/per-dispatch storage preflight. Request fields describe only
task requirements; destination, scores, exploration and server paths cannot be
provided by the browser. Original durable choice/binding and caller identity
remain authoritative. Added prompt-free recorded status projection for recovery.

Authority/invalid-input/uncertainty tests cover no unauthorized dispatch or
automatic retry, metadata redaction, exact task classification and no exploration.
Actual mTLS BFF fixture verifies saved-destination status and fresh revocation.
Configuration tests reject absent dispatch authority, disabled UI and unsafe
paths. These checks do not prove full automatic browser behavior: Settings form,
recovery UI and outcome review integration remain unfinished. Full repository
gate/push and physical two-host qualification remain pending.

## 2026-10-01 — Settings automatic remote selection and recovery

Added an opt-in automatic model/harness request form using the production browser
API. Exact requirements are reviewed before one send; field edits invalidate the
review. Request ID is saved in a URL fragment before dispatch, with no prompt or
credential persistence. Reload offers read-only destination lookup and existing
status/result/progress/cancel controls, never candidate reselection or replay.
Uncertain responses remain explicitly uncertain. Independent new work replaces
the recovery link and is labeled separately from retries.

Behavior tests cover stale review, duplicate clicks, failed recovery-link writes,
reload, prompt-free status and opt-in rendering. Real production HTTP fixture
accepts the automatic fragment without any dispatch or status call. Real IAB
verification using production assets with synthetic responses observed exactly
one dispatch (503), reload, one recorded-status lookup, then one task status
lookup. Screenshot/call evidence: reporting outputs/remote-automatic-browser-qa.
This is UI evidence, not physical-host, inference-quality or end-to-end selector
qualification. Browser quality-review integration and remaining remote product
gaps are still open. No daemon configuration or live inference changed.

## 2026-10-01 — production browser automatic dispatch integration

Added a fixture joining the actual authenticated browser handler,
RecordedRemoteAutomatic adapter, remote discovery/ranking, private route/choice
store and remote.Server through mutual TLS. Only the execution backend and its
capability/capacity observations are synthetic. After remote intake commits,
the fixture drops the TLS connection before the dispatch response. Browser API
returns uncertainty; recorded-status recovers the original destination. Exact-key
resubmission remains one backend creation and does not rediscover after catalogue
availability changes. Changed intent and fresh revocation fail without extra
dispatch. Actual received identity, task difficulty and prompt are checked.

Focused race integration, targeted vet and source/diff checks pass. This closes
a protocol-integration gap in prior synthetic browser tests; it does not prove
model quality, ranking among multiple candidates, installed-harness execution,
physical two-system behavior or browser review completion. Combined full gate
and push remain pending. No live service or inference was used.

## 2026-10-01 — opt-in browser remote advisory review API

Added explicit reviewer-model/cost configuration and session/CSRF-protected
evaluate/status API for completed automatic requests. Resolve original intent
before reviewer policy; use the daemon's configured evaluator/shared resource
coordinator and existing durable no-replay evaluation receipts. Authenticated
actual output provenance and private/local policy remain enforced by the remote
evaluator. Browser verdicts, reviewer overrides and arbitrary receipt imports
are not accepted. Evaluation responses do not project a possibly superseded
review; separate read-only status shows current ledger classification/head.

BFF/config race suites passed, as did targeted CLI/BFF/config vet and source/diff
checks. Tests cover unauthorized and malformed requests, no callback before
valid original binding, disabled configuration, explicit cost, uncertainty
without retry, metadata projection and advisory automated-AI current-head status.
These are interface/policy tests, not completed browser-to-provider qualification.
Settings review form, combined background supervision, full repository gate and
physical-host qualification remain pending. No live evaluator invoked.

## 2026-10-01 — Settings completed-output AI review controls

Added separate review-enabled membership flag and completed-only review form.
Original request requirements survive in page memory after dispatch, never
browser storage; reload requires re-entry and the backend checks exact intent.
Confirmation is invalidated by edits. One evaluation per form is followed by
read-only current-head status; uncertainty and an existing head disable another
evaluation. AI/advisory classification is shown explicitly. Backend durable
receipts remain responsible for cross-session replay protection.

Node tests cover no implicit evaluation, stale confirmations, double clicks,
uncertain response, original requirements, missing reload inputs, existing head
and succeeded-only rendering. Webui/BFF race suites, targeted vet and source/diff
checks pass. Real IAB with synthetic endpoints observed one task dispatch and one
evaluation request with identical original requirements, lost review response,
read-only advisory status and reload with no automatic replay. Visual inspection
found cramped field labels; added scoped grid spacing and verified final layout.
Evidence: reporting outputs/remote-review-browser-qa. This is UI qualification,
not live-provider evaluation or physical-host qualification. Background review
supervision and remaining DAR-133 product gaps remain open. Full gate/push pending.

## 2026-10-01 — browser remote review through canonical learning

Extended the production mTLS/discovery/dispatch recovery fixture through runtime
completion events, the browser review API, durable evaluator attempts and scoped
ranking. An accepted AI review contributes one advisory sample; duplicate calls
and a discarded response do not re-evaluate or increase weight. A failed evaluator
remains unscored and is not retried. Changed original intent is rejected before
reviewer policy. Current-head status and ranking retain a later hash-bound
expected-head deterministic correction despite replay of the older evaluation.
No review path redispatches inference or repeats discovery; metadata omits raw
requirements, output and evaluator diagnostics.

Focused integration and complete BFF race tests plus targeted vet/source checks
are recorded with this checkpoint. Initial expanded fixture exceeded the real
bootstrap rate limit by authenticating every request; reused one authenticated
browser session, preserving the production limit. Execution and evaluator output
are synthetic; this is boundary/provenance/learning qualification, not real model
quality or physical second-host evidence. Full gate/push remains queued behind
the unchanged Settings gate. Earlier 3d1a065 full gate passed and was pushed.

## 2026-10-01 — opt-in live remote lifecycle progress

Added Follow progress to the Settings task lifecycle view. It reads new events
through the existing authenticated, scoped HTTPS/SSH API every five seconds,
with one request in flight, one displayed page and a 30-minute following limit.
It drains terminal pages before stopping. Stop, hidden/collapsed/removed views,
failed reads, malformed pages and regressing heads stop following; no automatic
failure retry or task dispatch/cancellation is introduced. Responses arriving
after Stop cannot resume polling or overwrite its status. Last-loaded task state
is labelled separately from newly observed lifecycle state; results remain an
explicit status read.

Behavior tests cover unchanged heads, terminal backlog, failed/revoked reads,
head regression, manual refresh, overlap, late-response suppression, explicit
resume and visibility/time bounds. Real IAB with synthetic endpoints traversed
started/heartbeat/completed automatically and stopped; updated wording following
visual inspection. Evidence is in reporting outputs/remote-progress-follow-browser-qa.
This is browser/read-flow qualification, not physical-host or push-stream evidence.
Reviewed asset digest and targeted tests are recorded with the checkpoint;
full repository validation/push remains queued behind the active Settings gate.

## 2026-10-01 — persistent remote review queue and worker CLI

Added explicit enqueue-review/enqueue-auto-review, run-review-jobs and read-only
review-job-status. Private queue stores original requirements, exact bound route,
reviewer provenance, storage scope and absolute deadline. Worker passes serialize
with an OS lock, poll pending remote status and reuse durable one-attempt review
orchestration. Failed/uncertain evaluation and access/policy failures are terminal
attention records. Shutdown leaves pending jobs recoverable; recovered completed
evaluator results reconcile without repeating inference. No inference dispatch,
automatic enrollment, live service or configuration changes.

Tests cover reopen, queued-to-completed progression, discarded terminal queue
receipt, one advisory sample, changed intent/deadline/policy/storage, failed
review, revocation, expiry, shutdown during evaluation, private storage and real
process lock release after SIGKILL. CLI invalid-input checks are inert. Execution
and evaluator content remain synthetic; automatic daemon/browser enrollment and
physical-host qualification are not claimed. Full repository gate/push is queued
behind the unchanged Settings check after focused qualification.

## 2026-10-01 — automatic dispatch persists review intent first

Added auto-dispatch-review-job and DispatchQueuedAutomaticReview. They persist
original automatic requirements, caller identity, storage scope, reviewer policy
and absolute deadline before discovery or submission, under the review-worker
lock. Workers resolve the original choice without discovery or dispatch. Missing
choice stays pending to expiry; conflicting intent, caller or policy cannot
silently select new work. Terminal supervision cannot authorize another combined
dispatch. Lost dispatch responses preserve the review job and use read-only
canonical completion for later evaluation.

Integration fixture cuts the TLS response after committed dispatch and proves
one evaluator invocation, no repeat selection or submission, and intent on disk
before discovery while the worker is fenced. Unbound intent remains non-executing
after peer availability returns; unsafe queue storage stops dispatch. These are
synthetic output/evaluator fixtures through production mTLS/protocol/storage,
not physical-host qualification. Browser/daemon enrollment remains next; the
explicit combined CLI and existing queue worker form the current usable workflow.
Full repository gate/push remains queued after the active Settings gate.

## 2026-10-01 — remote review supervision lifecycle

Added cancellation-aware RunReviewJobs for embedding hosts: immediate pass,
fifteen-second interval, retry only explicitly identified worker-lock contention.
Storage/integrity failures stop supervision instead of silently retrying. Queue
receipts and evaluator admission still prevent repeated evaluation; the worker
never dispatches tasks. Callers must cancel and join before closing dependencies.

Remote/CLI race suites passed (26.732s / 2.080s), targeted vet and source checks
passed. New tests cover lock contention followed by malformed storage, prompt
shutdown and reopened supervision after interrupted receipt publication using
one evaluator invocation. Synthetic fixtures, not deployed daemon or physical
host evidence. Daemon config/startup/health and browser enrollment remain open.
Full repository validation and normal branch backup are queued separately.

## 2026-10-01 — daemon-owned remote review worker

Added optional web_ui.remote_review.queue_directory, validated as a separate
absolute path under the existing explicit reviewer/evidence/client configuration.
Daemon startup prepares the private queue and owns RunReviewJobs after dispatcher
startup. Shutdown cancels and joins it before closing shared resource/storage
dependencies. Health/readiness reflect unexpected worker exit or queue errors,
without disclosing private diagnostics. Disabled configuration starts no worker.
Individual review verdicts remain in durable receipts/current heads.

Tests cover configuration rejection, disabled/manual compatibility, successful
worker lifecycle, cancellation joining, unexpected nil/error exits and real
malformed queue failure propagating into health. Focused CLI/config/health race
tests passed; targeted vet, source formatting/size and diff checks passed. This
is daemon integration source plus lifecycle tests, not live deployment or an
end-to-end installed-service qualification. Browser automatic enrollment remains
next. Full repository validation and normal branch backup remain queued.

## 2026-10-01 — browser dispatch enrolls background content review

An explicitly configured review queue plus bounded wait now enrolls browser
automatic requests before discovery/submission. Original intent, caller, reviewer
policy and absolute deadline remain immutable; exact retries reuse the saved
deadline, while conflicting or expired enrollment rejects rather than extending
work. Browser fields cannot override reviewer/storage/wait. The membership
projection exposes only an enabled flag; the form discloses private retention
and one advisory AI review before sending. No prompt browser storage added.

Production authenticated remote integration covers committed dispatch with a
lost response, saved intent/deadline, same-key recovery without rediscovery or
duplicate execution, then one queued review with canonical completion. Execution
and evaluator are synthetic. Browser behavior verifies visible disclosure and
no implicit sends. Reviewed embedded asset digest updated; the initial digest
guard failure was expected and not weakened. Physical-host/live-model and
installed-service qualification remain open. No live daemon configuration changed.

Verification: complete web UI/backend/config race suites passed (10.169s /
5.722s / cached); final membership and automatic routing tests passed (2.488s).
Targeted vet, source formatting/size and diff checks passed. Full repository
validation and normal branch backup remain queued behind the active gate.

## 2026-10-01 — recoverable browser review-job visibility

Added explicit Check background review for saved automatic requests. A reload
needs only the request ID. New protected backend read validates saved queue
scope/original choice and fresh authenticated ownership before projecting status
and applied flag. No evaluator, dispatch, repair, raw prompt/output/path or job
hash projection. Missing/unbound/revoked requests remain unavailable; completed
receipts are explicitly distinguished from current quality verdicts.

Production-mTLS integration covers missing, pending, completed and revoked job
reads alongside one evaluation/no repeated execution. Authority tests cover
session, CSRF, origin, strict input and disabled inspector. Browser behavior
covers reload without implicit reads, double click, safe projection, malformed
state and no retry/evaluation. Synthetic fixtures, not live deployment. Reviewed
asset digest updated after expected guard rejection. Full validation/backup
remains queued and broader DAR-133 qualification remains incomplete.

## 2026-10-01 — daemon process qualification found two review lifecycle defects

A disposable production-binary daemon test exposed remote_review error health
with Ready=true: the shared health Outcome calculation omitted that component.
It now gates readiness like other configured review/learning supervisors; disabled
or absent optional review retains compatibility. Source shutdown audit also
found explicit shared-resource cleanup before the deferred review join. The
review worker now joins immediately after HTTP serving ends, before explicit
cleanup as well as deferred cleanup.

The original process test failed on false readiness, preserved as the defect
trigger. Corrected test passed across three fresh daemon processes: corrupt
private queue is unhealthy, restart remains unhealthy without automatic repair,
then explicit test-only corruption removal permits healthy worker startup. All
three exit cleanly on SIGTERM, with no inference calls or private diagnostic
leakage. Focused CLI lifecycle/process race package passed (4.591s), health
readiness tests passed (1.700s). Child is the normal production binary built
by the test, not a live installation. Actual in-flight remote evaluation through
process shutdown and physical-host qualification remain separate evidence gaps.

## 2026-10-01 — unpaired discovery record boundary

Added a closed, bounded DNS-SD version-1 candidate parser for private-network
connection hints, including optional SSH port. Candidate is separate from Peer,
always unverified, with receiver-controlled timestamps/lifetime <=120 seconds.
Reject duplicate case-insensitive keys, authority/unknown fields, mismatched
instance, invalid certificate claim, malformed DNS names/ports and public,
loopback, link-local/scoped/mapped addresses. No network, pairing, credentials,
policy or quality evidence mutation. RFC 6763 informed record structure.

This is the record-validation foundation, not completed LAN discovery. Bounded
interface-specific transport, advertisement lifecycle, conflict handling and UI
remain required. Evaluated an external mDNS library's API/source but added no
dependency or network listener. Focused race tests and vet/source/diff passed;
full remote/CLI race verification follows. No live LAN broadcast performed.

Complete remote/CLI race suites passed (29.614s / 1.720s). A three-second,
two-worker fuzz run completed 27,151 executions without failure. Full repository
validation and normal backup remain queued behind the active Settings gate.

## 2026-10-01 — bounded explicit discovery browse

Added discover --interface NAME --wait DURATION and a private-interface IPv4
legacy-unicast-response DNS-SD browse. One random-ID PTR query; explicit <=10s
context/deadline, packet/record/result budgets, interface/source-port/TTL checks,
source-address agreement, conflict exclusion and expiration. No automatic
startup, credential use, endpoint probing, pairing or execution. Parser accepts
complete same-packet bundles only and refuses malformed/oversized/truncated or
wrong-transaction packets. Partial record assembly, IPv6 browse, advertising and
UI/real multicast qualification remain open.

Added pinned x/net v0.56.0 for DNS messages and interface controls, with required
x/text upgrade to v0.38.0; no zeroconf library added. Synthetic DNS tests cover
valid hints, missing addresses, duplicate SRV, authority injection, TTL zero,
address spoofing, malformed count/size and request ID mismatch. CLI tests reject
missing/unbounded interface settings without performing LAN discovery. No live
broadcast or service configuration changed.

Complete remote/CLI race suites passed (27.358s / 1.686s), targeted vet and
source/diff checks passed. Three-second two-worker DNS fuzz run completed
61,007 executions without failure. Full repository validation and normal backup
remain queued; no actual multicast interoperability result is claimed.

### 2026-10-01 — DAR-133 opt-in discovery advertiser and SSH hint

Added explicit serve-only advertisement interface/TLS-name/optional SSH-port flags.
The advertiser derives the fingerprint from the loaded certificate, checks its
validity and SAN and requires the concrete private listener IPv4 to belong to the
selected interface. Socket setup precedes dispatcher start. Bounded legacy-unicast
PTR responses carry the complete connection bundle with a 30-second TTL; private
source/interface/TTL checks and eight replies per second limit exposure. No
unsolicited announcements, trust changes or task/credential advertisement. Host
shutdown cancels and joins the responder; responder transport failure stops HTTP.
SSH hints do not assert SSH readiness or grant access; strict known-hosts checking
and inner mutual TLS remain required for actual SSH routing.

Remote and CLI race suites passed (26.765s/1.708s); subsequent targeted advertiser
race tests including loopback blocked-reader cancellation passed (1.548s/1.868s).
Vet, source formatting/size and diff checks passed. Synthetic DNS certificate/SAN/
expiry/forged-field/bounds roundtrips remain explicitly untrusted. No multicast
broadcast or live service configuration was performed. Full combined make check
and ordinary push are queued behind the already running frozen validation gate.
Physical two-system multicast/SSH qualification, Settings discovery, IPv6,
fragment assembly and general mDNS probing/collision handling remain open.

### 2026-10-01 — DAR-133 explicit Settings discovery and pairing handoff

Added administrator-fixed web_ui.remote_discovery_interface with membership
prerequisite and inert disabled defaults. Browser discovery is an explicit POST
behind session, same-origin and CSRF authority; input contains only protocol
version, one scan can run at a time, the production interface browse lasts three
seconds and the request context caps at four. It neither reads client credentials
nor changes trust. Errors and cancellation release the scan slot without partial
results. Membership advertises whether discovery was explicitly enabled.

Settings renders unverified connection hints using text nodes. No automatic scan
on load/refresh, duplicate click or stale completion; expired candidates cannot be
copied. Pairing handoff resets old consent, scopes and credential fields and fills
only identity/endpoint/claimed pin/optional SSH port, leaving HTTPS selected until
operator choice. Existing independently verified identity and expected-digest
pairing requirements remain intact. Asset manifest reviewed as
7f201e961f69491d2d3c978af68b013c369b641f44bf40972f36496a72807460.

BFF/config/browser race suites passed (5.811s/config cached/9.791s), including
unauthorized/CSRF/foreign-origin/unknown/duplicate input rejection before scan,
no trust mutation, one concurrent scan, cancellation, explicit UI request,
expiry/late result rejection and clearing prior pairing authority. Vet,
source-size/format and diff checks passed. Initial fixture failure correctly
rejected a non-private test registry directory; fixture now chmods its directory
0700. No live multicast scan or daemon configuration change occurred. Full
combined make check and normal push remain queued behind frozen active validation.
Physical two-system qualification and cross-platform multicast behavior remain
open; these tests do not establish deployment or complete DAR-133.

### 2026-10-01 — DAR-133 real Chrome discovery-to-SSH pairing qualification

Added a production embedded-Settings Chrome qualification with synthetic local
API responses and an isolated browser profile. It proves no automatic scan,
duplicate-click suppression, unverified connection/SSH hints, 390px viewport
without horizontal overflow, connection-detail handoff clearing prior identity
consent/cloud permission/SSH key, default HTTPS retained until explicit SSH
selection, no unverified pairing submission, bounded CSRF-bearing version-only
scan, explicit verified SSH pairing payload with info-only scope/current digest,
receipt reset, and failed scans removing actionable candidates without leaking
private failure text. This complements the production BFF authority/trust tests;
the API fixture does not claim to exercise actual trust mutation or network mDNS.

Required real Chrome race test passed: TestChromeRemoteDiscoveryPairingHandoff
0.83s, package 2.255s, no skip (DARWIN_REQUIRE_CHROME=1). Vet and source
format/size checks passed. No production assets or live configuration changed;
no physical remote peer, SSH connection, multicast scan or inference was used.
Full combined check/ordinary push remains queued behind active frozen validation.
Physical cross-system lifecycle and multicast qualification remain outstanding.

### 2026-10-01 — DAR-133 per-user remote host service templates

Added `nexus remote service-template` with explicit launchd/systemd selection,
main executable, working directory, shared process-admission directory, instance,
listener and runtime/trust/TLS/journal paths. Rendering only emits a reviewable
artifact: no file reads/writes, install, network, credentials copy, trust changes,
SSH server management or discovery. Paths are absolute/normalized/bounded and
control/invalid XML characters reject. Literal launchd argv uses XML escaping;
systemd command uses colon to disable environment expansion, percent escaping,
and context-appropriate value quoting. Upstream parser inspection caught and
corrected WorkingDirectory quoting before commit (single path, not token list).
Both templates use private umask, bounded restart intervals and stop timeout;
Linux includes start limiting/control-group stop. Shared admission path is explicit
rather than silently allocating an isolated reservation namespace.

Remote/CLI race suites passed 27.653s/2.088s; native plutil decoded the generated
plist and preserved quoted/metacharacter paths, argv, umask and admission env.
Vet/source/diff checks passed. Systemd serialization was checked against upstream
source/documentation; native systemd unavailable on this Mac and not claimed.
No service installed or running environment modified. Documentation now separates
current implementation coverage from remaining acceptance, replacing stale first-
implementation gaps. Full combined check/normal backup remains queued behind
active frozen validation; real platform install/restart/shutdown and physical
second-system qualification remain required.

### 2026-10-01 — DAR-133 native launchd remote-host lifecycle

Added explicit NEXUS_REMOTE_LAUNCHD=1 qualification. Builds the production nexus
entry point, renders the generated launchd template, registers only a unique
owned user agent, and serves pinned mutual TLS on loopback using test certificates,
private isolated DB/journal/admission paths and a synthetic local provider.
Authenticated capability read proves startup; launchctl SIGKILL of that owned
fixture proves restart with a different PID; bootout proves deregistration and
loss of endpoint reachability. Cleanup is registered before bootstrap to handle
ambiguous failures. No discovery or dispatch is authorized in fixture peer scopes.

Native race test passed without skip: TestNativeLaunchdRemoteHostLifecycle
32.62s, package 34.164s. Zero provider POSTs; temporary agent removed and no
production service/configuration changed. Vet, source and diff checks passed.
Docker CLI exists but its local daemon socket is absent; no Linux systemd runtime
was available or started. Native Linux validation and physical separate-system
routing remain open. Full combined check/ordinary push remains queued behind
active frozen validation.

### 2026-10-01 — DAR-133 native Linux ARM64 systemd lifecycle on Spark

User supplied the Spark and Mac mini connection route. Authenticated to the Spark
through the mini with strict existing host-key verification. User subsequently
authorized copying its dedicated Spark identity locally; copied with 0600 mode
and saved verified host-key entries separately, then proved direct authentication.
No private key or credential bytes enter the repository or qualification report.

Added explicit NEXUS_REMOTE_SYSTEMD=1 qualification using an absolute prebuilt
native nexus binary. Cross-built main/test executables for Linux ARM64, verified
SHA-256 equality after transfer, and ran on the Spark's actual systemd user
manager. Native systemd-analyze verification, unique temporary unit link/start,
pinned mTLS capability read, literal working/config/admission paths containing
spaces/dollar/percent/quotes, owned-service SIGKILL/restart under a new PID,
stop and endpoint closure passed. Test cleanup removes its own unit links.
Independent post-check found no fixture units/files/processes; existing Ollama
PID 2145 remained present, with zero provider POSTs during the test.

Native test passed in 30.86s, no skip; cross-built Linux binary has no race
instrumentation. Mac compile/vet/source/diff gates passed. Main binary SHA-256
was a7ab00128f1114e568cf9a17accfc169ad3130808cf84d83b4a5006499675382;
test binary 3f70ebf6d8d20dc8c78ec641b2fafdb39b800454004813c1df4669fa23f61724.
Reporting evidence: outputs/spark-systemd-qualification/manifest.json and
native-test.log. Full combined gate/normal push remains queued behind active
frozen validation. This closes native Linux fixture lifecycle, not physical
cross-system dispatch/cancellation/recovery or production deployment.

## 2026-10-01: Physical Mac-to-Spark remote lifecycle fixture

Added an explicit opt-in two-host test using strict SSH identity/known-host
checking, temporary pinned mTLS credentials and a hash-verified Linux ARM64
production host on DGX Spark. HTTPS and SSH both passed dispatch, committed
results/events, running cancellation and duplicate prevention after reopening
the caller route store. Host CLI revocation with expected registry digest
removed caller access. Four synthetic provider requests total; no model
inference. Native Linux resource measurement remains enabled. The initial
fixture disabled measurement and was correctly denied before provider dispatch;
only the fixture was corrected. Production admission was not weakened.

The Mac race-instrumented test passed in 15.10s (package 16.580s); the Linux
production binary is not race-instrumented. Vet, source formatting/line limits
and diff checks passed. Independent remote check found no remaining owned
two-host fixture directories; existing Ollama PID 2145 remained present.
The user-authorized SSH key stays outside Git, mode 0600.

This qualifies the physical synthetic lifecycle, not real model quality or
production deployment. Actual network-loss-after-commit injection, host-restart
task recovery and physical multicast discovery remain unqualified. Full combined
make check and normal push are queued behind the existing frozen gate.

## 2026-10-01: Physical committed-response loss recovery

Extended the Mac-to-Spark fixture with an isolated remote TLS fault proxy.
Ordinary lifecycle checks still contact the production host directly. Only the
response-loss cases use the proxy: both TLS legs authenticate with disposable
test credentials, and the proxy captures/fsyncs the production host's accepted
submission receipt before closing the caller socket without an HTTP response.
The test independently reads that receipt over strict SSH, reopens caller
records and retries the same payload/key. HTTPS and SSH recover the same
submission ID and successful result. Six synthetic provider calls cover four
successful tasks and two canceled tasks; retries add none. This is physical
connection/response loss, not a claim of arbitrary packet-loss or partition
qualification. Production code and admission are unchanged.

Temporary proxy/host processes and credentials are owned and cleaned up by the
fixture. Host-restart task recovery, physical multicast, real-model qualification
and production deployment remain open. Full repository gate and normal push
remain queued behind the already running systemd qualification checkpoint; its
process and checkout were left untouched.

Final physical test passed without skip: 16.76s (race-enabled Mac package
18.313s; Linux production binary not race-instrumented). Vet/source/diff gates
passed. This checkpoint supersedes only the prior waiting two-host gate.

## 2026-10-01: Physical host-crash task reconciliation

Added a controlled single-restart path to the isolated Spark fixture supervisor.
After verifying exact PID command/binary/journal ownership, the test sends
SIGKILL while one synthetic model request is running and a second is durably
queued. It requires a different live host PID, identical persisted storage,
ordinary lease reconciliation, original running task/submission identity and
failed committed history with no output fabrication. The queued task completes
once; replaying the original running request returns its failed submission.
Eight provider calls total across lifecycle, response-loss and restart cases
prove no extra dispatch in this fixture. No production service is killed.

Combined physical test passed without skip in 49.31s (Mac race package 50.868s);
Spark production host not race-instrumented. Vet/source/diff passed. Independent
cleanup inspection found no owned fixture directories and existing Ollama PID
2145 remained present. Updated secure-remote-routing acceptance text to separate
verified physical lifecycle/native managers from remaining external-harness
crash, partition/power-loss, multicast and deployment qualification. Full gate
and ordinary push remain queued behind the live frozen systemd checkpoint;
only the previous waiting two-host gate was superseded.

## 2026-10-01: Physical unpaired IPv4 discovery

The same disposable production Spark host now supports optional explicit
advertising on its private interface, with a caller-side three-second browse.
Fresh route/interface inspection selected Mac en1 and Spark wlP9s9. Physical
DNS-SD returned the exact test endpoint, certificate fingerprint, TLS name and
SSH port with Verified=false; saved caller trust bytes stayed unchanged and
provider calls remained zero during discovery. No implicit pairing.

Combined discovery/HTTPS/SSH/response-loss/crash-recovery fixture passed without
skip: 53.63s (Mac race package55.155s, Linux binary not race-instrumented).
Vet/source/diff passed. Independent cleanup found no owned temporary fixture
directories; existing Ollama PID2145 remained present. Updated acceptance and
opt-in test setup documentation. One physical IPv4 LAN pair is qualified; broader
network interoperability, IPv6, fragmented bundles, external-harness crash side
effects, power loss and production deployment remain open. Only the previous
waiting gate was superseded; combined full validation/ordinary push queued
behind the live frozen systemd checkpoint.

## 2026-10-01: Optional discovery in installed-service templates

Closed a configuration gap: the standalone remote host could advertise but
service-template generation could not preserve those settings. Added explicit
advertise-interface/name/optional SSH-port fields and CLI flags to launchd and
systemd templates. Defaults omit discovery. Incomplete names/interfaces, invalid
SSH ports, non-private/non-IPv4 listeners and mixed-case advertised IDs reject
without output. Rendering remains free of filesystem/network/service mutation;
actual certificate and interface ownership checks stay at host startup.

Focused template race tests passed (remote1.913s/CLI1.565s), complete remote/CLI
race suites passed (26.714s/1.578s), vet/source/diff passed. Tests cover defaults,
explicit argv and incomplete/unsafe settings. No live installed service changed;
new advertised templates still need native-manager discovery qualification.
Full combined repository validation/ordinary push remains queued behind the
live systemd gate; only preceding waiting checkpoint superseded.

## 2026-10-01: Web UI remote advertisement settings

User-requested Settings controls now expose disabled-by-default LAN advertising,
network interface, TLS name and optional SSH port. Shared inert validation backs
config and browser contract. The existing authenticated/CSRF-protected settings
endpoint persists remote_advertisement through expected-file-digest atomic
updates; active/saved projections show restart required. Read-only loaded-config
summary is explicitly not a remote-host health claim. The remote serve command
reads the saved block on startup when explicit advertisement CLI flags are absent.
No live reload, SSH installation, pairing, trust mutation or service restart is
performed by saving. UI explains same-config ownership and CLI precedence.

Real Chrome verifies default-off, invalid incomplete form rejection, valid save,
reset/reload persistence, restart badge and 390px layout. Persistence tests verify
actual config reload, stale digest rejection and no invalid-file replacement.
Initial fixtures failed because one retained enabled skills without a configured
root and the browser acted before initialization; fixtures corrected. Focused
config/browser race passed1.456s/2.145s; full config4.617s, BFF6.019s, webui11.354s,
remotecli1.923s, remoteconfig1.199s; vet/source/diff and CLI compile passed. Reviewed
asset digest be49df0ff697df2c5abbec170f779c027f4f0b89a72bde25fcfee52296a46ddb.

Cross-built fresh Linux ARM64 production host, transferred and SHA-256 verified
69f5d6d6aa5c8c061328193068ba6496266767c148e78e5ed595af8bc05903ff.
Physical Mac-to-Spark test used saved advertisement config with NO advertisement
CLI flags. Exact unverified discovery, HTTPS/SSH lifecycle, response loss and
crash reconciliation passed52.09s/Mac race package53.717s; Linux host not race
instrumented. Eight synthetic provider calls, no real model inference.

The earlier separate launchd self-browse fixture failed to observe its advertised
instance (3.26s), not claimed passed. Its pending patch/helper and failure record
are preserved in reporting outputs/launchd-discovery-pending.* for independent
Spark-browse investigation. That test change is not included here. Native service
advertisement qualification remains open. Full combined gate/normal push queued
behind live frozen systemd validation; no production daemon replaced.

Complete remote race suite also passed26.230s after config integration.


## 2026-10-01 — native systemd advertisement lifecycle

Extended the opt-in Linux user-service fixture with an explicitly selected
private IPv4 interface and generated advertisement flags. Independent browses
at startup, after forced-crash restart and after stop assert exact unverified
claims and final absence. The production host and frozen graders are unchanged.
Native DGX Spark run passed 36.80 seconds with zero provider POSTs; both transferred
binary hashes were checked. Linux test binary is not race-instrumented. Local
service-template race tests passed 1.618 seconds; remote vet and diff checks pass.
An incidental Mac browse occurred during restart and found nothing, so it is
not cross-host qualification. Mac launchd multicast remains unqualified; the
previous minimal-receiver reproduction and Apple TN3179 guidance are recorded
in the remote operations documentation. No privacy settings were changed.

Evidence: reporting outputs/systemd-advertisement-qualification/manifest.json
and native-test.log. Full combined make check and normal push will be queued
behind the already running systemd checkpoint; no full-gate pass or live
deployment is claimed. DAR-133 remains In Progress.


## 2026-10-01 — physical Pi routing and remote clock skew

Extended the physical Mac/Spark fixture with an explicit pinned Pi registration,
separate harness evidence and caller/destination scopes. Actual authenticated
harness identity is bound before dispatch and compared with immutable terminal
events. HTTPS and SSH now exercise Pi success, committed-response loss recovery,
reopened caller deduplication and running cancellation. Later crash recovery
continues to use the native-model path, not arbitrary harness side effects.

The first physical identity probes exposed a real interoperability defect:
Spark replies were about 40ms ahead of the Mac, and any positive clock difference
was rejected. Advisory observation validation now allows up to one second ahead
while retaining the 15-second stale limit. Certificate/evidence times and local
resource admission are unchanged. Boundary tests cover zero, measured offset,
maximum and excessive skew, and stale observations. Initial failure produced
zero harness dispatches; it was not counted as a quality failure.

With the fix, the actual pinned Spark Pi 0.99.2 test passed 50.17s (Mac race package
51.615s), eight synthetic provider calls total. No real-model quality, Linux race
instrumentation or deployment claimed. Installed Pi 0.85.1 remains unchanged;
Pi 0.99.2 uses the isolated previously hash-verified runtime. All fixture host
directories removed and existing Ollama untouched. Relevant focused race tests
passed 1.583s; full remote race suite passed 27.427s. Source checks and targeted vet pass. Full repository validation remains queued.
Evidence: reporting outputs/spark-pi-qualification/physical-test-clock-fix.log.
DAR-133 remains In Progress; full check/normal push queued behind frozen gate.


## 2026-10-01 — physical Pi host-crash recovery

The selected Pi registration now also drives the physical fixture's interrupted
and durably queued tasks. Verified ordinary lease reconciliation preserves
interrupted lineage/events, fails without fabricated text or replay, completes
queued work once, and returns the original failed submission on retry. The
fixture identifies descendants of its exact owned host before SIGKILL by PID
and Linux start time and requires their execution to end within five seconds.
Failure would stop only those fixture descendants and fail qualification.

Physical Mac/Spark combined HTTPS/SSH/Pi test passed 52.39s (Mac race package
53.888s), eight synthetic provider calls. Original children exited naturally;
independent cleanup found no fixture host directories and existing Ollama
remained untouched. Linux host is not race-instrumented. This qualifies Pi text
mode with native tools disabled; it does not establish arbitrary tool side
effects, other harness crash handling, or power-loss recovery. Source, vet and
diff checks pass. Full combined check/normal push will follow the existing
running frozen gate. Evidence: reporting
outputs/spark-pi-qualification/physical-pi-crash.log. DAR-133 remains In Progress.

## 2026-10-01 — Mac permission retest and portable OpenClaw qualification

After the user enabled Nexus local-network access, the disposable freshly built
Mac launchd advertisement still failed independent Spark discovery (9.12s;
Mac race package 9.690s). The fixture cleans up its own agent. Permission alone
has not yet qualified this service path: macOS tracks executable identity through
code signing, and a new test build may differ from the permitted executable.
No production discovery workaround or privacy bypass was introduced. The native
launchd test now supports explicit interface selection and an optional independent
strict-key SSH observer that verifies its CLI digest before each scan. Claims must
remain unverified; startup/restart presence and stop absence are checked.
Evidence: reporting outputs/launchd-discovery-investigation-20261001/permission-retest.json.

OpenClaw native tests now resolve the executable from PATH or an explicit absolute
NEXUS_OPENCLAW_EXECUTABLE instead of assuming Homebrew. On Spark, isolated official
OpenClaw 2026.9.7 with Node 26.10.0 passed native OpenAI-compatible and Ollama fixture
paths (9.38s combined), plus cancellation/provider join (5.43s). Official archive
SHA and npm package integrity were verified; existing installations are unchanged.
Linux tests were not race-instrumented and used synthetic providers, not real-model
quality tests. Evidence: reporting outputs/spark-openclaw-qualification/{manifest.json,native-test.log}.
Full repository validation and normal push remain queued behind the frozen gate;
DAR-133 remains In Progress, including Mac service discovery and broader harness
physical qualification.

Validation for this checkpoint: remote race suite passed 25.923s; OpenClaw race
suite passed 1.974s; targeted go vet and git diff --check passed. Optional physical
Mac discovery qualification remains failing as recorded above.

## 2026-10-01 — physical OpenClaw HTTPS/SSH and crash recovery

The explicit two-host pinned-registration fixture now permits OpenClaw as well
as Pi. Actual isolated Spark OpenClaw 2026.9.7 / Node 26.10.0 passed the full
Mac-to-Spark lifecycle: authenticated identity matches durable completion events,
results/events, running cancellation, response loss after committed acceptance,
reopened caller-store deduplication and live revocation over HTTPS and SSH.
Controlled owned-host SIGKILL required original harness descendants to exit,
preserved failed interrupted lineage without replay or fabricated output, and
completed saved queued work once. Eight synthetic provider calls exactly.

Native test passed 73.18s (Mac race package 74.704s); HTTPS 15.86s, SSH 17.17s.
Independent cleanup found no fixture host directories; existing Ollama PID2145
remained. Linux production host is not race-instrumented. No real-model quality,
native-tool side effects, arbitrary network partitions or power-loss claims.
Source formatting/size gate, remote vet and diff checks passed. Operations docs
updated. Evidence: reporting outputs/spark-openclaw-qualification/physical-test.log
and registration.json plus previously verified runtime manifest. DAR-133 remains
In Progress; combined full gate/normal push queued behind existing live gate.

## 2026-10-01 — Goose physical crash defect and Linux correction

Fresh official release API reports Goose1.52.0, matching the adapter; installed
Spark Goose1.50.0 remains unchanged. Staged isolated Linux ARM64 release1.52.0,
verified official asset SHA256, executable version and hash. Initial physical
HTTPS/SSH cases passed, but owned-host SIGKILL left a Goose child running; fixture
stopped that exact child and failed24.82s. This is preserved, not relabeled.

Linux Goose now sets kernel parent-death SIGKILL on the direct harness process.
The launching Go thread stays locked until child reaping because Linux associates
this signal with the creating thread. Darwin remains unchanged; ordinary group
cancellation remains. New Linux subprocess regression kills the owning host and
requires the direct child to stop; cleanup binds PID to Linux start time. Native
Spark regression passed0.01s. Broader arbitrary tool descendants are not covered.

Fresh hash-verified corrected Linux host passed complete physical Mac/Spark
HTTPS/SSH, cancellation, committed-response loss, duplicate suppression,
revocation and host-crash recovery51.10s (Mac race package52.659s), eight synthetic
provider calls. Original children stopped naturally; interrupted lineage failed
without replay/fabricated text and queued work completed once. Independent cleanup
found no fixture directories and OllamaPID2145 untouched. Goose Mac race1.892s,
remote race26.359s, source formatting/size, vet and diff checks passed. Linux host
and regression are not race-instrumented; no real model-quality claim.
Evidence: reporting outputs/spark-goose-qualification/ includes release metadata,
source/binary hashes, original physical-test.log, parent-death-test.log and
physical-parent-death-fixed.log. DAR-133 remains In Progress, including unresolved
Mac advertised-service identity/permission qualification. Combined full validation
and normal push queued behind the existing frozen gate.

## 2026-10-01 — physical OpenHands remote lifecycle

Fresh PyPI metadata confirms openhands-sdk1.50.1, matching the adapter. Prepared
isolated Spark Python3.12 venv with wheels only; captured dependency freeze,
interpreter SHA256 and installation report with hashes for138 distributions.
Existing environments unchanged. Physical fixture now permits pinned OpenHands
and supplies its required16384 context; normal resource admission remains.

Actual Mac-to-Spark OpenHands over HTTPS/SSH passed63.03s (Mac race64.577s),
including identity-bound durable results/events, cancellation, committed-response
loss recovery, reopened caller-store deduplication, revocation and controlled
host-crash recovery. Original owned child execution ended naturally; interrupted
lineage failed without replay/fabricated output and queued work completed once.
Exactly eight synthetic provider calls. Independent cleanup found no fixture
host directories; existing OllamaPID2145 remained untouched. Source formatting,
size, remote vet and diff checks passed. Linux host is not race-instrumented;
this does not qualify model quality, arbitrary native tools or power loss.

Evidence: reporting outputs/spark-openhands-qualification contains pinned runtime
manifest, install report, registration and physical-test.log. Full combined
validation/normal push queued behind existing live gate. DAR-133 remains In
Progress; Hermes physical qualification and Mac advertised-service permission
identity remain open alongside broader acceptance work.

## 2026-10-01 — physical Hermes and Linux parent-death correction

Prepared isolated Spark Hermes supported source663362680b6ffa4fbffeb58f6682564239a1953b
with Python3.14.7 and frozen upstream uv.lock dependencies. Captured clean source,
lockfile, interpreter and dependency-manifest hashes; existing Hermes unchanged.
Initial physical HTTPS/SSH passed, but owned-host SIGKILL left a Hermes child
running (test35.71s failed); fixture stopped only its owned survivor. Evidence
preserved. Linux Hermes now requests direct-child parent-death SIGKILL and pins
the launching Go thread through reaping, matching the demonstrated Goose fix.
Darwin and ordinary process-group cancellation remain unchanged. Native Linux
parent-death subprocess regression passed0.02s; no arbitrary descendant guarantee.

Fresh hash-verified corrected host passed physical Mac/Spark HTTPS/SSH lifecycle
60.37s (Mac race61.918s), eight synthetic provider calls: bound actual identity,
results/events, cancellation, response-loss deduplication, revoked access and host
crash reconciliation. Original harness children stopped naturally; interrupted
lineage failed without replay/fabricated output, saved queued work completed once.
Independent cleanup found no host fixtures, unchanged clean Hermes source and
existing OllamaPID2145. Hermes race1.700s, remote race26.634s, formatting/source
size, vet and diff checks passed. Linux host/regression not race-instrumented.

All five requested harnesses now have physical text-mode lifecycle evidence;
real-model accuracy, arbitrary native-tool side effects, power loss, Mac service
advertisement and complete release/deployment remain separate gaps. Evidence:
reporting outputs/spark-hermes-qualification retains original physical-test.log,
parent-death-test.log, physical-parent-death-fixed.log and pinned manifest.
DAR-133 remains In Progress. Full combined gate and normal push queued behind
existing live frozen validation; no full-gate pass or deployment claimed.

## 2026-10-01 — physical automatic dispatch and saved-choice recovery

Added opt-in NEXUS_REMOTE_TEST_AUTOMATIC with a pinned harness. On both physical
HTTPS and SSH, discover the paired destination with fresh capacity/readiness,
dispatch automatically, match durable actual identity to the saved choice, then
reopen caller storage and recover the same submission/choice with no candidates
and an invalid current ranking policy. Confirmed/advisory/effective sample counts
remain zero; sole-candidate selection is not comparative accuracy evidence.

Initial fixture incorrectly attempted automatic selection before its preceding
direct task released the sole execution slot. HTTPS correctly returned no eligible
candidate; SSH passed. Preserved initial log. Corrected fixture waits for completed
direct work; production admission unchanged. Combined physical Pi fixture passed
55.94s (Mac race57.480s), exactly ten synthetic calls including existing connection
loss, cancellation, revocation and host-crash cases. Independent cleanup found no
fixture hosts; OllamaPID2145 remained. Source formatting/size, remote vet and diff
checks passed. Linux binary is not race-instrumented; no model quality claim.

Updated stale historical documentation to point to implemented automatic/browser
workflows and distinguish arbitrary tool side effects from qualified harness text
crash recovery. Evidence: reporting outputs/physical-automatic-routing-20261001.log
and physical-automatic-routing-corrected-20261001.log. DAR-133 remains In Progress;
Mac advertised service, broad network/side-effect qualification, real comparative
accuracy, full repository gate and deployment remain open. Combined validation
and normal push queued behind the freshly verified existing live gate.

## 2026-10-01 — release notes reflect external and remote routing

The release-notes template omitted newly implemented external harnesses, secure
remote routing and Settings controls. Added their current behavior and explicit
qualification limits: synthetic physical text evidence, unknown comparative
quality, Linux direct-child crash fixes, and unresolved Mac service advertisement
identity. This prepares accurate candidate collateral; no release approval.
Earlier license evidence remains bound to31e7674 and must be regenerated for a
new final candidate. Proposed candidate artifacts will remain unapproved.
