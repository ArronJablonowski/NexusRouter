package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/workboard"
)

const auxiliaryReviewOutcomeColumns = `outcome_id,version,admission_id,admission_digest,operation_id,
	board_id,card_id,attempt_id,claim_id,candidate_id,candidate_digest,criteria_digest,policy_digest,
	source_task_id,source_session_id,source_turn_id,source_attempt_id,source_completion_event_id,
	source_completion_sequence,source_completion_digest,source_output_digest,source_terminal_event_id,
	source_terminal_sequence,source_terminal_digest,source_domain,source_profile,source_privacy,
	source_admission_id,source_admission_digest,source_model_id,source_provider_id,source_config_id,
	reviewer_id,reviewer_model_id,reviewer_provider_id,reviewer_config_id,audit_id,audit_digest,
	evidence_digest,evidence_count,settlement_digest,recorded_at,outcome_digest,body`

// ReplayAuxiliaryReviewOutcome returns the immutable successful result without
// granting reviewer redispatch authority. The legacy flag identifies only
// admissions sealed by the schema-45 migration.
func (s *Store) ReplayAuxiliaryReviewOutcome(ctx context.Context, operationID string) (workboard.AuxiliaryReviewOutcomeRecord, bool, bool, error) {
	if ctx == nil || s == nil || s.db == nil || !validWorkboardID(operationID) {
		return workboard.AuxiliaryReviewOutcomeRecord{}, false, false, invalidWorkboard("auxiliary_review_outcome")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return workboard.AuxiliaryReviewOutcomeRecord{}, false, false, err
	}
	defer tx.Rollback()
	admission, found, err := readAuxiliaryReviewAdmissionByOperation(ctx, tx, operationID)
	if err != nil || !found {
		return workboard.AuxiliaryReviewOutcomeRecord{}, false, false, err
	}
	legacy, err := isLegacyAuxiliaryReviewOutcomeAdmission(ctx, tx, admission)
	if err != nil {
		return workboard.AuxiliaryReviewOutcomeRecord{}, false, false, err
	}
	outcome, found, err := readAuxiliaryReviewOutcome(ctx, tx, admission.AdmissionID)
	if err != nil || legacy {
		if err == nil && found {
			err = ErrWorkboardCorrupt
		}
		return workboard.AuxiliaryReviewOutcomeRecord{}, false, legacy, err
	}
	if !found {
		return workboard.AuxiliaryReviewOutcomeRecord{}, false, false, nil
	}
	settlement, settled, err := readAuxiliaryReviewSettlement(ctx, tx, admission.AdmissionID)
	if err != nil || !settled {
		if err == nil {
			err = ErrWorkboardCorrupt
		}
		return workboard.AuxiliaryReviewOutcomeRecord{}, false, false, err
	}
	if _, err = validateAuxiliaryReviewOutcomeGraph(ctx, tx, admission, settlement, outcome); err != nil {
		return workboard.AuxiliaryReviewOutcomeRecord{}, false, false, err
	}
	if err = tx.Commit(); err != nil {
		return workboard.AuxiliaryReviewOutcomeRecord{}, false, false, err
	}
	return outcome, true, false, nil
}

// validateAuxiliaryReviewOutcomeGraph re-derives successful review authority
// from durable records. Callers must not acknowledge a replay from an outcome
// row alone: the admission, settlement, audit, runtime source, candidate, and
// evidence prefix are one immutable authority graph.
func validateAuxiliaryReviewOutcomeGraph(ctx context.Context, tx *sql.Tx, admission workboard.AuxiliaryReviewAdmissionRecord,
	settlement workboard.AuxiliaryReviewSettlementRecord, outcome workboard.AuxiliaryReviewOutcomeRecord,
) (evaluation.AuditRecord, error) {
	if settlement.Disposition != workboard.AuxiliaryReviewCompleted ||
		!outcomeMatchesAdmissionSettlement(outcome, admission, settlement) {
		return evaluation.AuditRecord{}, ErrWorkboardCorrupt
	}
	var auditTask string
	var auditBody []byte
	if err := tx.QueryRowContext(ctx, `SELECT task_id,body FROM audit_records WHERE id=?`, outcome.AuditID).Scan(&auditTask, &auditBody); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrWorkboardCorrupt
		}
		return evaluation.AuditRecord{}, err
	}
	audit, err := decodeAuditRecord(auditBody, outcome.AuditID, auditTask)
	digest, digestErr := evaluation.AuditRecordDigest(audit)
	if err != nil || digestErr != nil || digest != outcome.AuditDigest || !reviewAuditMatchesOutcome(audit, outcome, admission, settlement) {
		return evaluation.AuditRecord{}, ErrWorkboardCorrupt
	}
	if err = validateOutcomeRuntimeSource(ctx, tx, outcome); err != nil {
		return evaluation.AuditRecord{}, err
	}
	candidate, candidateErr := readEvaluationCandidate(ctx, tx, outcome.BoardID, outcome.CardID, outcome.AttemptID)
	if candidateErr != nil || candidate.ID != outcome.CandidateID || candidate.Digest != outcome.CandidateDigest ||
		candidate.CriteriaDigest != outcome.CriteriaDigest || candidate.PolicyDigest != outcome.PolicyDigest ||
		candidate.EvidenceDigest != outcome.EvidenceDigest || candidate.EvidenceCount != outcome.EvidenceCount {
		return evaluation.AuditRecord{}, ErrWorkboardCorrupt
	}
	evidence, evidenceErr := readEvaluationEvidence(ctx, tx, outcome.BoardID, outcome.CardID, outcome.AttemptID, candidate)
	if evidenceErr != nil || len(evidence) < outcome.EvidenceCount ||
		workboard.EvidenceSetDigest(evidence[:outcome.EvidenceCount]) != outcome.EvidenceDigest {
		return evaluation.AuditRecord{}, ErrWorkboardCorrupt
	}
	return audit, nil
}

func insertAuxiliaryReviewOutcomeTx(ctx context.Context, tx *sql.Tx, record workboard.AuxiliaryReviewOutcomeRecord) error {
	if record.Validate() != nil {
		return invalidWorkboard("auxiliary_review_outcome")
	}
	record.RecordedAt = record.RecordedAt.UTC()
	body, err := json.Marshal(record)
	if err != nil || len(body) < 1 || len(body) > workboard.MaxTransactionBytes {
		return invalidWorkboard("auxiliary_review_outcome")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workboard_auxiliary_review_outcomes(`+auxiliaryReviewOutcomeColumns+`)
		VALUES(`+questionMarks(44)+`)`, auxiliaryReviewOutcomeValues(record, body)...)
	return err
}

func readAuxiliaryReviewOutcome(ctx context.Context, tx *sql.Tx, admissionID string) (workboard.AuxiliaryReviewOutcomeRecord, bool, error) {
	if !validWorkboardID(admissionID) {
		return workboard.AuxiliaryReviewOutcomeRecord{}, false, invalidWorkboard("auxiliary_review_outcome")
	}
	row := tx.QueryRowContext(ctx, `SELECT `+auxiliaryReviewOutcomeColumns+`
		FROM workboard_auxiliary_review_outcomes WHERE admission_id=?`, admissionID)
	var indexed workboard.AuxiliaryReviewOutcomeRecord
	var recordedAt int64
	var body []byte
	err := row.Scan(&indexed.OutcomeID, &indexed.Version, &indexed.AdmissionID, &indexed.AdmissionDigest, &indexed.OperationID,
		&indexed.BoardID, &indexed.CardID, &indexed.AttemptID, &indexed.ClaimID, &indexed.CandidateID, &indexed.CandidateDigest,
		&indexed.CriteriaDigest, &indexed.PolicyDigest, &indexed.SourceTaskID, &indexed.SourceSessionID, &indexed.SourceTurnID,
		&indexed.SourceAttemptID, &indexed.SourceCompletionEventID, &indexed.SourceCompletionSequence,
		&indexed.SourceCompletionDigest, &indexed.SourceOutputDigest, &indexed.SourceTerminalEventID,
		&indexed.SourceTerminalSequence, &indexed.SourceTerminalDigest, &indexed.SourceDomain, &indexed.SourceProfile,
		&indexed.SourcePrivacy, &indexed.SourceAdmissionID, &indexed.SourceAdmissionDigest, &indexed.SourceModelID,
		&indexed.SourceProviderID, &indexed.SourceConfigID, &indexed.ReviewerID, &indexed.ReviewerModelID,
		&indexed.ReviewerProviderID, &indexed.ReviewerConfigID, &indexed.AuditID, &indexed.AuditDigest,
		&indexed.EvidenceDigest, &indexed.EvidenceCount, &indexed.SettlementDigest, &recordedAt, &indexed.OutcomeDigest, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return workboard.AuxiliaryReviewOutcomeRecord{}, false, nil
	}
	if err != nil {
		return workboard.AuxiliaryReviewOutcomeRecord{}, false, err
	}
	indexed.RecordedAt = time.Unix(0, recordedAt).UTC()
	var canonical workboard.AuxiliaryReviewOutcomeRecord
	if json.Unmarshal(body, &canonical) != nil || canonical.Validate() != nil || !sameAuxiliaryReviewOutcome(canonical, indexed) {
		return workboard.AuxiliaryReviewOutcomeRecord{}, false, ErrWorkboardCorrupt
	}
	canonicalBody, err := json.Marshal(canonical)
	if err != nil || string(canonicalBody) != string(body) {
		return workboard.AuxiliaryReviewOutcomeRecord{}, false, ErrWorkboardCorrupt
	}
	return canonical, true, nil
}

func isLegacyAuxiliaryReviewOutcomeAdmission(ctx context.Context, tx *sql.Tx, admission workboard.AuxiliaryReviewAdmissionRecord) (bool, error) {
	var digest string
	err := tx.QueryRowContext(ctx, `SELECT admission_digest FROM workboard_auxiliary_review_legacy_outcome_admissions
		WHERE admission_id=?`, admission.AdmissionID).Scan(&digest)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if digest != admission.AdmissionDigest {
		return false, ErrWorkboardCorrupt
	}
	return true, nil
}

func reviewAuditMatchesFrozen(audit evaluation.AuditRecord, frozen workboard.CandidateEvaluationRequest,
	mutation workboard.EvaluationMutation,
) bool {
	_, offset := audit.Time.Zone()
	if audit.Validate() != nil || offset != 0 || audit.TaskID != frozen.SourceTaskID || audit.AttemptID != frozen.SourceAttemptID ||
		audit.Audit.Domain != frozen.SourceDomain || mutation.BoardID != frozen.BoardID || mutation.CardID != frozen.CardID ||
		mutation.AttemptID != frozen.AttemptID || mutation.ClaimID != frozen.ClaimID || mutation.CandidateID != frozen.CandidateID ||
		mutation.CandidateDigest != frozen.CandidateDigest {
		return false
	}
	return workboard.ValidateBudgetedCandidateEvidence(frozen, audit, mutation.Evaluated) == nil
}

func reviewAuditMeasurementsMatch(audit evaluation.AuditRecord, measurements workboard.AuxiliaryReviewMeasurements) bool {
	if audit.Usage == nil {
		if measurements.Tokens != nil {
			return false
		}
	} else if audit.Usage.InputTokens > workboard.MaxWorkTokens-audit.Usage.OutputTokens || measurements.Tokens == nil ||
		*measurements.Tokens != audit.Usage.InputTokens+audit.Usage.OutputTokens {
		return false
	}
	if audit.Elapsed == 0 {
		return measurements.TimeMS == nil
	}
	millis := audit.Elapsed.Milliseconds()
	if audit.Elapsed%time.Millisecond != 0 {
		millis++
	}
	return measurements.TimeMS != nil && *measurements.TimeMS == millis
}

func reviewAuditMatchesOutcome(audit evaluation.AuditRecord, outcome workboard.AuxiliaryReviewOutcomeRecord,
	admission workboard.AuxiliaryReviewAdmissionRecord, settlement workboard.AuxiliaryReviewSettlementRecord,
) bool {
	return audit.Validate() == nil && audit.ID == outcome.AuditID && audit.TaskID == outcome.SourceTaskID &&
		audit.AttemptID == outcome.SourceAttemptID && audit.EvaluatorModel == outcome.ReviewerModelID &&
		audit.EvaluatorProvider == outcome.ReviewerProviderID && audit.Audit.EvaluatorID == outcome.ReviewerID &&
		audit.Audit.Domain == outcome.SourceDomain && !audit.Time.Before(admission.AdmittedAt) &&
		!audit.Time.Add(-audit.Elapsed).Before(admission.AdmittedAt) &&
		!audit.Time.After(settlement.SettledAt) && !audit.Time.After(outcome.RecordedAt)
}

func outcomeMatchesAdmissionSettlement(outcome workboard.AuxiliaryReviewOutcomeRecord, admission workboard.AuxiliaryReviewAdmissionRecord,
	settlement workboard.AuxiliaryReviewSettlementRecord,
) bool {
	return outcome.Validate() == nil && admission.Validate() == nil && settlement.Validate() == nil &&
		outcome.AdmissionID == admission.AdmissionID && outcome.AdmissionDigest == admission.AdmissionDigest &&
		outcome.OperationID == admission.OperationID && outcome.BoardID == admission.BoardID && outcome.CardID == admission.CardID &&
		outcome.AttemptID == admission.AttemptID && outcome.ClaimID == admission.ClaimID && outcome.CandidateID == admission.CandidateID &&
		outcome.CandidateDigest == admission.CandidateDigest && outcome.CriteriaDigest == admission.CriteriaDigest &&
		outcome.PolicyDigest == admission.PolicyDigest && outcome.ReviewerID == admission.ReviewerID &&
		outcome.ReviewerModelID == admission.ModelID && outcome.ReviewerProviderID == admission.ProviderID &&
		outcome.ReviewerConfigID == admission.ConfigID && outcome.SettlementDigest == settlement.SettlementDigest &&
		settlement.AdmissionID == admission.AdmissionID && !outcome.RecordedAt.Before(settlement.SettledAt)
}

func auxiliaryReviewCommitBindings(frozen workboard.CandidateEvaluationRequest, mutation workboard.EvaluationMutation,
	audit evaluation.AuditRecord, admission workboard.AuxiliaryReviewAdmissionRecord,
) bool {
	return admission.Validate() == nil && admission.OperationID == "candidate-review-"+frozen.CandidateID &&
		admission.BoardID == frozen.BoardID && admission.CardID == frozen.CardID && admission.AttemptID == frozen.AttemptID &&
		admission.ClaimID == frozen.ClaimID && admission.CandidateID == frozen.CandidateID &&
		admission.CandidateDigest == frozen.CandidateDigest && admission.CriteriaDigest == frozen.CriteriaDigest &&
		admission.PolicyDigest == frozen.PolicyDigest && admission.ReviewerID == audit.Audit.EvaluatorID &&
		admission.ModelID == audit.EvaluatorModel && admission.ProviderID == audit.EvaluatorProvider &&
		mutation.CriteriaDigest == "" && mutation.PolicyDigest == ""
}

func validateFrozenRuntimeSource(ctx context.Context, tx *sql.Tx, frozen workboard.CandidateEvaluationRequest) error {
	indexed, admission, found, err := readExecutionAdmission(ctx, tx, frozen.SourceTaskID)
	if err != nil || !found {
		if err != nil {
			return err
		}
		return ErrWorkboardCorrupt
	}
	if indexed != admission || admission.AdmissionID != frozen.AdmissionID || admission.AdmissionDigest != frozen.AdmissionDigest ||
		admission.TaskID != frozen.SourceTaskID || admission.SessionID != frozen.SourceSessionID ||
		admission.BoardID != frozen.BoardID || admission.CardID != frozen.CardID || admission.AttemptID != frozen.AttemptID ||
		admission.ClaimID != frozen.ClaimID || admission.ModelID != frozen.SourceModelID ||
		admission.ProviderID != frozen.SourceProviderID || admission.ConfigID != frozen.ConfigID ||
		admission.PolicyDigest != frozen.PolicyDigest {
		return ErrWorkboardCorrupt
	}
	source, err := candidateSourceCompletion(ctx, tx, frozen.SourceTaskID, frozen.SourceSessionID)
	if err != nil || source.completion.ID != frozen.SourceCompletionEventID || source.completion.TurnID != frozen.SourceTurnID ||
		source.completion.AttemptID != frozen.SourceAttemptID || source.completion.Sequence != frozen.SourceCompletionSequence ||
		source.completionDigest != frozen.SourceCompletionDigest || source.output != frozen.SourceOutput || source.outputDigest != frozen.SourceOutputDigest ||
		source.terminal.ID != frozen.SourceTerminalEventID || source.terminal.Sequence != frozen.SourceTerminalSequence ||
		source.terminalDigest != frozen.SourceTerminalDigest || source.domain != frozen.SourceDomain ||
		source.profile != frozen.SourceProfile || source.privacy != frozen.SourcePrivacy {
		return ErrWorkboardCorrupt
	}
	return nil
}

func validateOutcomeRuntimeSource(ctx context.Context, tx *sql.Tx, outcome workboard.AuxiliaryReviewOutcomeRecord) error {
	indexed, admission, found, err := readExecutionAdmission(ctx, tx, outcome.SourceTaskID)
	if err != nil || !found {
		if err != nil {
			return err
		}
		return ErrWorkboardCorrupt
	}
	if indexed != admission || admission.AdmissionID != outcome.SourceAdmissionID ||
		admission.AdmissionDigest != outcome.SourceAdmissionDigest || admission.SessionID != outcome.SourceSessionID ||
		admission.BoardID != outcome.BoardID || admission.CardID != outcome.CardID || admission.AttemptID != outcome.AttemptID ||
		admission.ClaimID != outcome.ClaimID || admission.ModelID != outcome.SourceModelID ||
		admission.ProviderID != outcome.SourceProviderID || admission.ConfigID != outcome.SourceConfigID ||
		admission.PolicyDigest != outcome.PolicyDigest {
		return ErrWorkboardCorrupt
	}
	source, err := candidateSourceCompletion(ctx, tx, outcome.SourceTaskID, outcome.SourceSessionID)
	if err != nil || source.completion.ID != outcome.SourceCompletionEventID || source.completion.TurnID != outcome.SourceTurnID ||
		source.completion.AttemptID != outcome.SourceAttemptID || source.completion.Sequence != outcome.SourceCompletionSequence ||
		source.completionDigest != outcome.SourceCompletionDigest || source.outputDigest != outcome.SourceOutputDigest ||
		source.terminal.ID != outcome.SourceTerminalEventID || source.terminal.Sequence != outcome.SourceTerminalSequence ||
		source.terminalDigest != outcome.SourceTerminalDigest || source.domain != outcome.SourceDomain ||
		source.profile != outcome.SourceProfile || source.privacy != outcome.SourcePrivacy {
		return ErrWorkboardCorrupt
	}
	return nil
}

func buildAuxiliaryReviewOutcome(frozen workboard.CandidateEvaluationRequest, admission workboard.AuxiliaryReviewAdmissionRecord,
	settlement workboard.AuxiliaryReviewSettlementRecord, audit evaluation.AuditRecord, auditDigest, evidenceDigest string,
	evidenceCount int, recordedAt time.Time,
) (workboard.AuxiliaryReviewOutcomeRecord, error) {
	record := workboard.AuxiliaryReviewOutcomeRecord{Version: workboard.AuxiliaryReviewOutcomeVersion, OutcomeID: newWorkboardID(),
		AdmissionID: admission.AdmissionID, AdmissionDigest: admission.AdmissionDigest, OperationID: admission.OperationID,
		BoardID: frozen.BoardID, CardID: frozen.CardID, AttemptID: frozen.AttemptID, ClaimID: frozen.ClaimID,
		CandidateID: frozen.CandidateID, CandidateDigest: frozen.CandidateDigest, CriteriaDigest: frozen.CriteriaDigest,
		PolicyDigest: frozen.PolicyDigest, SourceTaskID: frozen.SourceTaskID, SourceSessionID: frozen.SourceSessionID,
		SourceTurnID: frozen.SourceTurnID, SourceAttemptID: frozen.SourceAttemptID,
		SourceCompletionEventID: frozen.SourceCompletionEventID, SourceCompletionSequence: frozen.SourceCompletionSequence,
		SourceCompletionDigest: frozen.SourceCompletionDigest, SourceOutputDigest: frozen.SourceOutputDigest,
		SourceTerminalEventID: frozen.SourceTerminalEventID, SourceTerminalSequence: frozen.SourceTerminalSequence,
		SourceTerminalDigest: frozen.SourceTerminalDigest, SourceDomain: frozen.SourceDomain, SourceProfile: frozen.SourceProfile,
		SourcePrivacy: frozen.SourcePrivacy, SourceAdmissionID: frozen.AdmissionID, SourceAdmissionDigest: frozen.AdmissionDigest,
		SourceModelID: frozen.SourceModelID, SourceProviderID: frozen.SourceProviderID, SourceConfigID: frozen.ConfigID,
		ReviewerID: admission.ReviewerID, ReviewerModelID: admission.ModelID, ReviewerProviderID: admission.ProviderID,
		ReviewerConfigID: admission.ConfigID, AuditID: audit.ID, AuditDigest: auditDigest, EvidenceDigest: evidenceDigest,
		EvidenceCount: evidenceCount, SettlementDigest: settlement.SettlementDigest, RecordedAt: recordedAt.UTC()}
	if record.OutcomeID == "" {
		return workboard.AuxiliaryReviewOutcomeRecord{}, errors.New("secure identifier generation failed")
	}
	var err error
	record.OutcomeDigest, err = record.CanonicalDigest()
	if err != nil || record.ValidateBindings(frozen, admission, settlement) != nil {
		return workboard.AuxiliaryReviewOutcomeRecord{}, invalidWorkboard("auxiliary_review_outcome")
	}
	return record, nil
}

func auxiliaryReviewOutcomeValues(r workboard.AuxiliaryReviewOutcomeRecord, body []byte) []any {
	return []any{r.OutcomeID, r.Version, r.AdmissionID, r.AdmissionDigest, r.OperationID,
		r.BoardID, r.CardID, r.AttemptID, r.ClaimID, r.CandidateID, r.CandidateDigest, r.CriteriaDigest, r.PolicyDigest,
		r.SourceTaskID, r.SourceSessionID, r.SourceTurnID, r.SourceAttemptID, r.SourceCompletionEventID,
		r.SourceCompletionSequence, r.SourceCompletionDigest, r.SourceOutputDigest, r.SourceTerminalEventID,
		r.SourceTerminalSequence, r.SourceTerminalDigest, r.SourceDomain, r.SourceProfile, r.SourcePrivacy,
		r.SourceAdmissionID, r.SourceAdmissionDigest, r.SourceModelID, r.SourceProviderID, r.SourceConfigID,
		r.ReviewerID, r.ReviewerModelID, r.ReviewerProviderID, r.ReviewerConfigID, r.AuditID, r.AuditDigest,
		r.EvidenceDigest, r.EvidenceCount, r.SettlementDigest, r.RecordedAt.UnixNano(), r.OutcomeDigest, body}
}

func sameAuxiliaryReviewOutcome(a, b workboard.AuxiliaryReviewOutcomeRecord) bool {
	a.RecordedAt, b.RecordedAt = a.RecordedAt.UTC(), b.RecordedAt.UTC()
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && string(left) == string(right)
}

func questionMarks(count int) string {
	if count < 1 {
		return ""
	}
	out := "?"
	for i := 1; i < count; i++ {
		out += ",?"
	}
	return out
}
