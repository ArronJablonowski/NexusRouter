package workboard

import "time"

const AuxiliaryReviewOutcomeVersion = 1

// AuxiliaryReviewOutcomeRecord is the immutable bridge from a successfully
// admitted auxiliary review to the exact runtime output it audited and the
// durable records produced from that audit. It deliberately stores only
// identities and digests: model-authored findings remain in the separately
// validated audit record, while evidence remains in the candidate mutation.
//
// An outcome grants no independent acceptance authority. In particular, its
// evidence digest only proves which advisory evidence was written atomically;
// normal acceptance policy still decides whether that evidence is sufficient.
type AuxiliaryReviewOutcomeRecord struct {
	Version         int    `json:"version"`
	OutcomeID       string `json:"outcome_id"`
	AdmissionID     string `json:"admission_id"`
	AdmissionDigest string `json:"admission_digest"`
	OperationID     string `json:"operation_id"`

	BoardID     string `json:"board_id"`
	CardID      string `json:"card_id"`
	AttemptID   string `json:"attempt_id"`
	ClaimID     string `json:"claim_id"`
	CandidateID string `json:"candidate_id"`

	CandidateDigest string `json:"candidate_digest"`
	CriteriaDigest  string `json:"criteria_digest"`
	PolicyDigest    string `json:"policy_digest"`

	SourceTaskID             string `json:"source_task_id"`
	SourceSessionID          string `json:"source_session_id"`
	SourceTurnID             string `json:"source_turn_id"`
	SourceAttemptID          string `json:"source_attempt_id"`
	SourceCompletionEventID  string `json:"source_completion_event_id"`
	SourceCompletionSequence int64  `json:"source_completion_sequence"`
	SourceCompletionDigest   string `json:"source_completion_digest"`
	SourceOutputDigest       string `json:"source_output_digest"`
	SourceTerminalEventID    string `json:"source_terminal_event_id"`
	SourceTerminalSequence   int64  `json:"source_terminal_sequence"`
	SourceTerminalDigest     string `json:"source_terminal_digest"`
	SourceDomain             string `json:"source_domain"`
	SourceProfile            string `json:"source_profile"`
	SourcePrivacy            string `json:"source_privacy"`
	SourceAdmissionID        string `json:"source_admission_id"`
	SourceAdmissionDigest    string `json:"source_admission_digest"`
	SourceModelID            string `json:"source_model_id"`
	SourceProviderID         string `json:"source_provider_id"`
	SourceConfigID           string `json:"source_config_id"`

	ReviewerID         string `json:"reviewer_id"`
	ReviewerModelID    string `json:"reviewer_model_id"`
	ReviewerProviderID string `json:"reviewer_provider_id"`
	ReviewerConfigID   string `json:"reviewer_config_id"`

	AuditID          string    `json:"audit_id"`
	AuditDigest      string    `json:"audit_digest"`
	EvidenceDigest   string    `json:"evidence_digest"`
	EvidenceCount    int       `json:"evidence_count"`
	SettlementDigest string    `json:"settlement_digest"`
	RecordedAt       time.Time `json:"recorded_at"`
	OutcomeDigest    string    `json:"outcome_digest"`
}

// Validate checks the self-contained canonical contract. ValidateBindings
// additionally proves that the duplicated identities came from the exact
// frozen request, admission, and completed settlement.
func (r AuxiliaryReviewOutcomeRecord) Validate() error {
	want, err := r.CanonicalDigest()
	if err != nil || r.Version != AuxiliaryReviewOutcomeVersion ||
		!validLifecycleIDs(r.OutcomeID, r.AdmissionID, r.OperationID, r.BoardID, r.CardID, r.AttemptID, r.ClaimID, r.CandidateID,
			r.SourceTaskID, r.SourceSessionID, r.SourceTurnID, r.SourceAttemptID, r.SourceCompletionEventID, r.SourceTerminalEventID,
			r.SourceAdmissionID, r.SourceProviderID, r.ReviewerID, r.ReviewerProviderID, r.AuditID) ||
		!digest(r.AdmissionDigest) || !digest(r.CandidateDigest) || !digest(r.CriteriaDigest) || !digest(r.PolicyDigest) ||
		!digest(r.SourceCompletionDigest) || !digest(r.SourceOutputDigest) || !digest(r.SourceTerminalDigest) ||
		!boundedText(r.SourceDomain, MaxIdentifierBytes, true) || !boundedText(r.SourceProfile, MaxIdentifierBytes, true) ||
		!boundedText(r.SourcePrivacy, MaxIdentifierBytes, true) ||
		!digest(r.SourceAdmissionDigest) || !boundedText(r.SourceModelID, MaxExecutionModelBytes, false) || !digest(r.SourceConfigID) ||
		!boundedText(r.ReviewerModelID, MaxExecutionModelBytes, false) || !digest(r.ReviewerConfigID) ||
		r.SourceCompletionSequence < 1 || r.SourceTerminalSequence <= r.SourceCompletionSequence ||
		!digest(r.AuditDigest) || !digest(r.EvidenceDigest) || r.EvidenceCount < 0 || r.EvidenceCount > MaxEvaluationEvidence ||
		!digest(r.SettlementDigest) || !validTime(r.RecordedAt) || !digest(r.OutcomeDigest) || r.OutcomeDigest != want {
		return fail(CodeInvalid, "auxiliary_review_outcome_record")
	}
	return nil
}

func (r AuxiliaryReviewOutcomeRecord) CanonicalDigest() (string, error) {
	r.OutcomeDigest = ""
	return executionDigest(r)
}

// ValidateBindings prevents a well-formed outcome from being transplanted to
// a different candidate, runtime turn, reviewer admission, or settlement.
func (r AuxiliaryReviewOutcomeRecord) ValidateBindings(frozen CandidateEvaluationRequest, admission AuxiliaryReviewAdmissionRecord,
	settlement AuxiliaryReviewSettlementRecord,
) error {
	if r.Validate() != nil || frozen.Validate() != nil || frozen.BindingKind != "runtime_budgeted" ||
		admission.Validate() != nil || settlement.Validate() != nil || settlement.Disposition != AuxiliaryReviewCompleted ||
		r.RecordedAt.Before(settlement.SettledAt) ||
		r.AdmissionID != admission.AdmissionID || r.AdmissionDigest != admission.AdmissionDigest || r.OperationID != admission.OperationID ||
		admission.BoardID != frozen.BoardID || admission.CardID != frozen.CardID || admission.AttemptID != frozen.AttemptID ||
		admission.ClaimID != frozen.ClaimID || admission.CandidateID != frozen.CandidateID ||
		admission.CandidateDigest != frozen.CandidateDigest || admission.CriteriaDigest != frozen.CriteriaDigest ||
		admission.PolicyDigest != frozen.PolicyDigest ||
		r.BoardID != frozen.BoardID || r.CardID != frozen.CardID || r.AttemptID != frozen.AttemptID || r.ClaimID != frozen.ClaimID ||
		r.CandidateID != frozen.CandidateID || r.CandidateDigest != frozen.CandidateDigest || r.CriteriaDigest != frozen.CriteriaDigest ||
		r.PolicyDigest != frozen.PolicyDigest ||
		r.SourceTaskID != frozen.SourceTaskID || r.SourceSessionID != frozen.SourceSessionID || r.SourceTurnID != frozen.SourceTurnID ||
		r.SourceAttemptID != frozen.SourceAttemptID || r.SourceCompletionEventID != frozen.SourceCompletionEventID ||
		r.SourceCompletionSequence != frozen.SourceCompletionSequence || r.SourceCompletionDigest != frozen.SourceCompletionDigest ||
		r.SourceOutputDigest != frozen.SourceOutputDigest || r.SourceTerminalEventID != frozen.SourceTerminalEventID ||
		r.SourceTerminalSequence != frozen.SourceTerminalSequence || r.SourceTerminalDigest != frozen.SourceTerminalDigest ||
		r.SourceDomain != frozen.SourceDomain || r.SourceProfile != frozen.SourceProfile || r.SourcePrivacy != frozen.SourcePrivacy ||
		r.SourceAdmissionID != frozen.AdmissionID || r.SourceAdmissionDigest != frozen.AdmissionDigest ||
		r.SourceModelID != frozen.SourceModelID || r.SourceProviderID != frozen.SourceProviderID || r.SourceConfigID != frozen.ConfigID ||
		r.ReviewerID != admission.ReviewerID || r.ReviewerModelID != admission.ModelID ||
		r.ReviewerProviderID != admission.ProviderID || r.ReviewerConfigID != admission.ConfigID ||
		r.SettlementDigest != settlement.SettlementDigest || settlement.AdmissionID != admission.AdmissionID ||
		settlement.AdmissionDigest != admission.AdmissionDigest || settlement.OperationID != admission.OperationID {
		return fail(CodeInvalid, "auxiliary_review_outcome_binding")
	}
	return nil
}
