package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/ArronJablonowski/NexusRouter/workboard"
)

type executionCharges struct {
	timeMS, tokens, costMicros int64
}

// insertExecutionAdmission admits one configured execution after the runtime
// event, claim, and cross-domain marker have been staged in this transaction.
// All failures therefore roll every staged fact back together.
func insertExecutionAdmission(ctx context.Context, tx *sql.Tx, command workboard.TaskStartClaim, receipt workboard.OperationReceipt, eventBody []byte) error {
	if command.Reservation == nil {
		return nil
	}
	if err := enforceExecutionCapacity(ctx, tx, command); err != nil {
		return err
	}
	marker, err := taskStartClaimRecord(ctx, tx, command, receipt, eventBody)
	if err != nil {
		return err
	}
	admissionID := newWorkboardID()
	if admissionID == "" {
		return errors.New("secure identifier generation failed")
	}
	record, err := executionAdmissionRecord(command, marker, admissionID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workboard_execution_admissions(admission_id,task_id,session_id,event_id,event_digest,board_id,card_id,
		attempt_id,claim_id,worker_id,operation_id,request_digest,card_revision,model_id,provider_id,config_id,policy_digest,time_limit_ms,
		token_limit,cost_micros,global_wip_limit,board_wip_limit,admitted_at,admission_digest,body)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, record.AdmissionID, record.TaskID, record.SessionID, record.EventID,
		record.EventDigest, record.BoardID, record.CardID, record.AttemptID, record.ClaimID, record.WorkerID, record.OperationID,
		record.RequestDigest, record.CardRevision, record.ModelID, record.ProviderID, record.ConfigID, record.PolicyDigest,
		record.TimeLimitMS, record.TokenLimit, record.CostMicros, record.GlobalWIPLimit, record.BoardWIPLimit, record.AdmittedAt.UnixNano(),
		record.AdmissionDigest, body)
	return err
}

func executionAdmissionRecord(command workboard.TaskStartClaim, marker workboard.TaskStartClaimRecord, admissionID string) (workboard.ExecutionAdmissionRecord, error) {
	reservation := command.Reservation
	if reservation == nil || reservation.Validate(command.Event) != nil || marker.Validate() != nil {
		return workboard.ExecutionAdmissionRecord{}, ErrWorkboardCorrupt
	}
	record := workboard.ExecutionAdmissionRecord{Version: 1, AdmissionID: admissionID, TaskID: marker.TaskID, SessionID: marker.SessionID,
		EventID: marker.EventID, EventDigest: marker.EventDigest, BoardID: marker.BoardID, CardID: marker.CardID,
		AttemptID: marker.AttemptID, ClaimID: marker.ClaimID, WorkerID: marker.WorkerID, OperationID: marker.OperationID,
		RequestDigest: marker.RequestDigest, CardRevision: marker.ExpectedCardRevision, ModelID: reservation.ModelID,
		ProviderID: reservation.ProviderID, ConfigID: reservation.ConfigID, PolicyDigest: marker.PolicyDigest,
		TimeLimitMS: reservation.TimeLimitMS, TokenLimit: reservation.TokenLimit, CostMicros: reservation.CostMicros,
		GlobalWIPLimit: reservation.GlobalWIPLimit, BoardWIPLimit: reservation.BoardWIPLimit, AdmittedAt: command.Event.Time.UTC()}
	var err error
	record.AdmissionDigest, err = record.CanonicalDigest()
	if err != nil || record.Validate() != nil {
		return workboard.ExecutionAdmissionRecord{}, ErrWorkboardCorrupt
	}
	return record, nil
}

// validateExecutionAdmissionReplay enforces exact presence as well as exact
// canonical content. This prevents a legacy command from replaying a budgeted
// task and prevents a budgeted command from blessing an older unbudgeted task.
func validateExecutionAdmissionReplay(ctx context.Context, tx *sql.Tx, command workboard.TaskStartClaim, receipt workboard.OperationReceipt, eventBody []byte) error {
	indexed, canonical, found, err := readExecutionAdmission(ctx, tx, command.Event.TaskID)
	if err != nil {
		return err
	}
	if command.Reservation == nil {
		if found {
			return ErrConflict
		}
		return nil
	}
	if !found {
		return ErrConflict
	}
	marker, err := taskStartClaimRecord(ctx, tx, command, receipt, eventBody)
	if err != nil {
		return err
	}
	want, err := executionAdmissionRecord(command, marker, indexed.AdmissionID)
	if err != nil {
		return err
	}
	if indexed != canonical {
		return ErrWorkboardCorrupt
	}
	if canonical != want {
		return ErrConflict
	}
	return nil
}

func readExecutionAdmission(ctx context.Context, tx *sql.Tx, taskID string) (workboard.ExecutionAdmissionRecord, workboard.ExecutionAdmissionRecord, bool, error) {
	var indexed workboard.ExecutionAdmissionRecord
	var admittedAt int64
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT admission_id,task_id,session_id,event_id,event_digest,board_id,card_id,attempt_id,claim_id,worker_id,
		operation_id,request_digest,card_revision,model_id,provider_id,config_id,policy_digest,time_limit_ms,token_limit,cost_micros,
		global_wip_limit,board_wip_limit,admitted_at,admission_digest,body FROM workboard_execution_admissions WHERE task_id=?`, taskID).
		Scan(&indexed.AdmissionID, &indexed.TaskID, &indexed.SessionID, &indexed.EventID, &indexed.EventDigest, &indexed.BoardID,
			&indexed.CardID, &indexed.AttemptID, &indexed.ClaimID, &indexed.WorkerID, &indexed.OperationID, &indexed.RequestDigest,
			&indexed.CardRevision, &indexed.ModelID, &indexed.ProviderID, &indexed.ConfigID, &indexed.PolicyDigest,
			&indexed.TimeLimitMS, &indexed.TokenLimit, &indexed.CostMicros, &indexed.GlobalWIPLimit, &indexed.BoardWIPLimit,
			&admittedAt, &indexed.AdmissionDigest, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return workboard.ExecutionAdmissionRecord{}, workboard.ExecutionAdmissionRecord{}, false, nil
	}
	if err != nil {
		return workboard.ExecutionAdmissionRecord{}, workboard.ExecutionAdmissionRecord{}, false, err
	}
	indexed.Version, indexed.AdmittedAt = 1, time.Unix(0, admittedAt).UTC()
	var canonical workboard.ExecutionAdmissionRecord
	if strictJSON(body, &canonical) != nil || indexed.Validate() != nil || canonical.Validate() != nil {
		return workboard.ExecutionAdmissionRecord{}, workboard.ExecutionAdmissionRecord{}, true, ErrWorkboardCorrupt
	}
	return indexed, canonical, true, nil
}

func enforceExecutionCapacity(ctx context.Context, tx *sql.Tx, command workboard.TaskStartClaim) error {
	reservation := *command.Reservation
	card, body, err := readStoredCard(ctx, tx, command.Claim.BoardID, command.Claim.CardID)
	if err != nil {
		return err
	}
	// applyLifecycleMutationTx has already moved this exact card forward by
	// one revision. Its canonical budget must remain the frozen attempt budget.
	budget := card.Budget
	if card.Revision != command.Claim.ExpectedCardRevision+1 || body.Budget != (storedWorkboardBudget{
		AttemptLimit: budget.AttemptLimit, TimeLimitMS: budget.TimeLimitMS, TokenLimit: budget.TokenLimit, CostMicros: budget.CostMicros,
	}) {
		return ErrWorkboardCorrupt
	}
	if err = validateRouteReservationCost(command, reservation.CostMicros); err != nil {
		return err
	}
	if err = validateReservationBound("time_budget", budget.TimeLimitMS, reservation.TimeLimitMS, true); err != nil {
		return err
	}
	if err = validateReservationBound("token_budget", budget.TokenLimit, reservation.TokenLimit, true); err != nil {
		return err
	}
	// A configured local model may have an exact zero-dollar route estimate;
	// zero is therefore a valid cost reservation even on a bounded card.
	if err = validateReservationBound("cost_budget", budget.CostMicros, reservation.CostMicros, false); err != nil {
		return err
	}
	accounts, err := validatedExecutionAccounts(ctx, tx, command.Claim.BoardID, command.Claim.CardID)
	if err != nil {
		return err
	}
	if err = enforceExecutionWIP(accounts, command.Claim.BoardID, reservation); err != nil {
		return err
	}
	settled, unresolved, err := executionCardCharges(accounts, command.Claim.BoardID, command.Claim.CardID)
	if err != nil {
		return err
	}
	if exceedsExecutionBudget(budget.TimeLimitMS, settled.timeMS, unresolved.timeMS, reservation.TimeLimitMS) {
		return &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "time_budget"}
	}
	if exceedsExecutionBudget(budget.TokenLimit, settled.tokens, unresolved.tokens, reservation.TokenLimit) {
		return &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "token_budget"}
	}
	if exceedsExecutionBudget(budget.CostMicros, settled.costMicros, unresolved.costMicros, reservation.CostMicros) {
		return &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "cost_budget"}
	}
	return nil
}

func validateReservationBound(field string, cardLimit, requested int64, positive bool) error {
	if cardLimit > 0 && (positive && requested == 0 || requested > cardLimit) {
		return &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: field}
	}
	return nil
}

func exceedsExecutionBudget(limit, settled, unresolved, requested int64) bool {
	return limit > 0 && (settled > limit || unresolved > limit-settled || requested > limit-settled-unresolved)
}

func enforceExecutionWIP(accounts []validatedExecutionAccount, boardID string, reservation workboard.ExecutionReservation) error {
	globalCount, globalMin := 0, reservation.GlobalWIPLimit
	boardCount, boardMin := 0, reservation.BoardWIPLimit
	for _, account := range accounts {
		if account.settlement != nil {
			continue
		}
		globalCount++
		if account.admission.GlobalWIPLimit < globalMin {
			globalMin = account.admission.GlobalWIPLimit
		}
		if account.admission.BoardID == boardID {
			boardCount++
			if account.admission.BoardWIPLimit < boardMin {
				boardMin = account.admission.BoardWIPLimit
			}
		}
	}
	if globalCount >= reservation.GlobalWIPLimit || globalCount >= globalMin {
		return &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "global_wip"}
	}
	if boardCount >= reservation.BoardWIPLimit || boardCount >= boardMin {
		return &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "board_wip"}
	}
	return nil
}

func executionCardCharges(accounts []validatedExecutionAccount, boardID, cardID string) (executionCharges, executionCharges, error) {
	var settled, unresolved executionCharges
	var err error
	for _, account := range accounts {
		if account.admission.BoardID != boardID || account.admission.CardID != cardID {
			continue
		}
		if account.settlement == nil {
			err = addExecutionCharge(&unresolved, account.admission.TimeLimitMS, account.admission.TokenLimit, account.admission.CostMicros)
		} else {
			err = addExecutionCharge(&settled, account.settlement.ChargedTimeMS, account.settlement.ChargedTokens, account.settlement.ChargedCostMicros)
		}
		if err != nil {
			return settled, unresolved, err
		}
	}
	return settled, unresolved, nil
}

func validateRouteReservationCost(command workboard.TaskStartClaim, reserved int64) error {
	want, err := routeCostMicros(command.Event.Data.RouteEstimatedCost)
	if err != nil || want != reserved {
		return &workboard.Violation{Code: workboard.CodeInvalid, Field: "route_estimated_cost"}
	}
	return nil
}

func routeCostMicros(cost *float64) (int64, error) {
	if cost == nil {
		return 0, nil
	}
	scaled := *cost * 1_000_000
	if math.IsNaN(scaled) || math.IsInf(scaled, 0) || scaled < 0 || scaled > float64(workboard.MaxWorkCostMicros) {
		return 0, &workboard.Violation{Code: workboard.CodeInvalid, Field: "route_estimated_cost"}
	}
	// Cost is an upper-bound reservation. Round every positive fractional
	// micro-dollar upward so a low non-zero cloud estimate can never become a
	// free execution in integer accounting.
	return int64(math.Ceil(scaled)), nil
}
