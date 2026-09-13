package workboard

import (
	"strings"
	"testing"
	"time"
)

func auxiliaryReviewOutcomeFixtures(t *testing.T) (CandidateEvaluationRequest, AuxiliaryReviewAdmissionRecord, AuxiliaryReviewSettlementRecord, AuxiliaryReviewOutcomeRecord) {
	t.Helper()
	criteria := []AcceptanceCriterion{{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic",
		ValidatorID: "go-test", Description: "Tests pass.", Required: true}}
	sourceOutput := "exact runtime output"
	frozen := CandidateEvaluationRequest{Version: 1, BoardID: "board", CardID: "card", AttemptID: "work-attempt", ClaimID: "claim",
		CandidateID: "candidate", BindingKind: "runtime_budgeted", SourceTaskID: "task", SourceSessionID: "session",
		SourceTurnID: "turn", SourceAttemptID: "runtime-attempt", SourceCompletionEventID: "turn-completed",
		SourceCompletionSequence: 3, SourceCompletionDigest: strings.Repeat("1", 64), SourceOutput: sourceOutput, SourceOutputDigest: SourceOutputDigest(sourceOutput),
		SourceTerminalEventID: "task-completed", SourceTerminalSequence: 4, SourceTerminalDigest: strings.Repeat("3", 64),
		SourceDomain: "code", SourceProfile: "default", SourcePrivacy: "local_only", AdmissionID: "source-admission",
		AdmissionDigest: strings.Repeat("4", 64), SourceModelID: "source-model", SourceProviderID: "source-provider",
		ConfigID: strings.Repeat("5", 64), SourceTimeLimitMS: 1_000, SourceTokenLimit: 100, SourceCostMicros: 10,
		WorkerID: "worker", ExpectedCardRevision: 2, ExpectedClaimRevision: 1, CriteriaRevision: 1,
		CandidateDigest: CandidateContentDigest("candidate output", []string{}), CriteriaDigest: AcceptanceCriteriaDigest(criteria),
		PolicyDigest: strings.Repeat("6", 64), Summary: "candidate output", ArtifactRefs: []string{}, Criteria: criteria}
	if err := frozen.Validate(); err != nil {
		t.Fatal(err)
	}

	reservation := AuxiliaryReviewReservation{Version: 1, BoardID: frozen.BoardID, CardID: frozen.CardID,
		AttemptID: frozen.AttemptID, ClaimID: frozen.ClaimID, CandidateID: frozen.CandidateID,
		CandidateDigest: frozen.CandidateDigest, CriteriaDigest: frozen.CriteriaDigest, PolicyDigest: frozen.PolicyDigest,
		ReviewerID: "reviewer", ModelID: "review-model", ProviderID: "review-provider", ConfigID: strings.Repeat("7", 64),
		TimeLimitMS: 1_000, TokenLimit: 2_000, CostMicros: 3_000}
	reservationDigest, err := reservation.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	admittedAt := time.Unix(100, 0).UTC()
	admission := AuxiliaryReviewAdmissionRecord{Version: 1, AdmissionID: "review-admission", OperationID: "review-operation",
		BoardID: reservation.BoardID, CardID: reservation.CardID, AttemptID: reservation.AttemptID, ClaimID: reservation.ClaimID,
		CandidateID: reservation.CandidateID, CandidateDigest: reservation.CandidateDigest, CriteriaDigest: reservation.CriteriaDigest,
		PolicyDigest: reservation.PolicyDigest, ReviewerID: reservation.ReviewerID, ModelID: reservation.ModelID,
		ProviderID: reservation.ProviderID, ConfigID: reservation.ConfigID, TimeLimitMS: reservation.TimeLimitMS,
		TokenLimit: reservation.TokenLimit, CostMicros: reservation.CostMicros, AdmittedAt: admittedAt,
		ReservationDigest: reservationDigest}
	admission.AdmissionDigest, err = admission.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}

	settlement := AuxiliaryReviewSettlementRecord{Version: 1, SettlementID: "review-settlement", AdmissionID: admission.AdmissionID,
		AdmissionDigest: admission.AdmissionDigest, ReservationDigest: admission.ReservationDigest, OperationID: admission.OperationID,
		BoardID: admission.BoardID, CardID: admission.CardID, AttemptID: admission.AttemptID, ClaimID: admission.ClaimID,
		CandidateID: admission.CandidateID, CandidateDigest: admission.CandidateDigest, CriteriaDigest: admission.CriteriaDigest,
		PolicyDigest: admission.PolicyDigest, ReviewerID: admission.ReviewerID, ModelID: admission.ModelID, ProviderID: admission.ProviderID,
		ConfigID: admission.ConfigID, TimeLimitMS: admission.TimeLimitMS, TokenLimit: admission.TokenLimit, CostMicros: admission.CostMicros,
		AdmittedAt: admission.AdmittedAt, Disposition: AuxiliaryReviewCompleted, ChargedTimeMS: 500,
		ChargedTokens: admission.TokenLimit, ChargedCostMicros: admission.CostMicros, TimeChargeMode: AuxiliaryReviewMeasured,
		TokenChargeMode: AuxiliaryReviewConservative, CostChargeMode: AuxiliaryReviewConservative, SettledAt: admittedAt.Add(time.Second)}
	settlement.SettlementDigest, err = settlement.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}

	outcome := AuxiliaryReviewOutcomeRecord{Version: 1, OutcomeID: "review-outcome", AdmissionID: admission.AdmissionID,
		AdmissionDigest: admission.AdmissionDigest, OperationID: admission.OperationID, BoardID: frozen.BoardID, CardID: frozen.CardID,
		AttemptID: frozen.AttemptID, ClaimID: frozen.ClaimID, CandidateID: frozen.CandidateID, CandidateDigest: frozen.CandidateDigest,
		CriteriaDigest: frozen.CriteriaDigest, PolicyDigest: frozen.PolicyDigest, SourceTaskID: frozen.SourceTaskID,
		SourceSessionID: frozen.SourceSessionID, SourceTurnID: frozen.SourceTurnID, SourceAttemptID: frozen.SourceAttemptID,
		SourceCompletionEventID: frozen.SourceCompletionEventID, SourceCompletionSequence: frozen.SourceCompletionSequence,
		SourceCompletionDigest: frozen.SourceCompletionDigest, SourceOutputDigest: frozen.SourceOutputDigest,
		SourceTerminalEventID: frozen.SourceTerminalEventID, SourceTerminalSequence: frozen.SourceTerminalSequence,
		SourceTerminalDigest: frozen.SourceTerminalDigest, SourceDomain: frozen.SourceDomain, SourceProfile: frozen.SourceProfile,
		SourcePrivacy: frozen.SourcePrivacy, SourceAdmissionID: frozen.AdmissionID,
		SourceAdmissionDigest: frozen.AdmissionDigest, SourceModelID: frozen.SourceModelID,
		SourceProviderID: frozen.SourceProviderID, SourceConfigID: frozen.ConfigID, ReviewerID: admission.ReviewerID,
		ReviewerModelID: admission.ModelID, ReviewerProviderID: admission.ProviderID, ReviewerConfigID: admission.ConfigID,
		AuditID: "audit", AuditDigest: strings.Repeat("8", 64), EvidenceDigest: strings.Repeat("9", 64), EvidenceCount: 1,
		SettlementDigest: settlement.SettlementDigest, RecordedAt: settlement.SettledAt.Add(time.Second)}
	outcome.OutcomeDigest, err = outcome.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	return frozen, admission, settlement, outcome
}

func TestAuxiliaryReviewOutcomeBindsCanonicalStructuredResult(t *testing.T) {
	frozen, admission, settlement, outcome := auxiliaryReviewOutcomeFixtures(t)
	if err := outcome.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := outcome.ValidateBindings(frozen, admission, settlement); err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*AuxiliaryReviewOutcomeRecord){
		"source attempt":    func(r *AuxiliaryReviewOutcomeRecord) { r.SourceAttemptID = "other-attempt" },
		"source completion": func(r *AuxiliaryReviewOutcomeRecord) { r.SourceCompletionDigest = strings.Repeat("a", 64) },
		"source output":     func(r *AuxiliaryReviewOutcomeRecord) { r.SourceOutputDigest = strings.Repeat("b", 64) },
		"source terminal":   func(r *AuxiliaryReviewOutcomeRecord) { r.SourceTerminalDigest = strings.Repeat("c", 64) },
		"source domain":     func(r *AuxiliaryReviewOutcomeRecord) { r.SourceDomain = "math" },
		"source profile":    func(r *AuxiliaryReviewOutcomeRecord) { r.SourceProfile = "other" },
		"source privacy":    func(r *AuxiliaryReviewOutcomeRecord) { r.SourcePrivacy = "cloud" },
		"source admission":  func(r *AuxiliaryReviewOutcomeRecord) { r.SourceAdmissionDigest = strings.Repeat("d", 64) },
		"review model":      func(r *AuxiliaryReviewOutcomeRecord) { r.ReviewerModelID = "other-model" },
		"review config":     func(r *AuxiliaryReviewOutcomeRecord) { r.ReviewerConfigID = strings.Repeat("e", 64) },
		"settlement":        func(r *AuxiliaryReviewOutcomeRecord) { r.SettlementDigest = strings.Repeat("f", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := outcome
			mutate(&changed)
			changed.OutcomeDigest, _ = changed.CanonicalDigest()
			if err := changed.Validate(); err != nil {
				t.Fatalf("mutated record should remain structurally valid: %v", err)
			}
			if err := changed.ValidateBindings(frozen, admission, settlement); err == nil {
				t.Fatal("transplanted binding accepted")
			}
		})
	}
}

func TestAuxiliaryReviewOutcomeRejectsUnrelatedAdmission(t *testing.T) {
	frozen, admission, settlement, outcome := auxiliaryReviewOutcomeFixtures(t)
	admission.CardID = "other-card"
	admission.ReservationDigest, _ = admission.reservation().CanonicalDigest()
	admission.AdmissionDigest, _ = admission.CanonicalDigest()
	settlement.CardID = admission.CardID
	settlement.ReservationDigest = admission.ReservationDigest
	settlement.AdmissionDigest = admission.AdmissionDigest
	settlement.SettlementDigest, _ = settlement.CanonicalDigest()
	outcome.AdmissionDigest = admission.AdmissionDigest
	outcome.SettlementDigest = settlement.SettlementDigest
	outcome.OutcomeDigest, _ = outcome.CanonicalDigest()
	if err := outcome.ValidateBindings(frozen, admission, settlement); err == nil {
		t.Fatal("unrelated but internally consistent admission accepted")
	}
}

func TestAuxiliaryReviewOutcomeDigestCoversAuditEvidenceAndAccounting(t *testing.T) {
	_, _, _, outcome := auxiliaryReviewOutcomeFixtures(t)
	for name, mutate := range map[string]func(*AuxiliaryReviewOutcomeRecord){
		"audit identity": func(r *AuxiliaryReviewOutcomeRecord) { r.AuditID = "other-audit" },
		"audit digest":   func(r *AuxiliaryReviewOutcomeRecord) { r.AuditDigest = strings.Repeat("a", 64) },
		"evidence digest": func(r *AuxiliaryReviewOutcomeRecord) {
			r.EvidenceDigest = strings.Repeat("b", 64)
		},
		"evidence count": func(r *AuxiliaryReviewOutcomeRecord) { r.EvidenceCount++ },
		"settlement":     func(r *AuxiliaryReviewOutcomeRecord) { r.SettlementDigest = strings.Repeat("c", 64) },
		"recorded time":  func(r *AuxiliaryReviewOutcomeRecord) { r.RecordedAt = r.RecordedAt.Add(time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := outcome
			mutate(&changed)
			if err := changed.Validate(); err == nil {
				t.Fatal("mutation with stale outcome digest accepted")
			}
		})
	}
}

func TestAuxiliaryReviewOutcomeRequiresCompletedSettlementAndCausalTime(t *testing.T) {
	frozen, admission, settlement, outcome := auxiliaryReviewOutcomeFixtures(t)
	failed := settlement
	failed.Disposition = AuxiliaryReviewFailed
	failed.SettlementDigest, _ = failed.CanonicalDigest()
	if err := outcome.ValidateBindings(frozen, admission, failed); err == nil {
		t.Fatal("failed review settlement produced an outcome")
	}

	early := outcome
	early.RecordedAt = settlement.SettledAt.Add(-time.Nanosecond)
	early.OutcomeDigest, _ = early.CanonicalDigest()
	if err := early.ValidateBindings(frozen, admission, settlement); err == nil {
		t.Fatal("outcome recorded before settlement accepted")
	}
}
