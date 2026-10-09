# NexusRouter repository QA on Spark — campaign started

Snapshot: `7d4b7aac4203e6f8bdfa95a44763b58ef3eef540`.
Host: `spark-9c8a`, paired instance `dgx-spark`. NexusRouter direct remote tasks use its dedicated Ollama runner, with explicit expected model names and private zero-cost admission.

- Muse Glimmer: `muse-glimmer-30b-q4-k-m-dflash` / `muse-glimmer:30b-q4_K_M-dflash`.
- qwen3.8 27B: `qwen3-8-27b` / `qwen3.8:27b` (131072 configured context ceiling).
- Laguna S 2.1: `laguna-s-2-1` / `laguna-s-2.1:q4_K_M`.

## Status and findings

Campaign in progress; no validated bugs recorded yet. Muse batch 0 completed without an actionable candidate and explicitly identified missing caller evidence. qwen batch 1 and Laguna batch 2 have also completed; Muse batch 3 has completed; qwen batch 4 is running. 634 primary batches cover tracked UTF-8 text at the pinned commit, including source, tests, requirements and documentation. The two binary image assets are excluded. An overlong minified SPDX schema is split into two additional fragments with original line/character offsets; those fragments are still pending. Models rotate across batches: this is distributed coverage, not three independent full-repository audits. Candidate-bearing reports receive a second-model cross-review, with subsequent caller tracing needed to establish verified defects. Do not equate scheduling or a successful model response with complete coverage.

Live Markdown findings and the coverage manifest are maintained outside the repository:

`/Users/aj_lab/.NexusRouter/resources/qa-2026-10-09/findings.md`

State, pinned source snapshot, numbered batches and durable request receipts are beside that file. Reports will be copied here at reviewed checkpoints. Task envelopes/private transport configuration are excluded from Git.

## Workflow and limitations

Remote task contracts grant no filesystem/tool authority. Code is supplied as numbered text; models cannot run tests or follow unseen callers. Findings are unverified candidates unless independently traced/reproduced, and missing context must be recorded. Tests are review inputs, not a passing test-suite claim. No product fixes are authorized by this QA workflow; operational problems may be corrected to maintain progress.

The worker submits one task at a time, persists intent before dispatch, uses recorded caller-bound request identity, inspects the same request after uncertain delivery, and never automatically replays it. Initial oversized batches failed admission with context_overflow observed; retained those receipts and rebuilt the inputs to fit conservative serialized-byte limits. The replacement source-review request reached running. No model/service/configuration/privacy safeguard was changed. Helper syntax validation passed using Python py_compile; subsequent batch completions remain pending.

Ten-minute monitoring is active in the Codex app (automation `monitor-spark-nexusrouter-qa`) to inspect progress, diagnose workflow failures, validate candidates and record gaps. The worker runs on this Mac and the heartbeat needs Codex available; machine/app downtime can delay checks. The campaign stops when primary and cross-review jobs are terminal and monitoring then pauses, reporting failures/incomplete coverage honestly. No exhaustive-review or model-quality claim is made.

## Checkpoint — 2026-10-09 18:07 UTC

NexusRouter status inspection confirms qwen batch 1 is running; the worker remains alive. One primary batch is complete and 633 remain incomplete. No verified defect or product fix is established. Muse batch 0 (adaptive context/resource and approval code/tests) reported:

NO_VERIFIED_BUGS

Scope reviewed in this batch:

* AGENTS.md lines 1-21
* internal/app/adaptive_context.go lines 1-133 – chooseContextTier, contextReservationModel, contextFitsMemory
* internal/app/adaptive_context_test.go lines 1-83
* internal/app/adaptive_resources_test.go lines 1-153
* internal/app/admission_test.go lines 1-51
* internal/app/advisory_test.go lines 1-64
* internal/app/approval_decision.go lines 1-52 – Service.DecideApproval
* internal/app/approvals.go lines 1-102 – InspectApproval, ListApprovals, readApprovals, ApprovalExecutionStatus

Gaps / missing caller evidence preventing a verified bug claim:

* `s.lockResources`, `s.mu`, `s.resourceProfile`, `s.budget.Plan`, `s.modelResources` implementations not provided; cannot verify lock/unlock pairing and blocking semantics used by `contextFitsMemory`.
* `contextpolicy.Select` contract, tier ordering and interaction with `FitsMemory` not provided; cannot verify precedence intent of `tier == requiredTier && tier >= estimated || fitsMemory(tier)` and whether a tier < estimated can be selected.
* `memorySecrets`, `toolAuthority.containsSecret`, `telemetry.OpenApprovalControl`, `telemetry.OpenReadOnly`, `telemetry.Store` methods and error wrapping semantics not provided; cannot verify error mapping in `readApprovals` and `DecideApproval`.
* Callers of `chooseContextTier` and `contextFitsMemory` beyond the shown tests not provided; cannot confirm reachable paths for the cloud-locality tier return and for the `tokens <= WorkingContextTokens` bypass.
* Provider estimator `providers.EstimateWith` and `request.contextEstimator` visibility not verifiable here.

No concrete behavioral bug with sufficient evidence to claim severity, trigger, impact and a minimal fix within the provided slice was identified.

## Checkpoint — 2026-10-09 18:28 UTC

All three named models have completed one assigned batch. Primary coverage: 3/634 succeeded, one running, 630 pending. Cross-reviews have not started; their eventual total depends on candidate-bearing reports. No new primary failure or recovery was required at this check. The sequential worker is alive, and NexusRouter inspection confirms Muse batch 3 running.

qwen batch 1 reviewed `internal/app/audit.go` lines 1–422, `audit_behavior_test.go` lines 1–157, `audit_delegate.go` lines 1–231 and `audit_delegate_count.go` lines 1–92. It reported NO_VERIFIED_BUGS. Its missing evidence includes telemetry review atomicity, cancellation monitoring, replay invariants, redaction helpers and harness outcome validation. Its non-bug observations are not treated as actionable findings.

Laguna batch 2 reviewed audit delegation parsing, child-success/failure evidence and batch-bound tests. It reported NO_VERIFIED_BUGS, with gaps in caller context, runtime audit validation and event-ID validation. These are limited model reviews, not independent confirmation that the implementation is bug-free.

Spark read-only temperature sampling at 18:28 UTC: GPU 84°C; hottest reported ACPI thermal zone 92.8°C. Retained sampled peaks since tracking began: GPU 85°C and thermal zone 93.3°C (initial sample's zone identity was not captured). These are intermittent samples, not continuous maxima or CPU-package identification. Timestamped per-sensor readings remain in the private QA workspace. No model services, safeguards or source were changed. Documentation diff verification only.

## Muse batch 3 candidate triage — 2026-10-09 18:38 UTC

Pinned source: `7d4b7aac`. Three model candidates; zero confirmed defects. Source tracing only; no tests executed or product changes.

1. **Transient read error cancels audit — not accepted as a defect.** `internal/app/audit.go:320` binds the audit provider context to `monitorAuditCancellation`. `internal/app/audit_operation.go:340` explicitly requires any loss of durable observation to cancel inference fail-closed; initial and polling read errors cancel at lines 356 and 368. The model's proposal to continue on errors would weaken that guarantee. No requirement/evidence establishes continuing when durable cancellation state cannot be observed.
2. **ToolBehavior can contain arbitrary secrets — rejected trigger.** `internal/app/audit_evidence.go:35` calls event validation before projecting the field. `runtime/event.go:186` rejects nonempty invalid behavior; `runtime/tool_behavior.go:16` accepts only three fixed values. Arbitrary free-form secret content cannot reach this projection along the claimed validated path. Future type expansion is a hypothetical change, not a present bug.
3. **Post-marshal redaction invalidates JSON — unverified hypothesis.** `internal/app/audit_evidence.go:73` redacts serialized JSON and line 74 rejects invalid JSON before output. `internal/app/run.go:889` uses longest-first literal replacements; `internal/app/audit_evidence_test.go:97` exercises quoted/backslash/newline secrets and expects valid redacted JSON. No concrete admitted configuration/event reproducing an unintended rejection has been demonstrated. Crafted secrets overlapping JSON syntax/metadata remain an edge-case hypothesis; no leak is established. Removing the validity/secret defense is not justified by this report.

The original model output remains in the live Markdown report. A second-model cross-review is still planned for this candidate-bearing batch. Related files in this triage were read from the pinned snapshot, not runtime data.

## Muse batch 6 triage — 2026-10-09 18:58 UTC

Three new model claims are not supported by the full pinned caller context (`7d4b7aac`):

- **Unknown-profile local capacity denial:** intentional fail-closed behavior. `internal/app/auto.go:682` disqualifies host-local candidates when profiling fails while retaining separately profiled remote candidates. `internal/app/auto_pressure_test.go:18` explicitly expects the profile-error path to return no route. Treating unknown capacity as available would weaken admission; manual-pin behavior is not established by this automatic-path claim.
- **Shared configuration pollution:** unsupported. `internal/app/auto.go:435` creates a value-local Settings result; `internal/config/cloud_context.go:16` takes and returns Settings by value. Lines 702–703 clone both slices before appending and reassign only the local cfg fields. The cited code does not append to s.settings or its backing slices.
- **Nil recursive execution context:** rejected. `internal/app/auto.go:428` initializes executionCtx from the incoming ctx, before candidate selection/reservation. The slice sent to Muse omitted this line. No nil-context trigger is demonstrated.

No tests were run and no product code changed. Claims remain in raw model output for the later second-model cross-review; zero newly verified defects. qwen batch 7 and Laguna batch 8 reported NO_VERIFIED_BUGS with missing implementation/caller gaps. Primary coverage is 9/634; Muse batch 9 is running. Cross-reviews have not started. Worker healthy, no new failures. Spark sample: GPU 81°C; hottest thermal zone 89.3°C. Retained sampled peaks: 85°C GPU / 93.3°C thermal zone.

## Workflow recovery — 2026-10-09 19:18 UTC

Primary coverage: 11/634 succeeded, one primary failure (qwen batch 10), Muse batch 12 running. Laguna batch 11 reported no verified bugs. Campaign-owned NexusRouter status and complete event page confirm qwen batch 10 terminated with `empty_output` after a completed turn; this is not uncertain delivery. Retained original receipt; queued one new same-model recovery task for the same input, asking explicitly for a nonempty final report. The worker will run it after the active Muse task and before subsequent primary work. No automatic model fallback, configuration change or task cancellation. Failed coverage is not counted as reviewed; recovery outcome remains pending.

Worker was safely restarted with a priority for the recovery job, and Python syntax validation passed. Current GPU 81°C / hottest thermal zone 89.3°C; retained sampled peaks 85°C / 93.3°C. No new bug candidate or verified defect at this check. Source remains pinned at `7d4b7aac`; documentation-only diff validation.

## Muse batch 12 triage — 2026-10-09 19:28 UTC

Five model candidates, no confirmed defect. Read only the pinned source (`7d4b7aac`); no tests or product changes.

- Provider-key observation contamination: rejected claimed trigger. `internal/app/federated_routing.go:72` rejects duplicate synthetic provider keys before browser ranking consumes the candidates. Same-provider/different-model input cannot pass that boundary as alleged.
- Shared Exploration mutation: rejected. `internal/app/routing_policy.go:10` returns a `routing.Policy` value built from `routing.Defaults`, not a shared policy pointer. Assigning the returned value's Exploration field is local.
- Remote error aborting preview: unverified availability hypothesis. `internal/app/browser_rankings.go:58` propagates failed candidate collection into failed inspection; no real paired-host failure reproducer or collector error classification was supplied. The top-three retry requirement governs execution, not this read-only preview; the model's asserted requirement violation is unsupported.
- Database error aborting preview: unverified availability hypothesis. Evidence-store errors return failed inspection rather than a possibly misleading partial ranking. No requirement or failure-path test establishes that skipping damaged evidence is the correct behavior; do not weaken this boundary based on the claim alone.
- MaxInspectionModels overflow: unverified bounded-inventory availability hypothesis. The preview rejects an oversized projection; no admitted deployment exceeding the cap was reproduced. Truncation and visibility semantics need requirements/evidence before calling this a defect.

The original candidate report remains in live Markdown for later second-model cross-review. Primary successful coverage: 12/634. qwen's single recovery of failed batch 10 is running; original failed receipt retained. No new primary failure. Worker healthy. Current sample: GPU 79°C; hottest thermal zone 86.1°C; sampled peaks 85°C / 93.3°C.

## qwen batch 10 recovery completed — 2026-10-09 19:38 UTC

The one same-model recovery completed successfully. Original failed receipt is retained as history; unique successful primary coverage is now 15/634, including recovered batch 10. Muse batch 15 running; no new failure or service/configuration change. Cross-reviews not started.

Recovery output proposed three claims, none verified after tracing pinned source:

- Zero-revision feedback panic is rejected before indexing: `webui/contract.go:160` requires revise ExpectedRevision >=1; `internal/app/browser_mutations.go:248` invokes validation before opening the mutation. The zero-revision contract fixture is also present in `webui/contract_test.go`.
- Follow-up oldest-task assumption is false: `internal/telemetry/session_tasks.go:46` explicitly orders rowid DESC, so Limit 1 returns newest, matching `internal/app/browser_mutations.go:112`.
- UTF-8 truncation claim lacks a reachable oversized input: `approvals/approval.go:73` limits the admitted scope to 256 UTF-8 bytes, while `webui/mutations.go:9` sets the summary limit to 4096. Ordinary scope alone cannot hit the claimed cutoff. Secret-redaction expansion may alter lengths; no admitted expansion reproducer establishing invalid UTF-8 has been supplied. This is not accepted as the model's claimed confirmed defect. Do not apply its suggested truncation snippet without independent verification.

qwen batch 13 and Laguna batch 14 also reported no verified defects with caller/implementation gaps. Temperature sample: GPU 81°C; hottest thermal zone 89.6°C. Retained sampled peaks 85°C / 93.3°C. Code/source inspection only; no tests run or product fixes. Raw model report and recovered request metadata remain in the private campaign workspace; dispositions copied here for later review.

## Workflow checkpoint — 2026-10-09 19:58 UTC

19/634 unique primary batches succeeded. qwen batch 19 failed with terminal empty_output (campaign status and complete event page checked); original receipt retained and one same-model recovery queued after active Laguna batch 20. The earlier batch 10 recovery succeeded. Future task prompts explicitly require nonempty final reports, without changing provider/model settings or safeguards. Worker restarted safely; Python helper syntax check passed. No task with uncertain delivery was replayed, and active Laguna was not canceled.

Latest completed Muse/Laguna batches 18–19 in the user-facing ordinal sequence reported NO_VERIFIED_BUGS with absent implementation/caller context. No new candidate or verified defect. Cross-reviews not started. Current sample: GPU 69°C, hottest thermal zone 76.4°C; sampled peaks 85°C / 93.3°C. Documentation diff checks only; no product source or service changes.

## Muse batch 21 triage — 2026-10-09 20:08 UTC

Claimed double cleanup in configured evaluator is rejected by omitted helper context: `internal/app/resource_reservation.go:184` returns a closure guarded by sync.Once (line 188), caching its release error. Manual and deferred invocations of the reassigned closure do not execute close/release twice. Removing the explicit call would also remove its immediate cleanup-error check. No confirmed defect; no test or product change.

Successful unique primary coverage 21/634. Laguna batch 20 reported no verified bug with missing implementation context. qwen's single recovery of batch 19 is still running; no new primary failures. Worker healthy; cross-reviews not started. Sampled GPU 79°C / hottest zone 86.5°C; retained sampled peaks 85°C / 93.3°C.

## Second recovery completed — 2026-10-09 20:18 UTC

qwen's single recovery of batch 19 succeeded with a nonempty report: NO_VERIFIED_BUGS for skill generation/protocol/redaction source and tests, with explicit missing caller/helper coverage. Both empty-output gaps now have successful same-model recovery reports; original failed receipts remain preserved. Unique successful primary coverage 22/634; qwen batch 22 running, worker alive, cross-reviews not started. No new failures, candidate or verified defect. GPU 78°C / hottest thermal zone 86.2°C now; sampled peaks remain 85°C / 93.3°C. No product/source/service/configuration change or test execution; documentation diff checks only.

## qwen batch 25 triage — 2026-10-09 20:48 UTC

### P2 source-supported candidate: exhausted explicit recovery budget becomes override

Pinned `7d4b7aac`: `internal/app/context_overflow_recovery.go:47` subtracts the failed route estimate from a positive request MaxCost, rejects only negative remaining cost, then assigns zero at line 56. `internal/app/auto.go:204` sends that recovered request through runRouteChain. For an explicit model, `internal/app/run.go:242` documents zero as the legacy operator override and line 252 checks cost only if MaxCost >0. Thus a positive budget exactly equal to the failed paid route estimate can become the explicit zero-cost override on the recovery attempt.

Trigger requires an explicitly pinned paid model, AutoApprovedCompaction enabled, eligible continuation with approved summary, and a verified output-free provider context_overflow failure. Impact: the second inference is not constrained by the original exhausted positive estimate ceiling. Automatic routing instead treats zero as strict zero cost (`routing/router.go:182`), so the model's broad claim must be narrowed to explicit recovery. Code-path evidence is present; a complete isolated integration reproduction and intended total-budget policy validation remain pending. This is not yet reported as a reproduced/verified defect.

Suggested fix for later review: preserve a distinction between an operator's originally unspecified explicit budget and an exhausted positive recovery budget; deny a paid recovery with no remaining budget (or enforce an explicit strict-zero ceiling), retaining permitted genuinely zero-cost recovery. Test using an isolated existing overflow/approved-summary fixture with an explicit paid-model cost equal to MaxCost; assert no second paid call. No product changes made.

The missing-summary candidate is rejected: `internal/telemetry/summary_review.go:159` returns sql.ErrNoRows when no approved summary exists; the caller checks err and stops recovery. Muse/Laguna's other latest report found no verified bug. Primary successful coverage 27/634; Muse batch 27 running. Worker healthy; no new failures. Cross-reviews not started. Temperature sample GPU 81°C / hottest zone 89.5°C; retained peaks 85°C / 93.3°C.

## Delegation claims and cross-review selection — 2026-10-09 20:58 UTC

Muse batch 27's lifetime-budget claim is contradicted by the caller: `internal/app/run.go:535` registers delegates for the current result.TaskID and `internal/app/delegate.go:101` creates a fresh closure-local atomic counter. This is a total-call budget per parent task, not service lifetime capacity; decrementing it would weaken the task budget. The batch recoverable-success claim is also unsupported: `internal/app/delegate_batch.go:61` rejects contradictory Recoverable=true/Failed=false, while delegate rejection constructors return Recoverable=true only with Failed=true. No valid successful outcome was demonstrated to be rejected. No new verified defect.

Fixed a campaign classification gap: reports containing both explicit numbered Candidate headings and a NO_VERIFIED_BUGS marker now remain eligible for second-model cross-review. Batch 25 has that mixed shape; ignoring its marker alone would skip the cost candidate. Syntax and positive/negative classification checks passed on existing reports, and worker safely restarted without canceling the active remote task. No product or provider settings changed. Cross-reviews still run after primary batches.

30/634 unique primary batches succeeded; Muse batch 30 active, worker healthy, no new failures. qwen/Laguna batches 28–29 reported no verified defects. The source-supported recovery-cost candidate remains pending isolated reproduction. GPU 82°C / hottest thermal zone 89.4°C; retained sampled peaks 85°C / 93.3°C. Documentation diff checks only.

## Muse batch 36 feedback triage — 2026-10-09 21:38 UTC

Two model claims remain unverified/rejected against pinned caller evidence:

- Completed task with pending tool calls: rejected claimed trigger. `sessions/replay.go:209` rejects TaskCompleted while pending calls or uncertain effects remain, so FeedbackHistoryStore cannot receive the alleged completed-plus-pending snapshot through successful replay.
- Empty RouteSelected domain/profile overwrites feedback key: unverified contract-edge hypothesis. `runtime/event.go:254` does not independently require these fields, but the application's actual route emitter `internal/app/auto.go:792` fills both from the request, matching TaskStarted. No normal application caller producing the alleged empty route event has been found; direct/custom event-producer behavior needs further tracing and a reproducer before a defect claim.

37/634 successful primary batches, qwen batch 37 running. Cross-reviews pending. Worker healthy; no new failures. No new reproduced defect; recovery-cost candidate still pending isolated reproduction. GPU 78°C / hottest thermal zone 86.2°C, retained sampled peaks 85°C / 93.3°C. Source read only; documentation diff checks, no tests or product changes.
