.PHONY: build check test fmt profile-releasepack qualify-linux-cgroup qualify-performance qualify-mvp qualify-context-recovery qualify-webui qualify-delegated-count qualify-release qualify-release-test qualify-license-evidence qualify-codex-repair qualify-codex-rollover

build:
	go build -trimpath -buildvcs=false -o bin/darwin ./cmd/darwin

check:
	go run ./cmd/check
	go vet ./...
	DARWIN_PROCESS_OWNER_DIR="$$(mktemp -d)" go test -p=1 -race -timeout=45m ./...
	go build ./...

test:
	DARWIN_PROCESS_OWNER_DIR="$$(mktemp -d)" go test -p=1 -race -timeout=45m ./...

fmt:
	gofmt -w cmd internal runtime providers tools routing policy evaluation sessions workers resources memory skills health metrics traces submissions approvals contextengine sdk examples scripts/qualify-cgroup

# Run every releasepack test under the same declared package timeout as the
# supported full-suite command, reject skips, require the historical DAR-127
# failure point, and print the slowest top-level test groups plus timeout margin.
profile-releasepack:
	@go test -race -count=1 -timeout=45m -json ./internal/releasepack | go run ./cmd/test-profile --package github.com/ArronJablonowski/DarwinRouter/internal/releasepack --timeout 45m --top 12 --require TestPostPublicationReceiptRejectsMissingOrTamperedVerificationIdentity --expect-skip TestReleaseQualification

qualify-linux-cgroup:
	sh scripts/qualify-linux-cgroup.sh

# Local, opt-in performance evidence. Run without other project tests or builds.
qualify-performance:
	go test ./routing -run '^$$' -bench '^BenchmarkSelect' -benchmem -benchtime=10000x -count=3
	go test ./internal/telemetry -run '^$$' -bench '^BenchmarkDurableTaskStartAppend$$' -benchmem -benchtime=500x -count=3
	go test ./internal/telemetry -run '^$$' -bench '^BenchmarkMetricsCompletedTasks/tasks_100000$$' -benchmem -benchtime=1x -count=3
	go test ./resources -run '^$$' -bench '^BenchmarkLocalReservation$$' -benchmem -benchtime=100000x -count=3
	go test ./internal/app -run '^TestTaskLatencyNearestRank$$' -bench '^BenchmarkAutomaticTaskOverhead/pool_8/seed_1000$$' -benchtime=100x -count=3

# Deterministic DAR-45 release-candidate qualification. Live Sol/Ollama evidence
# is recorded separately and is intentionally not repeated by this target.
qualify-mvp:
	DARWIN_PROCESS_OWNER_DIR="$$(mktemp -d)" go test -race ./internal/app -run '^(TestExplicitTaskEndToEnd|TestMVPCloudOnlyOpenAICompatible|TestHybridSolCoordinatorDelegatesToIsolatedOllama|TestMVPOrchestratorAuditEvidence|TestMVPObjectiveCodeAuditEvidencePrecedence|TestMVPReadOnlyToolEvidencePrecedesAuditOpinion|TestMVPAuditRunsOnlyOnSuccessfulFallback|TestMVPOrchestratorAuditFailureModes|TestAutomaticSafeFallbackPreservesFailedHistory|TestHybridFallbackMayCrossLocalityOnlyWhenPolicyAllows|TestFeedbackUpdatesRoutingExactlyOnce|TestActiveSkillContextIsScopedRedactedAndPrivacyBound|TestMVPAutomaticSkillEvolutionSurvivesRestartAndRollsBack|TestMVPGeneratedSkillSourcePrivacySurvivesRestartAndCloudAdmission|TestConfiguredLearningPreflightNoWork|TestConfiguredLearningDurablePolicyConflictPreflight|TestLearningActivationValidationFailureKeepsPending|TestLearningIndependentServicesShareGenerationClaim|TestCodexSkillGenerationAdmissionBeforeLaunch|TestRecoverableReadFeedbackAndRestartPreserveFailure|TestCompletedTaskRecoveredAfterOwnerProcessKilled)$$' -count=1 -v
	go test -race ./internal/app -run '^(TestInjectedEvaluatorRunsAfterDurableAdmissionWithoutProviderConstruction|TestInjectedEvaluatorPublicOperationDispatchesOnceAndReplaysAfterRestart|TestInjectedEvaluatorFailureIsDurableSanitizedAndNotReinvoked)$$' -count=1 -v
	go test -race ./skills -run '^(TestValidationAndKillSwitch|TestPathAndScopeConfinement|TestConcurrentActivationCAS|TestActivationOperationConcurrentDuplicatesCommitOnce|TestPublishGenerationIdempotentAcrossRestartAndRace)$$' -count=1 -v
	go test -race ./evaluation ./sdk/v1 -run '^(TestInvokeEvaluatorPinsProvenanceAndOwnsAliases|TestInvokeEvaluatorRejectsInvalidAdmissionWithoutCalling|TestInvokeEvaluatorContainsPanicErrorCancellationAndTimeout|TestInvokeEvaluatorContainsDescriptorPanic|TestInvokeEvaluatorRejectsUnstableAndMalformedResults|TestSDKEvaluatorExactlyOnceReplayAndNoReviewerProvider|TestSDKEvaluatorTypedNilRejected|TestSDKEvaluatorFailuresAreBoundedAndDurable|TestSDKEvaluatorCancellation)$$' -count=1 -v
	go test -race ./resources ./internal/app ./sdk/v1 -run '^(TestCapacityPlanAccountsForLiveReservationsWithoutMutation|TestCapacityPlanPressureWaitsWithoutClaimingCapacity|TestCapacityPlanDoesNotTreatColdSwapAsPressure|TestCapacityPlanNormalizesEquivalentTimezones|TestCapacityPlanDeviceHeadroomMatchesAdaptiveBudget|TestCapacityPlanRejectsUnknownStaleAndImpossibleData|TestCapacityResultValidation|TestCapacityPlanMatchesReservationProperty|TestCapacityPlanConcurrentWithReservations|TestCapacityPlanPercentBoundariesMatchReserve|TestResourcePlanMapsModePrivacyAndPressure|TestResourcePlanCloudOnlyDoesNotProfile|TestResourcePlanObservesLiveReservationsWithoutMutation|TestResourcePlanRejectsInvalidAndSanitizesFailure|TestSDKResourcePlanIsReadOnlyDetachedAndBounded|TestSDKResourcePlanMapsModePrivacyAndPressure|TestSDKResourcePlanRejectsInvalidProfilerAndRequests|TestSDKResourcePlanDiscreteGPUAndUnifiedMemory|TestSDKResourcePlanCancellationDuringProfiler|TestSDKResourcePlanResultValidation)$$' -count=1 -v
	go test -race ./runtime ./internal/telemetry ./internal/app ./sdk/v1 -run '^(TestEventCloneOwnsNestedStateAndCanonicalizesTime|TestEventDeliveryCommitsBeforeDetachedOrderedFanout|TestEventDeliveryFailureIsSanitizedAndNeverReclassifiesCommit|TestEventDeliveryObservesTerminalContextButNotFailedCommit|TestInstallEventSinkRejectsTypedNil|TestConfiguredEventSinkIncludesDelegationWhileRunStreamRemainsRootOnly|TestConfiguredEventSinkChildFailureCancelsAndReleasesWorker|TestConfiguredEventSinkWorkerBoundaryFailureFinalizesAndReleases|TestInterruptedRecoveryCommitReturnsExactOwnedEventsOnce|TestInterruptedRecoveryCommitReturnsNoEventsWithoutCommit|TestInterruptedDelegationScreeningFailsClosedBeforeCommit|TestInterruptedRecoveryScreeningCanonicalizesSecretSetBeforeBounds|TestRecoveryEventSinkReceivesOnlyNewInterruptedEvents|TestRecoveryEventSinkFailureDoesNotBlockLaterCandidateOrRedeliver|TestRecoveryEventSinkPreservesBatchOrderAndStopsFailedBatch|TestRecoveryEventSinkSequencerFencesCommitBehindPriorCallback|TestRecoverySecretResolverPanicLeavesCandidateFencedAndContinues|TestDispatcherRecoveryScreensRotatedSecretBeforeWritesAndContinues|TestRecoveryEventSinkUsesLiveDispatcherContextAfterCommit|TestConfigurationReconciliationRecoversOldInterruptedHistoryWithoutExecution|TestInterruptedDelegationRestoresResultAndExplicitlyContinues|TestSDKEventSinkCoversRunSurfacesAfterDurableCommit|TestSDKEventSinkAndRunStreamReceiveDetachedFanout|TestSDKEventSinkFailureStillDeliversSameCommittedEventToRunStream|TestSDKEventSinkExternalCancellationDeliversDurableTerminal|TestSDKEventSinkFailureSuppressesAssociatedAndPendingText|TestSDKEventSinkTypedNilRejectedBeforeEffects|TestSDKEventSinkFailureIsSanitizedAndClientReusable|TestSDKEventSinkDoesNotReceiveFailedPersistence|TestSDKEventSinkConcurrentRunsPreservePerTaskSequence|TestSDKEventSinkRestartDoesNotAutomaticallyReplay|TestExternalModuleConsumer)$$' -count=1 -v
	go test -race ./sessions ./internal/telemetry ./internal/app ./sdk/v1 -run '^Test(EventLogCursor|CommittedEventLog|CommittedEventPage|ReadCommittedEvents|SDKCommittedEventLedger|CommittedEventLedger)' -count=1 -v
	go test -race ./providers ./internal/app ./sdk/v1 -run '^(TestFactoryPurposeDefaultsToExecutionAndAdmitsKnownValues|TestFactoryPurposeRejectsUnknownValueBeforeConstruction|TestDeferredTaskProviderIsInertUntilStreamAndOpensOnceAcrossTurns|TestDeferredTaskProviderContainsOpenFailuresAndCleansUpOnce|TestDeferredTaskProviderCancellationIsStickyAndCleansUpOnce|TestExplicitExecutionFactoryReadsCommittedTaskStart|TestTaskStartedSinkFailurePreventsExecutionProviderConstruction|TestTaskStartedPersistenceFailurePreventsExecutionProviderConstruction|TestExecutionProviderConstructionFailuresAreDurableAndServiceReusable|TestExecutionProviderConstructionCancellationIsDurable|TestAutomaticDiscoveryBuildIsDistinctFromFailedExecutionBuild|TestAutomaticAndFallbackExecutionConstructionFollowOwnDurableStarts|TestDelegatedChildExecutionConstructionFollowsChildStart|TestChildStartSinkFailurePreventsChildExecutionConstruction|TestFallbackStartSinkFailurePreventsFallbackExecutionConstruction|TestExecutionConstructionWaitsForDeliveredRouteAndTurnBoundaries|TestTurnStartPersistenceFailurePreventsExecutionConstruction|TestOwnedCodexLaunchWaitsForTurnStartDeliveryAndCommit|TestProviderEngineUsedForAuxiliaryModelsAndHealth|TestCodexCompactionRevocationRespectsDurableStartBoundary|TestSDKEventSinkFailureIsSanitizedAndClientReusable)$$' -count=1 -v
	go test -race ./sessions ./internal/telemetry ./internal/app ./sdk/v1 ./internal/api ./internal/cli -run 'SessionTask' -count=1 -v
	go test -race ./sessions ./internal/telemetry ./internal/app ./sdk/v1 ./internal/api ./internal/cli -run 'Branch' -count=1 -v
	go test -race ./sessions ./submissions ./internal/telemetry ./internal/app ./sdk/v1 ./internal/api ./internal/cli -run 'Resume' -count=1 -v
	go test -race ./runtime ./internal/app -run '^(TestProviderPanic|TestProviderCallback|TestToolExecutorPanic|TestToolPanic|TestDispatcherContainsRootPanic|TestSubmittedDelegatedChildPanic)' -count=1 -v
	go test -race ./sessions ./internal/telemetry -run '^(TestPlanInterruptedModelBeforeAndAfterModelBoundaries|TestInterruptedModelRecoveryAtomicAndFenced)$$' -count=1 -v
	go test -race ./internal/cli -run '^TestDaemonBranchAndRecoveredResumeAcrossRestart$$' -count=1 -v
	go test -race ./internal/app ./internal/telemetry -run '^(TestClassifyRequestIntentUsesOnlyStructuredEvidence|TestClassifiedDomainPrecedesSkillDiscoveryAndPersistsEveryUse|TestSubmissionDigestUsesCanonicalIntentBeforeStorage|TestSubmissionContractGenerationFencesLegacyQueuedIntent|TestDispatcherRetiresOldConfigurationAndContinuesCurrentWork|TestConfigurationReconciliationAdvancesPastCorruptRequest|TestConfigurationReconciliationReconsidersLaterExpiration|TestConfigurationMismatchCandidatesSkipHistoryAndAdvancePastCorruption|TestConfigurationMismatchCandidatesReconsiderNewlyExpiredWork)$$' -count=1 -v
	go test -race ./runtime ./sessions ./metrics ./internal/telemetry ./internal/app -run '^(TestApprovedCompaction.*|TestCompactionPlan.*|TestAppendContextCompaction.*|TestPlannedContextCompaction.*|TestCustomContextEnginePlan.*|TestApprovedMidTaskCompactionPreservesLiveToolSuffix|TestMidTaskCompactionActivationBudgetTerminalizesWithRealStore|TestPendingCompactionSkipsUnsupportedAssemblyProviderAndRedaction|TestAutoCompactionPreparationFailurePrecedesManagedResidencyMutation|TestAutoCapacityRerankDiscardsRejectedCandidateCompaction|TestReplayContextCompaction.*|TestInterrupted.*Compaction.*|TestContextCompaction.*|TestPostCompaction.*|TestTraceSnapshotIncludesMidTaskContextCompaction|TestMetricsCountsCanonicalRuntimeEvents)$$' -count=1 -v

# Deterministic DAR-126 adversarial context-recovery qualification. This gate
# uses local fixtures and Unix SIGKILL process tests; it is not live-provider,
# power-loss, filesystem-durability, or physical-host evidence. The captured
# JSON event stream is replayed to the transcript and rejects every test skip.
qualify-context-recovery:
	@set -eu; case "$$(go env GOOS)" in darwin|linux) ;; *) echo 'DAR-126 qualification requires darwin or linux; skips are not accepted' >&2; exit 1;; esac; owner_dir="$$(mktemp -d)"; json_log="$$(mktemp)"; trap 'rm -rf "$$owner_dir"; rm -f "$$json_log"' EXIT HUP INT TERM; if ! DARWIN_PROCESS_OWNER_DIR="$$owner_dir" go test -race -timeout=45m ./runtime ./sessions ./workers ./internal/telemetry ./internal/app -run '^(TestDAR126.*|TestCompactionPlanAtomicFailureIsAmbiguousAndStopsDispatch|TestApprovedCompaction(FailureIsFailClosed|JournalLimitUsesReservedTerminal|CancellationAfterCommitPreventsDispatch)|TestAppendContextCompaction(CommitsEventAndActivationAtomically|RollsBackBothRecords|ConcurrentExactRetry)|TestContextCompactionActivation(MissingFactFailsReopen|SurvivesSIGKILLBoundaries)|TestContextCompactionPlan(DeadOwnerRecovery|LiveOwnerCannotRecover|DeadOwnerPreservesDurableDraft|PreparationSurvivesSIGKILLBoundaries)|TestContextCompactionDelegation.*|TestDeriveDelegationCompactionBindings.*|TestCompletedDelegationCalls.*|TestDelegationCompactionRejects.*|TestDelegationCompactionBinding.*|TestContextCompactionActivationDelegations.*|TestProjectCompletedDelegation.*|TestCompletedDelegationEvidence.*|TestWorker.*DelegationCompaction.*|TestPlannedParentCompaction.*|TestCodexContextRollover.*|TestContextRolloverProvider.*|TestContextRolloverRetirementNeverRetriesAmbiguousClose|TestRequiredContextRollover.*|TestCustomContextEnginePlanRejectsDescriptorDrift|TestCodexPendingCompactionRejectsDescriptorDrift|TestCodexCompactionRevocationRespectsDurableStartBoundary|TestSummaryRevocationBetweenAdmissionAndTaskStart|TestInterruptedRecoveryAfterContextCompactionOnlyTerminalizes|TestInterruptedReadOnlyRecoveryAcceptsCompactionBoundary|TestInterruptedDelegationRecoveredAfterAbruptProcessDeath|TestSummaryAttemptsRecoverAcrossFourSIGKILLBoundaries|TestCompletedTaskRecoveredAfterOwnerProcessKilled)$$' -count=1 -json > "$$json_log"; then cat "$$json_log"; exit 1; fi; cat "$$json_log"; if grep -Eq '"Action":"skip"' "$$json_log"; then echo 'DAR-126 qualification rejected: go test reported a skipped test' >&2; exit 1; fi; for required_test in TestContextCompactionPlanPreparationSurvivesSIGKILLBoundaries TestContextCompactionActivationSurvivesSIGKILLBoundaries TestDAR126DelegationCompactionAdversarialQualification TestPlannedParentCompactionAfterDelegationCompletion TestPlannedParentCompactionFailsClosedOnDelegationPolicyDrift TestSummaryAttemptsRecoverAcrossFourSIGKILLBoundaries TestInterruptedDelegationRecoveredAfterAbruptProcessDeath TestDAR126CodexRolloverRecoversAcrossSIGKILLBoundaries TestCompletedTaskRecoveredAfterOwnerProcessKilled; do if ! grep -Eq "\"Action\":\"pass\".*\"Test\":\"$$required_test\"(,|})" "$$json_log"; then echo "DAR-126 qualification rejected: missing root pass event for $$required_test" >&2; exit 1; fi; done; echo 'DAR-126 qualification JSON checks passed: no skips and all required root pass events present'

# Deterministic DAR-86 browser qualification. All provider traffic uses
# loopback fixtures. Real-Chrome checks run when Chrome and a Node runtime with
# built-in WebSocket support are installed; a skip is not browser evidence.
qualify-webui:
	DARWIN_REQUIRE_CHROME=1 DARWIN_PROCESS_OWNER_DIR="$$(mktemp -d)" go test -race -count=1 ./webui ./internal/browserauth ./internal/webuiapp
	DARWIN_PROCESS_OWNER_DIR="$$(mktemp -d)" go test -race -count=1 ./internal/app -run '^(TestExplicitTaskEndToEnd|TestMVPCloudOnlyOpenAICompatible|TestHybridSolCoordinatorDelegatesToIsolatedOllama|TestLocalOnlyBlocksCloudAndUnapprovedTransportsAcrossRuntimeSurfaces|TestBrowser.*|TestWorkboardScheduler.*)$$'
	DARWIN_PROCESS_OWNER_DIR="$$(mktemp -d)" go test -race -count=1 ./internal/cli -run '^(TestDaemonLifecycleAcrossCLIProcesses|TestDaemonBranchAndRecoveredResumeAcrossRestart|TestEnabledWorkboardSchedulerDaemonExecutesAndJoinsOnSIGTERM|TestEnabledWorkboardSchedulerDaemonAfterSIGKILLDoesNotRedispatch)$$'

# Deterministic DAR-129 qualification. The settings and provider calls use
# local fixtures; this target never expands the operator's live filesystem
# scope or consumes cloud-model usage.
qualify-delegated-count:
	DARWIN_PROCESS_OWNER_DIR="$$(mktemp -d)" go test -race -count=1 ./webui ./internal/webuiapp ./internal/config -run '^(TestSettingsShellUsesBoundedToggleControls|TestSettingsMutationRequiresAuthorityAndDetectsConflict|TestSettingsMutationRejectsInvalidDependencies|TestProjectToolAccessAtomicUpdate|TestProjectToolAccessRejectsInvalidAndSymlink)$$'
	DARWIN_PROCESS_OWNER_DIR="$$(mktemp -d)" go test -race -count=1 ./internal/app -run '^(TestReadToolConfinement|TestReadToolReturnsBoundedAggregateDirectoryCounts|TestCloudDelegatedCountToolRejectsFileContentReads|TestDelegatedReadModeRequiresExactOperatorAuthority|TestCloudDelegatedReadModeRejectsDirectOrChildUse|TestDelegateReadToolsInheritedScopeAndNoRecursion|TestCloudCoordinatorDelegatesBoundedCountWithoutDirectFilesystemAuthority|TestHybridSolCoordinatorDelegatesToIsolatedOllama)$$'

# Explicit supervised signed-in cloud inference with controlled local results.
# Uses account usage; never included in check/test or ordinary CI.
qualify-codex-repair:
	DARWIN_CODEX_LIVE_FAILURE_REPAIR=1 go test -race ./internal/codexbridge -run '^TestLiveCodexRecoverableToolProtocol$$' -count=1 -v
	DARWIN_CODEX_LIVE_REPAIR=1 go test -race ./internal/app -run '^TestLiveCodexDelegationRepair$$' -count=1 -v

# Explicit supervised signed-in Sol qualification of durable context rollover.
# Uses two native inference calls; never included in check/test or ordinary CI.
qualify-codex-rollover:
	DARWIN_CODEX_LIVE_ROLLOVER=1 go test -race ./internal/app -run '^TestLiveCodexApprovedPlanRollover$$' -count=1 -v

# Requires DARWIN_RELEASE_VERSION, DARWIN_RELEASE_COMMIT and a clean committed
# checkout. Uses only disposable test signing keys.
qualify-release:
	$(MAKE) qualify-mvp
	$(MAKE) qualify-context-recovery
	$(MAKE) qualify-release-test

qualify-release-test:
	DARWIN_RELEASE_QUALIFY=1 go test -count=1 -timeout=45m -run '^TestReleaseQualification$$' -v ./internal/releasepack

# Re-derive an external, canonical license-evidence record from its immutable
# commit and bind it to an independently supplied digest. The record must be
# outside the source checkout and the checkout must be exact and clean.
qualify-license-evidence:
	test -n "$$DARWIN_LICENSE_EVIDENCE_RECORD"
	test -n "$$DARWIN_LICENSE_EVIDENCE_SHA256"
	sh scripts/license-evidence-bootstrap.sh verify --record "$$DARWIN_LICENSE_EVIDENCE_RECORD" --record-sha256 "$$DARWIN_LICENSE_EVIDENCE_SHA256" --source .
