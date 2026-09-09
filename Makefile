.PHONY: build check test fmt qualify-linux-cgroup qualify-performance qualify-mvp qualify-release qualify-license-evidence qualify-codex-repair

build:
	go build -trimpath -o bin/darwin ./cmd/darwin

check:
	go run ./cmd/check
	go vet ./...
	DARWIN_PROCESS_OWNER_DIR="$$(mktemp -d)" go test -race ./...
	go build ./...

test:
	DARWIN_PROCESS_OWNER_DIR="$$(mktemp -d)" go test -race ./...

fmt:
	gofmt -w cmd internal runtime providers tools routing policy evaluation sessions workers resources memory skills health metrics traces submissions approvals contextengine sdk examples scripts/qualify-cgroup

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
	go test -race ./skills -run '^(TestValidationAndKillSwitch|TestPathAndScopeConfinement|TestConcurrentActivationCAS|TestActivationOperationConcurrentDuplicatesCommitOnce|TestPublishGenerationIdempotentAcrossRestartAndRace)$$' -count=1 -v

# Explicit supervised signed-in cloud inference with controlled local results.
# Uses account usage; never included in check/test or ordinary CI.
qualify-codex-repair:
	DARWIN_CODEX_LIVE_FAILURE_REPAIR=1 go test -race ./internal/codexbridge -run '^TestLiveCodexRecoverableToolProtocol$$' -count=1 -v
	DARWIN_CODEX_LIVE_REPAIR=1 go test -race ./internal/app -run '^TestLiveCodexDelegationRepair$$' -count=1 -v

# Requires DARWIN_RELEASE_VERSION, DARWIN_RELEASE_COMMIT and a clean committed
# checkout. Uses only disposable test signing keys.
qualify-release: qualify-mvp
	DARWIN_RELEASE_QUALIFY=1 go test -count=1 -timeout=45m -run '^TestReleaseQualification$$' -v ./internal/releasepack

# Re-derive an external, canonical license-evidence record from its immutable
# commit and bind it to an independently supplied digest. The record must be
# outside the source checkout and the checkout must be exact and clean.
qualify-license-evidence:
	test -n "$$DARWIN_LICENSE_EVIDENCE_RECORD"
	test -n "$$DARWIN_LICENSE_EVIDENCE_SHA256"
	go run ./cmd/license-evidence verify --record "$$DARWIN_LICENSE_EVIDENCE_RECORD" --record-sha256 "$$DARWIN_LICENSE_EVIDENCE_SHA256" --source .
