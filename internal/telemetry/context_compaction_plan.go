package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/processguard"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

type contextCompactionQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// BeginContextCompactionPlan atomically records the caller operation, its
// summary attempt and the started fact before auxiliary inference can run.
// Exact retries return the current projection and never repeat inference.
func (s *Store) BeginContextCompactionPlan(ctx context.Context, start sessions.ContextCompactionPlanStart) (sessions.ContextCompactionOperationState, bool, error) {
	zero := sessions.ContextCompactionOperationState{}
	if ctx == nil || start.Validate() != nil {
		return zero, false, sessions.ErrContextCompactionLifecycle
	}
	process, err := processguard.Current(ctx)
	if err != nil || process.Validate() != nil || process.ID != start.ProcessID {
		return zero, false, sessions.ErrContextCompactionLifecycle
	}
	attempt := summaryAttemptForCompactionStart(start)
	if attempt.Validate() != nil {
		return zero, false, sessions.ErrContextCompactionLifecycle
	}
	if err = s.verifySummarySource(ctx, attempt); err != nil {
		return zero, false, err
	}
	startBody, err := json.Marshal(start)
	if err != nil || len(startBody) > sessions.MaxContextCompactionLifecycleBytes {
		return zero, false, sessions.ErrContextCompactionLifecycle
	}
	attemptBody, err := json.Marshal(attempt)
	if err != nil {
		return zero, false, err
	}
	started, err := contextCompactionStartedFact(start)
	if err != nil {
		return zero, false, err
	}
	factBody, err := json.Marshal(started)
	if err != nil {
		return zero, false, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, false, err
	}
	defer tx.Rollback()
	// The first write obtains the SQLite writer reservation before idempotency
	// discovery. The completed source head is also the request's durable fence.
	if result, updateErr := tx.ExecContext(ctx, "UPDATE task_heads SET sequence=sequence WHERE task_id=?", start.TaskID); updateErr != nil {
		return zero, false, updateErr
	} else if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		return zero, false, ErrConflict
	}
	if err = registerLeaseProcess(ctx, tx, process); err != nil {
		return zero, false, err
	}
	var prior []byte
	err = tx.QueryRowContext(ctx, "SELECT body FROM context_compaction_operations WHERE operation_id=?", start.OperationID).Scan(&prior)
	if err == nil {
		state, readErr := readContextCompactionPlanState(ctx, tx, start.OperationID)
		if readErr != nil {
			return zero, false, readErr
		}
		// Process ownership, wall time and the operation envelope digest may
		// legitimately differ after a lost acknowledgement and restart. The
		// immutable IDs plus semantic request digest identify the retry.
		if state.Start.RequestID != start.RequestID || state.Start.AttemptID != start.AttemptID || state.Start.RequestDigest != start.RequestDigest {
			return zero, false, ErrConflict
		}
		if err = tx.Commit(); err != nil {
			return zero, false, err
		}
		return state, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return zero, false, err
	}
	var collision string
	err = tx.QueryRowContext(ctx, "SELECT operation_id FROM context_compaction_operations WHERE request_id=? OR attempt_id=?", start.RequestID, start.AttemptID).Scan(&collision)
	if err == nil {
		return zero, false, ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return zero, false, err
	}
	var attemptCollision string
	err = tx.QueryRowContext(ctx, "SELECT id FROM summary_attempts WHERE id=?", start.AttemptID).Scan(&attemptCollision)
	if err == nil {
		return zero, false, ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return zero, false, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO summary_attempts(id,task_id,body,process_id) VALUES(?,?,?,?)", attempt.ID, attempt.TaskID, attemptBody, process.ID); err != nil {
		return zero, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO context_compaction_operations
		(operation_id,operation_digest,request_id,request_digest,task_id,source_sequence,source_digest,attempt_id,model,provider,keep,estimated_cost,config_digest,policy_digest,engine_digest,tier_digest,process_id,started_at,status,body)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, start.OperationID, start.OperationDigest, start.RequestID, start.RequestDigest,
		start.TaskID, start.SourceSequence, start.SourceDigest, start.AttemptID, start.Model, start.Provider, start.Keep,
		start.EstimatedCost, start.ConfigDigest, start.PolicyDigest, start.Engine.Digest, start.Tiers.Digest, start.ProcessID,
		start.StartedAt.Format(time.RFC3339Nano), string(start.Status), startBody); err != nil {
		return zero, false, err
	}
	if err = insertContextCompactionFact(ctx, tx, started, factBody); err != nil {
		return zero, false, err
	}
	state, err := readContextCompactionPlanState(ctx, tx, start.OperationID)
	if err != nil {
		return zero, false, err
	}
	if err = tx.Commit(); err != nil {
		return zero, false, err
	}
	return state, true, nil
}

func (s *Store) ContextCompactionPlan(ctx context.Context, operationID string) (sessions.ContextCompactionOperationState, error) {
	if ctx == nil {
		return sessions.ContextCompactionOperationState{}, sessions.ErrContextCompactionLifecycle
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return sessions.ContextCompactionOperationState{}, err
	}
	defer tx.Rollback()
	state, err := readContextCompactionPlanState(ctx, tx, operationID)
	if err != nil {
		return sessions.ContextCompactionOperationState{}, err
	}
	if err = tx.Commit(); err != nil {
		return sessions.ContextCompactionOperationState{}, err
	}
	return state, nil
}

// ContextCompactionPlanForAttempt resolves the unique schema-50 operation
// owning a summary attempt. It returns the same fully validated projection as
// ContextCompactionPlan and never treats indexed columns as trusted state.
func (s *Store) ContextCompactionPlanForAttempt(ctx context.Context, attemptID string) (sessions.ContextCompactionOperationState, error) {
	zero := sessions.ContextCompactionOperationState{}
	if ctx == nil || attemptID == "" || len(attemptID) > 128 {
		return zero, sessions.ErrContextCompactionLifecycle
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	var operationID string
	if err = tx.QueryRowContext(ctx, "SELECT operation_id FROM context_compaction_operations WHERE attempt_id=?", attemptID).Scan(&operationID); err != nil {
		return zero, err
	}
	state, err := readContextCompactionPlanState(ctx, tx, operationID)
	if err != nil || state.Start.AttemptID != attemptID {
		if err != nil {
			return zero, err
		}
		return zero, sessions.ErrContextCompactionLifecycle
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	return state, nil
}

func summaryAttemptForCompactionStart(start sessions.ContextCompactionPlanStart) sessions.SummaryAttempt {
	return sessions.SummaryAttempt{Version: 1, ID: start.AttemptID, TaskID: start.TaskID, SourceDigest: start.SourceDigest,
		Model: start.Model, Provider: start.Provider, Status: "started", SourceSequence: start.SourceSequence,
		Keep: start.Keep, EstimatedCost: start.EstimatedCost, StartedAt: start.StartedAt}
}

func contextCompactionStartedFact(start sessions.ContextCompactionPlanStart) (sessions.ContextCompactionLifecycleFact, error) {
	return sessions.SealContextCompactionLifecycleFact(sessions.ContextCompactionLifecycleFact{
		ID: "cpf_" + start.OperationDigest, OperationID: start.OperationID, Sequence: 1,
		Kind: sessions.ContextCompactionStarted, CreatedAt: start.StartedAt,
	})
}

// PrepareContextCompactionPlan commits the exact sealed plan and prepared fact
// together. The current deterministic v2 approval and source are re-derived in
// the same writer transaction.
func (s *Store) PrepareContextCompactionPlan(ctx context.Context, plan runtime.ContextCompactionPlan, fact sessions.ContextCompactionLifecycleFact) (sessions.ContextCompactionOperationState, error) {
	if plan.Validate() != nil || fact.Validate() != nil || fact.Kind != sessions.ContextCompactionPrepared || fact.OperationID != plan.OperationID ||
		fact.PlanDigest != plan.PlanDigest || fact.SummaryAttemptID != plan.Compaction.SummaryAttemptID || fact.SummaryReviewID != plan.Compaction.SummaryReviewID {
		return sessions.ContextCompactionOperationState{}, sessions.ErrContextCompactionLifecycle
	}
	return s.advanceContextCompactionPlan(ctx, &plan, fact, true)
}

func (s *Store) ValidateContextCompactionPlan(ctx context.Context, fact sessions.ContextCompactionLifecycleFact) (sessions.ContextCompactionOperationState, error) {
	if fact.Kind != sessions.ContextCompactionValidated {
		return sessions.ContextCompactionOperationState{}, sessions.ErrContextCompactionLifecycle
	}
	return s.advanceContextCompactionPlan(ctx, nil, fact, true)
}

func (s *Store) ApproveContextCompactionPlan(ctx context.Context, fact sessions.ContextCompactionLifecycleFact) (sessions.ContextCompactionOperationState, error) {
	if fact.Kind != sessions.ContextCompactionApproved {
		return sessions.ContextCompactionOperationState{}, sessions.ErrContextCompactionLifecycle
	}
	return s.advanceContextCompactionPlan(ctx, nil, fact, true)
}

func (s *Store) RevokeContextCompactionPlan(ctx context.Context, fact sessions.ContextCompactionLifecycleFact) (sessions.ContextCompactionOperationState, error) {
	if fact.Kind != sessions.ContextCompactionRevoked {
		return sessions.ContextCompactionOperationState{}, sessions.ErrContextCompactionLifecycle
	}
	return s.advanceContextCompactionPlan(ctx, nil, fact, false)
}

func (s *Store) ActivateContextCompactionPlan(ctx context.Context, fact sessions.ContextCompactionLifecycleFact) (sessions.ContextCompactionOperationState, error) {
	// DAR-120 must append activation and the bound ContextCompacted event in one
	// writer transaction. A standalone lifecycle write would create false
	// authority if the event insert failed or named an absent live suffix.
	return sessions.ContextCompactionOperationState{}, sessions.ErrContextCompactionLifecycle
}

func contextCompactionPlanExists(ctx context.Context, tx *sql.Tx, event runtime.Event) (bool, error) {
	if event.Data.Compaction == nil {
		return false, sessions.ErrContextCompactionLifecycle
	}
	var exists bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM context_compaction_plans
		WHERE summary_attempt_id=? AND summary_review_id=?)`, event.Data.Compaction.SummaryAttemptID, event.Data.Compaction.SummaryReviewID).Scan(&exists)
	return exists, err
}

// prepareContextCompactionActivation derives the suffix from the journal while
// the same SQLite writer transaction is held. The caller inserts both this fact
// and the ContextCompacted event before committing, so neither can become
// durable without the other.
func prepareContextCompactionActivation(ctx context.Context, tx *sql.Tx, event runtime.Event, plan runtime.ContextCompactionPlan) (sessions.ContextCompactionLifecycleFact, error) {
	zero := sessions.ContextCompactionLifecycleFact{}
	if plan.Validate() != nil || event.Kind != runtime.ContextCompacted || event.Data.Compaction == nil ||
		!reflect.DeepEqual(event.Data.Compaction, plan.Compaction) || !reflect.DeepEqual(event.Data.Messages, plan.ReplacementPrefix) ||
		event.Data.ReplacedMessages != len(plan.OriginalPrefix) {
		return zero, sessions.ErrContextCompactionLifecycle
	}
	state, err := readContextCompactionPlanState(ctx, tx, plan.OperationID)
	if err != nil {
		return zero, err
	}
	if state.Status != sessions.ContextCompactionApproved || state.Plan == nil || !reflect.DeepEqual(*state.Plan, plan) {
		return zero, sessions.ErrContextCompactionLifecycle
	}
	if err = validateContextCompactionPlanEvidence(ctx, tx, state.Start, plan, true); err != nil {
		return zero, err
	}
	var parentEvents []runtime.Event
	current, err := taskSnapshotWithEvents(ctx, tx, event.TaskID, &parentEvents)
	if err != nil || current.State != "running" || current.SessionID != event.SessionID || current.Sequence != event.Sequence-1 ||
		len(current.Messages) < plan.LiveSuffixBoundary || !reflect.DeepEqual(current.Messages[:plan.LiveSuffixBoundary], plan.OriginalPrefix) {
		return zero, sessions.ErrHistory
	}
	suffix := current.Messages[plan.LiveSuffixBoundary:]
	suffixDigest, err := sessions.ContextCompactionLiveSuffixDigest(suffix)
	if err != nil {
		return zero, err
	}
	delegations, err := deriveDelegationCompactionBindings(ctx, tx, parentEvents, suffix, event.TaskID, state.Start, plan)
	if err != nil {
		return zero, err
	}
	activation, err := sessions.SealContextCompactionActivation(sessions.ContextCompactionActivation{
		OperationID: plan.OperationID, PlanDigest: plan.PlanDigest, TaskID: event.TaskID, EventID: event.ID,
		EventSequence: event.Sequence, LiveSuffixBoundary: plan.LiveSuffixBoundary, LiveSuffixCount: len(suffix),
		LiveSuffixDigest: suffixDigest, Delegations: delegations, ActivatedAt: event.Time,
	})
	if err != nil {
		return zero, err
	}
	latest := state.Facts[len(state.Facts)-1]
	fact, err := sessions.SealContextCompactionLifecycleFact(sessions.ContextCompactionLifecycleFact{
		ID: "cpf_" + activation.ActivationDigest, OperationID: plan.OperationID, Sequence: latest.Sequence + 1,
		PreviousID: latest.ID, Kind: sessions.ContextCompactionActivated, PlanDigest: plan.PlanDigest,
		SummaryAttemptID: plan.Compaction.SummaryAttemptID, SummaryReviewID: plan.Compaction.SummaryReviewID,
		Activation: &activation, CreatedAt: event.Time,
	})
	if err != nil || sessions.ValidateContextCompactionTransition(&latest, fact) != nil {
		return zero, sessions.ErrContextCompactionLifecycle
	}
	return fact, nil
}

func validateContextCompactionActivationRetry(ctx context.Context, tx *sql.Tx, event runtime.Event, plan runtime.ContextCompactionPlan) error {
	state, err := readContextCompactionPlanState(ctx, tx, plan.OperationID)
	if err != nil {
		return err
	}
	if state.Status != sessions.ContextCompactionActivated || state.Plan == nil || !reflect.DeepEqual(*state.Plan, plan) || len(state.Facts) == 0 {
		return ErrConflict
	}
	fact := state.Facts[len(state.Facts)-1]
	activation := fact.Activation
	if fact.Kind != sessions.ContextCompactionActivated || activation == nil || fact.PlanDigest != plan.PlanDigest ||
		activation.OperationID != plan.OperationID || activation.PlanDigest != plan.PlanDigest || activation.TaskID != event.TaskID ||
		activation.EventID != event.ID || activation.EventSequence != event.Sequence || activation.LiveSuffixBoundary != plan.LiveSuffixBoundary ||
		!activation.ActivatedAt.Equal(event.Time) {
		return ErrConflict
	}
	parentEvents, messages, err := parentEventsBeforeCompaction(ctx, tx, event.TaskID, event.Sequence)
	if err != nil || len(messages) < plan.LiveSuffixBoundary || !reflect.DeepEqual(messages[:plan.LiveSuffixBoundary], plan.OriginalPrefix) {
		return ErrConflict
	}
	suffix := messages[plan.LiveSuffixBoundary:]
	digest, err := sessions.ContextCompactionLiveSuffixDigest(suffix)
	if err != nil || digest != activation.LiveSuffixDigest || len(suffix) != activation.LiveSuffixCount {
		return ErrConflict
	}
	bindings, err := deriveDelegationCompactionBindings(ctx, tx, parentEvents, suffix, event.TaskID, state.Start, plan)
	if err != nil {
		return ErrConflict
	}
	return validateContextCompactionDelegationRetry(ctx, tx, fact, bindings)
}

func (s *Store) FailContextCompactionPlan(ctx context.Context, fact sessions.ContextCompactionLifecycleFact) (sessions.ContextCompactionOperationState, error) {
	if fact.Kind != sessions.ContextCompactionFailed {
		return sessions.ContextCompactionOperationState{}, sessions.ErrContextCompactionLifecycle
	}
	return s.advanceContextCompactionPlan(ctx, nil, fact, false)
}

func (s *Store) advanceContextCompactionPlan(ctx context.Context, prepared *runtime.ContextCompactionPlan, fact sessions.ContextCompactionLifecycleFact, requireCurrentReview bool) (sessions.ContextCompactionOperationState, error) {
	zero := sessions.ContextCompactionOperationState{}
	if ctx == nil || fact.Validate() != nil {
		return zero, sessions.ErrContextCompactionLifecycle
	}
	factBody, err := json.Marshal(fact)
	if err != nil {
		return zero, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	var task string
	if err = tx.QueryRowContext(ctx, "SELECT task_id FROM context_compaction_operations WHERE operation_id=?", fact.OperationID).Scan(&task); err != nil {
		return zero, err
	}
	if result, updateErr := tx.ExecContext(ctx, "UPDATE task_heads SET sequence=sequence WHERE task_id=?", task); updateErr != nil {
		return zero, updateErr
	} else if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		return zero, ErrConflict
	}
	var priorBody, priorOperation []byte
	err = tx.QueryRowContext(ctx, "SELECT body,operation_id FROM context_compaction_plan_facts WHERE fact_id=?", fact.ID).Scan(&priorBody, &priorOperation)
	if err == nil {
		if !bytes.Equal(priorBody, factBody) || string(priorOperation) != fact.OperationID {
			return zero, ErrConflict
		}
		state, readErr := readContextCompactionPlanState(ctx, tx, fact.OperationID)
		if readErr != nil {
			return zero, readErr
		}
		if err = tx.Commit(); err != nil {
			return zero, err
		}
		return state, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	state, err := readContextCompactionPlanState(ctx, tx, fact.OperationID)
	if err != nil {
		return zero, err
	}
	latest := state.Facts[len(state.Facts)-1]
	if sessions.ValidateContextCompactionTransition(&latest, fact) != nil {
		return zero, ErrConflict
	}
	plan := state.Plan
	if prepared != nil {
		if plan != nil {
			return zero, ErrConflict
		}
		plan = prepared
	} else if plan == nil && fact.PlanDigest != "" {
		return zero, ErrConflict
	}
	if plan != nil {
		if err = validateContextCompactionPlanEvidence(ctx, tx, state.Start, *plan, requireCurrentReview); err != nil {
			return zero, err
		}
	}
	if prepared != nil {
		planBody, marshalErr := json.Marshal(prepared)
		if marshalErr != nil || len(planBody) > runtime.MaxContextCompactionPlanBytes {
			return zero, sessions.ErrContextCompactionLifecycle
		}
		c := prepared.Compaction
		if _, err = tx.ExecContext(ctx, `INSERT INTO context_compaction_plans
			(operation_id,plan_digest,request_id,request_digest,summary_attempt_id,summary_review_id,draft_digest,original_prefix_digest,replacement_prefix_digest,live_suffix_boundary,live_suffix_boundary_digest,before_tokens,after_tokens,body)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, prepared.OperationID, prepared.PlanDigest, prepared.RequestID, prepared.RequestDigest,
			c.SummaryAttemptID, c.SummaryReviewID, prepared.DraftDigest, prepared.OriginalPrefixDigest, prepared.ReplacementPrefixDigest,
			prepared.LiveSuffixBoundary, prepared.LiveSuffixBoundaryDigest, c.BeforeContextTokens, c.AfterContextTokens, planBody); err != nil {
			return zero, err
		}
	}
	if err = insertContextCompactionFact(ctx, tx, fact, factBody); err != nil {
		return zero, err
	}
	state, err = readContextCompactionPlanState(ctx, tx, fact.OperationID)
	if err != nil {
		return zero, err
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	return state, nil
}

func insertContextCompactionFact(ctx context.Context, tx *sql.Tx, fact sessions.ContextCompactionLifecycleFact, body []byte) error {
	var previous, planDigest, attemptID, reviewID, activationDigest, code any
	if fact.PreviousID != "" {
		previous = fact.PreviousID
	}
	if fact.PlanDigest != "" {
		planDigest, attemptID, reviewID = fact.PlanDigest, fact.SummaryAttemptID, fact.SummaryReviewID
	}
	if fact.Activation != nil {
		activationDigest = fact.Activation.ActivationDigest
	}
	if fact.Code != "" {
		code = fact.Code
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO context_compaction_plan_facts
		(fact_id,fact_digest,operation_id,sequence,previous_fact_id,kind,plan_digest,summary_attempt_id,summary_review_id,activation_digest,code,created_at,body)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, fact.ID, fact.Digest, fact.OperationID, fact.Sequence, previous, string(fact.Kind),
		planDigest, attemptID, reviewID, activationDigest, code, fact.CreatedAt.Format(time.RFC3339Nano), body)
	return err
}

func validateContextCompactionPlanEvidence(ctx context.Context, tx *sql.Tx, start sessions.ContextCompactionPlanStart, plan runtime.ContextCompactionPlan, requireCurrent bool) error {
	if plan.Validate() != nil || plan.OperationID != start.OperationID || plan.OperationDigest != start.OperationDigest ||
		plan.RequestID != start.RequestID || plan.RequestDigest != start.RequestDigest || plan.ConfigDigest != start.ConfigDigest ||
		plan.PolicyDigest != start.PolicyDigest || plan.Engine != start.Engine || !reflect.DeepEqual(plan.Tiers, start.Tiers) ||
		plan.Compaction.SourceTaskID != start.TaskID || plan.Compaction.SourceSequence != start.SourceSequence ||
		plan.Compaction.SourceDigest != start.SourceDigest || plan.Compaction.SummaryAttemptID != start.AttemptID {
		return sessions.ErrContextCompactionLifecycle
	}
	var attemptBody []byte
	if err := tx.QueryRowContext(ctx, "SELECT body FROM summary_attempts WHERE id=? AND task_id=?", start.AttemptID, start.TaskID).Scan(&attemptBody); err != nil {
		return err
	}
	attempt, err := decodeSummaryAttempt(attemptBody, start.AttemptID, start.TaskID)
	if err != nil || attempt.Status != "drafted" || attempt.Draft == nil || attempt.SourceSequence != start.SourceSequence ||
		attempt.SourceDigest != start.SourceDigest || attempt.Model != start.Model || attempt.Provider != start.Provider || attempt.Keep != start.Keep ||
		attempt.EstimatedCost != start.EstimatedCost || !attempt.StartedAt.Equal(start.StartedAt) {
		return sessions.ErrHistory
	}
	draftDigest, err := sessions.SummaryDraftDigest(*attempt.Draft)
	if err != nil || draftDigest != plan.DraftDigest {
		return sessions.ErrHistory
	}
	var reviewBody []byte
	if requireCurrent {
		err = tx.QueryRowContext(ctx, `SELECT r.body FROM summary_review_heads h JOIN summary_reviews r
			ON r.id=h.review_id AND r.attempt_id=h.attempt_id WHERE h.attempt_id=? AND h.review_id=?`, start.AttemptID, plan.Compaction.SummaryReviewID).Scan(&reviewBody)
	} else {
		err = tx.QueryRowContext(ctx, "SELECT body FROM summary_reviews WHERE id=? AND attempt_id=?", plan.Compaction.SummaryReviewID, start.AttemptID).Scan(&reviewBody)
	}
	if err != nil {
		return err
	}
	review, err := decodeSummaryReview(reviewBody, plan.Compaction.SummaryReviewID, start.AttemptID)
	if err != nil || review.Version != 2 || review.Decision != "approved" || review.SourceSequence != start.SourceSequence ||
		review.SourceDigest != start.SourceDigest || review.DraftDigest != draftDigest {
		return sessions.ErrHistory
	}
	checkpoint := *plan.Compaction
	checkpoint.SummaryAttemptID, checkpoint.SummaryReviewID = "", ""
	if !canonicalJSONEqual(&checkpoint, attempt.Draft.Checkpoint) {
		return sessions.ErrHistory
	}
	source, err := taskSnapshot(ctx, tx, start.TaskID)
	if err != nil || source.State != "completed" || source.Sequence != start.SourceSequence {
		return sessions.ErrHistory
	}
	replacement, derived, err := sessions.PrepareContinuation(source, attempt.Draft.Request)
	if err != nil || !reflect.DeepEqual(derived, attempt.Draft.Checkpoint) || len(plan.OriginalPrefix) < len(source.Messages) ||
		len(plan.ReplacementPrefix) < len(replacement) || !reflect.DeepEqual(plan.OriginalPrefix[:len(source.Messages)], source.Messages) ||
		!reflect.DeepEqual(plan.ReplacementPrefix[:len(replacement)], replacement) ||
		!reflect.DeepEqual(plan.OriginalPrefix[len(source.Messages):], plan.ReplacementPrefix[len(replacement):]) {
		return sessions.ErrHistory
	}
	return nil
}

func readContextCompactionPlanState(ctx context.Context, q contextCompactionQuery, operationID string) (sessions.ContextCompactionOperationState, error) {
	zero := sessions.ContextCompactionOperationState{}
	if operationID == "" || len(operationID) > 128 {
		return zero, sessions.ErrContextCompactionLifecycle
	}
	var indexed struct {
		operationDigest, requestID, requestDigest, taskID, sourceDigest, attemptID, model, provider string
		configDigest, policyDigest, engineDigest, tierDigest, processID, startedAt, status          string
		sourceSequence                                                                              int64
		keep                                                                                        int
		estimatedCost                                                                               float64
	}
	var startBody []byte
	err := q.QueryRowContext(ctx, `SELECT operation_digest,request_id,request_digest,task_id,source_sequence,source_digest,attempt_id,
		model,provider,keep,estimated_cost,config_digest,policy_digest,engine_digest,tier_digest,process_id,started_at,status,body
		FROM context_compaction_operations WHERE operation_id=?`, operationID).Scan(&indexed.operationDigest, &indexed.requestID,
		&indexed.requestDigest, &indexed.taskID, &indexed.sourceSequence, &indexed.sourceDigest, &indexed.attemptID, &indexed.model,
		&indexed.provider, &indexed.keep, &indexed.estimatedCost, &indexed.configDigest, &indexed.policyDigest, &indexed.engineDigest,
		&indexed.tierDigest, &indexed.processID, &indexed.startedAt, &indexed.status, &startBody)
	if err != nil {
		return zero, err
	}
	var start sessions.ContextCompactionPlanStart
	if json.Unmarshal(startBody, &start) != nil || start.Validate() != nil || start.OperationID != operationID ||
		start.OperationDigest != indexed.operationDigest || start.RequestID != indexed.requestID || start.RequestDigest != indexed.requestDigest ||
		start.TaskID != indexed.taskID || start.SourceSequence != indexed.sourceSequence || start.SourceDigest != indexed.sourceDigest ||
		start.AttemptID != indexed.attemptID || start.Model != indexed.model || start.Provider != indexed.provider || start.Keep != indexed.keep ||
		start.EstimatedCost != indexed.estimatedCost || start.ConfigDigest != indexed.configDigest || start.PolicyDigest != indexed.policyDigest ||
		start.Engine.Digest != indexed.engineDigest || start.Tiers.Digest != indexed.tierDigest || start.ProcessID != indexed.processID ||
		start.StartedAt.Format(time.RFC3339Nano) != indexed.startedAt || string(start.Status) != indexed.status {
		return zero, sessions.ErrContextCompactionLifecycle
	}
	state := sessions.ContextCompactionOperationState{Start: start}
	var attemptBody []byte
	if err = q.QueryRowContext(ctx, "SELECT body FROM summary_attempts WHERE id=? AND task_id=?", start.AttemptID, start.TaskID).Scan(&attemptBody); err != nil {
		return zero, err
	}
	attempt, err := decodeSummaryAttempt(attemptBody, start.AttemptID, start.TaskID)
	if err != nil || attempt.SourceSequence != start.SourceSequence || attempt.SourceDigest != start.SourceDigest ||
		attempt.Model != start.Model || attempt.Provider != start.Provider || attempt.Keep != start.Keep ||
		attempt.EstimatedCost != start.EstimatedCost || !attempt.StartedAt.Equal(start.StartedAt) {
		return zero, sessions.ErrContextCompactionLifecycle
	}
	var planBody []byte
	var planIndexed struct {
		digest, requestID, requestDigest, attemptID, reviewID, draftDigest, originalDigest, replacementDigest, boundaryDigest string
		boundary, beforeTokens, afterTokens                                                                                   int
	}
	err = q.QueryRowContext(ctx, `SELECT plan_digest,request_id,request_digest,summary_attempt_id,summary_review_id,draft_digest,
		original_prefix_digest,replacement_prefix_digest,live_suffix_boundary,live_suffix_boundary_digest,before_tokens,after_tokens,body
		FROM context_compaction_plans WHERE operation_id=?`, operationID).Scan(&planIndexed.digest, &planIndexed.requestID,
		&planIndexed.requestDigest, &planIndexed.attemptID, &planIndexed.reviewID, &planIndexed.draftDigest, &planIndexed.originalDigest,
		&planIndexed.replacementDigest, &planIndexed.boundary, &planIndexed.boundaryDigest, &planIndexed.beforeTokens, &planIndexed.afterTokens, &planBody)
	if err == nil {
		var plan runtime.ContextCompactionPlan
		if json.Unmarshal(planBody, &plan) != nil || plan.Validate() != nil || plan.OperationID != operationID || plan.PlanDigest != planIndexed.digest ||
			plan.RequestID != planIndexed.requestID || plan.RequestDigest != planIndexed.requestDigest || plan.Compaction == nil ||
			plan.Compaction.SummaryAttemptID != planIndexed.attemptID || plan.Compaction.SummaryReviewID != planIndexed.reviewID ||
			plan.DraftDigest != planIndexed.draftDigest || plan.OriginalPrefixDigest != planIndexed.originalDigest ||
			plan.ReplacementPrefixDigest != planIndexed.replacementDigest || plan.LiveSuffixBoundary != planIndexed.boundary ||
			plan.LiveSuffixBoundaryDigest != planIndexed.boundaryDigest || plan.Compaction.BeforeContextTokens != planIndexed.beforeTokens ||
			plan.Compaction.AfterContextTokens != planIndexed.afterTokens {
			return zero, sessions.ErrContextCompactionLifecycle
		}
		state.Plan = &plan
	} else if !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	rows, err := q.QueryContext(ctx, `SELECT fact_id,fact_digest,sequence,previous_fact_id,kind,plan_digest,summary_attempt_id,
		summary_review_id,activation_digest,code,created_at,body FROM context_compaction_plan_facts
		WHERE operation_id=? ORDER BY sequence LIMIT 8`, operationID)
	if err != nil {
		return zero, err
	}
	for rows.Next() {
		var id, digest, kind, createdAt string
		var sequence int
		var previous, planDigest, attemptID, reviewID, activationDigest, code sql.NullString
		var body []byte
		if err = rows.Scan(&id, &digest, &sequence, &previous, &kind, &planDigest, &attemptID, &reviewID, &activationDigest, &code, &createdAt, &body); err != nil {
			rows.Close()
			return zero, err
		}
		var fact sessions.ContextCompactionLifecycleFact
		if json.Unmarshal(body, &fact) != nil || fact.Validate() != nil || fact.ID != id || fact.Digest != digest || fact.OperationID != operationID ||
			fact.Sequence != sequence || compactionNullString(fact.PreviousID) != previous || string(fact.Kind) != kind || compactionNullString(fact.PlanDigest) != planDigest ||
			compactionNullString(fact.SummaryAttemptID) != attemptID || compactionNullString(fact.SummaryReviewID) != reviewID || compactionNullString(fact.Code) != code ||
			fact.CreatedAt.Format(time.RFC3339Nano) != createdAt || nullableActivationDigest(fact.Activation) != activationDigest {
			rows.Close()
			return zero, sessions.ErrContextCompactionLifecycle
		}
		state.Facts = append(state.Facts, fact)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return zero, err
	}
	if err = rows.Close(); err != nil {
		return zero, err
	}
	if len(state.Facts) == 0 || len(state.Facts) > 7 {
		return zero, sessions.ErrContextCompactionLifecycle
	}
	state.Status = state.Facts[len(state.Facts)-1].Kind
	if attempt.Status != "started" {
		state.TerminalAttempt = &attempt
	}
	// Recovery insertion is intentionally deferred until the schema binds the
	// recovering process rather than the dead original owner. Existing rows are
	// still decoded fail-closed for forward-compatible reads.
	var recoveryID, recoveryDigest, recoveryProcess, failedFactID, failedFactDigest, recoveryReason, recoveredAt string
	var recoveryBody []byte
	err = q.QueryRowContext(ctx, `SELECT recovery_id,recovery_digest,process_id,failed_fact_id,failed_fact_digest,reason,recovered_at,body
		FROM context_compaction_plan_recoveries WHERE operation_id=?`, operationID).Scan(&recoveryID, &recoveryDigest, &recoveryProcess,
		&failedFactID, &failedFactDigest, &recoveryReason, &recoveredAt, &recoveryBody)
	if err == nil {
		var recovery sessions.ContextCompactionRecovery
		if json.Unmarshal(recoveryBody, &recovery) != nil || recovery.Validate() != nil || recovery.OperationID != operationID ||
			recovery.ID != recoveryID || recovery.Digest != recoveryDigest || recovery.ProcessID != recoveryProcess ||
			recovery.FailedFactID != failedFactID || recovery.FailedFactDigest != failedFactDigest || recovery.Reason != recoveryReason ||
			recovery.RecoveredAt.Format(time.RFC3339Nano) != recoveredAt {
			return zero, sessions.ErrContextCompactionLifecycle
		}
		state.Recovery = &recovery
	} else if !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	return sessions.SealContextCompactionOperationState(state)
}

func compactionNullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

func nullableActivationDigest(value *sessions.ContextCompactionActivation) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return compactionNullString(value.ActivationDigest)
}
