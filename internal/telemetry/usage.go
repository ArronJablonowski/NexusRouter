package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/accounting"
	"github.com/ArronJablonowski/DarwinRouter/classification"
	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// RecordUsage appends an immutable, evidence-bound operation accounting fact.
// Exact retries are acknowledgement-safe; an ID cannot be reused with changes.
func (s *Store) RecordUsage(ctx context.Context, r accounting.Record) error {
	if r.Validate() != nil {
		return accounting.ErrUsage
	}
	r.OccurredAt = r.OccurredAt.UTC()
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE task_heads SET sequence=sequence WHERE task_id=?", r.TaskID); err != nil {
		return err
	}
	return recordUsage(ctx, tx, r, body, true)
}

// appendUsage writes into a caller-owned lifecycle transaction. The durable
// evidence row must already reflect its terminal state in that transaction.
func appendUsage(ctx context.Context, tx *sql.Tx, r accounting.Record) error {
	if r.Validate() != nil {
		return accounting.ErrUsage
	}
	r.OccurredAt = r.OccurredAt.UTC()
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return recordUsage(ctx, tx, r, body, false)
}

func recordUsage(ctx context.Context, tx *sql.Tx, r accounting.Record, body []byte, commit bool) error {
	var previous []byte
	var task, session, operation, role, evidenceKind, evidenceID string
	err := tx.QueryRowContext(ctx, "SELECT task_id,session_id,operation_id,role,evidence_kind,evidence_id,body FROM usage_records WHERE id=?", r.ID).Scan(&task, &session, &operation, &role, &evidenceKind, &evidenceID, &previous)
	if err == nil {
		if string(previous) != string(body) || task != r.TaskID || session != r.SessionID || operation != r.OperationID || role != string(r.Role) || evidenceKind != string(r.EvidenceKind) || evidenceID != r.EvidenceID {
			return ErrConflict
		}
		history, historyErr := usageHistory(ctx, tx, r.ID)
		if historyErr != nil {
			return historyErr
		}
		// An acknowledgement replay is also an integrity read. Re-prove the
		// immutable base measurement, then prove that the current correction
		// still names the same source evidence without requiring a reconciled
		// measurement to equal the provider's original report.
		if !accounting.SameRecord(history.Base, r) || validateUsageEvidence(ctx, tx, history.Base, true) != nil || validateUsageEvidence(ctx, tx, history.Current, false) != nil {
			return accounting.ErrUsage
		}
		if commit {
			return tx.Commit()
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var collision bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM usage_corrections WHERE id=?)", r.ID).Scan(&collision); err != nil {
		return err
	}
	if collision {
		return accounting.ErrUsage
	}
	if err = validateUsageEvidence(ctx, tx, r, true); err != nil {
		return err
	}
	var logicalID string
	err = tx.QueryRowContext(ctx, "SELECT id FROM usage_records WHERE task_id=? AND role=? AND operation_id=?", r.TaskID, r.Role, r.OperationID).Scan(&logicalID)
	if err == nil {
		return ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO usage_records(id,task_id,session_id,operation_id,role,evidence_kind,evidence_id,body) VALUES(?,?,?,?,?,?,?,?)`, r.ID, r.TaskID, r.SessionID, r.OperationID, r.Role, r.EvidenceKind, r.EvidenceID, body); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO usage_heads(base_id,current_id) VALUES(?,?)", r.ID, r.ID); err != nil {
		return err
	}
	if commit {
		return tx.Commit()
	}
	return nil
}

func validateUsageEvidence(ctx context.Context, tx *sql.Tx, r accounting.Record, matchMeasurement bool) error {
	var session string
	if err := tx.QueryRowContext(ctx, "SELECT session_id FROM task_heads WHERE task_id=?", r.TaskID).Scan(&session); err != nil || session != r.SessionID {
		return accounting.ErrUsage
	}
	table := map[accounting.EvidenceKind]string{
		accounting.EventEvidence: "events", accounting.ReviewEvidence: "review_attempts",
		accounting.AuditEvidence: "audit_records", accounting.SummaryEvidence: "summary_attempts",
	}[r.EvidenceKind]
	if table == "" {
		return accounting.ErrUsage
	}
	var task string
	var body []byte
	if err := tx.QueryRowContext(ctx, "SELECT task_id,body FROM "+table+" WHERE id=?", r.EvidenceID).Scan(&task, &body); err != nil || task != r.TaskID || len(body) > 1<<20 {
		return accounting.ErrUsage
	}
	if r.Role == accounting.PrimaryExecution || r.Role == accounting.Fallback {
		return validateRoutedUsage(ctx, tx, r, body, matchMeasurement)
	}
	return validateAuxiliaryUsage(ctx, tx, r, body, matchMeasurement)
}

func validateAuxiliaryUsage(ctx context.Context, tx *sql.Tx, r accounting.Record, body []byte, matchMeasurement bool) error {
	if r.Role == accounting.Classifier {
		var event runtime.Event
		if r.EvidenceKind != accounting.EventEvidence || json.Unmarshal(body, &event) != nil || event.Validate() != nil ||
			event.ID != r.EvidenceID || event.TaskID != r.TaskID || event.SessionID != r.SessionID || event.Kind != runtime.TaskStarted ||
			event.Data.IntentClassification == nil || r.OperationID != event.Data.IntentClassification.AttemptID || r.RouteID != r.OperationID || r.CandidateAttemptID != "" {
			return accounting.ErrUsage
		}
		attempt, err := intentClassificationAttemptForEvent(ctx, tx, event)
		if err != nil || attempt.ID != r.OperationID || attempt.Provider != r.Provider || attempt.Model != r.Model ||
			(matchMeasurement && !matchesConfiguredEstimate(r, "intent_classification_attempt.estimated_cost", attempt.EstimatedCost, attempt.StartedAt)) {
			return accounting.ErrUsage
		}
		wantDisposition, wantUsage := accounting.Completed, attempt.Usage
		if attempt.Status == classification.AttemptFailed {
			wantDisposition = accounting.Failed
		} else if attempt.Status == classification.AttemptCanceled {
			wantDisposition, wantUsage = accounting.Canceled, nil
		}
		if r.Disposition != wantDisposition || (matchMeasurement && !accounting.SameUsage(r.Usage, wantUsage)) {
			return accounting.ErrUsage
		}
		return nil
	}
	switch r.EvidenceKind {
	case accounting.SummaryEvidence:
		var a sessions.SummaryAttempt
		if json.Unmarshal(body, &a) != nil || a.Validate() != nil || a.ID != r.EvidenceID || r.OperationID != a.ID || r.RouteID != a.ID || r.CandidateAttemptID != "" || r.Provider != a.Provider || r.Model != a.Model || (matchMeasurement && !matchesConfiguredEstimate(r, "summary_attempt.estimated_cost", a.EstimatedCost, a.StartedAt)) {
			return accounting.ErrUsage
		}
		var usage = r.Usage
		wantDisposition := accounting.Failed
		if a.Status == "drafted" {
			wantDisposition, usage = accounting.Completed, a.Draft.Usage
		} else if a.Code == "canceled" {
			wantDisposition = accounting.Canceled
			usage = nil
		} else {
			usage = a.Usage
		}
		if wantDisposition != r.Disposition || (matchMeasurement && !accounting.SameUsage(r.Usage, usage)) {
			return accounting.ErrUsage
		}
	case accounting.ReviewEvidence:
		var a evaluation.ReviewAttempt
		if json.Unmarshal(body, &a) != nil {
			return accounting.ErrUsage
		}
		wantRole := accounting.OptionalJudge
		if a.ReviewerID != "" {
			wantRole = accounting.OrchestratorAudit
		}
		if a.Validate() != nil || a.ID != r.EvidenceID || r.OperationID != a.ID || r.RouteID != a.ID || r.CandidateAttemptID != a.AttemptID || r.Provider != a.EvaluatorProvider || r.Model != a.EvaluatorModel || r.Role != wantRole || (matchMeasurement && (!accounting.SameUsage(r.Usage, a.Usage) || !matchesConfiguredEstimate(r, "review_attempt.estimated_cost", a.EstimatedCost, a.StartedAt))) || a.Status != "failed" {
			return accounting.ErrUsage
		}
		want := accounting.Failed
		if a.Code == "canceled" {
			want = accounting.Canceled
		}
		if r.Disposition != want {
			return accounting.ErrUsage
		}
	case accounting.AuditEvidence:
		var a evaluation.AuditRecord
		if json.Unmarshal(body, &a) != nil || a.Validate() != nil || a.ID != r.EvidenceID || r.AuditID != a.ID || r.CandidateAttemptID != a.AttemptID || r.Provider != a.EvaluatorProvider || r.Model != a.EvaluatorModel || r.Disposition != accounting.Completed || (matchMeasurement && !accounting.SameUsage(r.Usage, a.Usage)) {
			return accounting.ErrUsage
		}
		review, err := completedReviewForAudit(ctx, tx, r.TaskID, a.ID)
		if err != nil {
			return accounting.ErrUsage
		}
		wantRole := accounting.OptionalJudge
		if review.ReviewerID != "" {
			wantRole = accounting.OrchestratorAudit
		}
		if r.Role != wantRole || review.ID != r.OperationID || r.RouteID != review.ID || review.AttemptID != a.AttemptID || review.EvaluatorProvider != a.EvaluatorProvider || review.EvaluatorModel != a.EvaluatorModel || (matchMeasurement && !matchesConfiguredEstimate(r, "review_attempt.estimated_cost", review.EstimatedCost, review.StartedAt)) {
			return accounting.ErrUsage
		}
	default:
		// Never accept caller-asserted auxiliary spend without a supported,
		// independently validated lifecycle evidence type.
		return accounting.ErrUsage
	}
	return nil
}

func matchesConfiguredEstimate(r accounting.Record, source string, cost float64, at time.Time) bool {
	wantCost, wantPricing := configuredAuxiliaryEstimate(source, r.Provider, r.Model, cost, at)
	return r.NormalizedCost != nil && r.Pricing != nil && *r.NormalizedCost == *wantCost && *r.Pricing == *wantPricing
}

func validateRoutedUsage(ctx context.Context, tx *sql.Tx, r accounting.Record, terminalBody []byte, matchMeasurement bool) error {
	var terminal runtime.Event
	if json.Unmarshal(terminalBody, &terminal) != nil || terminal.Validate() != nil || terminal.ID != r.EvidenceID || terminal.TaskID != r.TaskID || terminal.SessionID != r.SessionID || r.OperationID != r.TaskID || r.CandidateAttemptID != "" {
		return accounting.ErrUsage
	}
	wantDisposition := map[runtime.Kind]accounting.Disposition{runtime.TaskCompleted: accounting.Completed, runtime.TaskFailed: accounting.Failed, runtime.TaskCanceled: accounting.Canceled}[terminal.Kind]
	if wantDisposition == "" || wantDisposition != r.Disposition {
		return accounting.ErrUsage
	}
	var startBody []byte
	if err := tx.QueryRowContext(ctx, "SELECT body FROM events WHERE task_id=? AND sequence=1", r.TaskID).Scan(&startBody); err != nil {
		return accounting.ErrUsage
	}
	var start runtime.Event
	if json.Unmarshal(startBody, &start) != nil || start.Validate() != nil || start.Kind != runtime.TaskStarted || start.Data.ProviderID != r.Provider || start.Data.ModelID != r.Model {
		return accounting.ErrUsage
	}
	wantRole := accounting.PrimaryExecution
	if start.Data.RetryOfTaskID != "" {
		if err := validateRetryChain(ctx, tx, r.TaskID, start.Data.RetryOfTaskID); err != nil {
			return err
		}
		wantRole = accounting.Fallback
	}
	if r.Role != wantRole {
		return accounting.ErrUsage
	}
	wantRoute := start.ID
	var routeBody []byte
	err := tx.QueryRowContext(ctx, "SELECT body FROM events WHERE task_id=? AND json_extract(body,'$.kind')='route.selected' ORDER BY sequence LIMIT 1", r.TaskID).Scan(&routeBody)
	if err == nil {
		var route runtime.Event
		if json.Unmarshal(routeBody, &route) != nil || route.Validate() != nil || route.Data.ProviderID != r.Provider || route.Data.ModelID != r.Model {
			return accounting.ErrUsage
		}
		wantRoute = route.RouteID
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if r.RouteID != wantRoute {
		return accounting.ErrUsage
	}
	usage, err := routedUsage(ctx, tx, r.TaskID, r.Provider, r.Model)
	if err != nil {
		return err
	}
	if matchMeasurement && !accounting.SameUsage(r.Usage, usage) {
		return accounting.ErrUsage
	}
	return nil
}

func (s *Store) CorrectUsage(ctx context.Context, c accounting.Correction) error {
	if c.Version != 1 {
		return accounting.ErrUsage
	}
	c.RecordedAt, c.Record.OccurredAt = c.RecordedAt.UTC(), c.Record.OccurredAt.UTC()
	body, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE task_heads SET sequence=sequence WHERE task_id=?", c.Record.TaskID); err != nil {
		return err
	}
	var old []byte
	err = tx.QueryRowContext(ctx, "SELECT body FROM usage_corrections WHERE id=?", c.ID).Scan(&old)
	if err == nil {
		if string(old) != string(body) {
			return ErrConflict
		}
		h, historyErr := usageHistory(ctx, tx, c.BaseID)
		if historyErr != nil {
			return historyErr
		}
		found := false
		for _, historical := range h.Corrections {
			if historical.ID == c.ID {
				found = true
				break
			}
		}
		if !found || validateUsageEvidence(ctx, tx, h.Base, true) != nil || validateUsageEvidence(ctx, tx, h.Current, false) != nil {
			return accounting.ErrUsage
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var baseCollision bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM usage_records WHERE id=?)", c.ID).Scan(&baseCollision); err != nil || baseCollision {
		if err != nil {
			return err
		}
		return ErrConflict
	}
	prior, err := currentUsageTx(ctx, tx, c.BaseID)
	if err != nil {
		return err
	}
	if prior.ID != c.Supersedes {
		return ErrConflict
	}
	if c.Validate(prior) != nil || validateUsageEvidence(ctx, tx, c.Record, false) != nil {
		return accounting.ErrUsage
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO usage_corrections(id,base_id,supersedes,task_id,session_id,role,evidence_kind,evidence_id,body) VALUES(?,?,?,?,?,?,?,?,?)`, c.ID, c.BaseID, c.Supersedes, c.Record.TaskID, c.Record.SessionID, c.Record.Role, c.Record.EvidenceKind, c.Record.EvidenceID, body); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE usage_heads SET current_id=? WHERE base_id=? AND current_id=?", c.ID, c.BaseID, c.Supersedes)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return ErrConflict
	}
	return tx.Commit()
}

func currentUsageTx(ctx context.Context, tx *sql.Tx, baseID string) (accounting.Record, error) {
	h, err := usageHistory(ctx, tx, baseID)
	if err != nil {
		return accounting.Record{}, err
	}
	return accounting.CloneRecord(h.Current), nil
}

func (s *Store) CurrentUsage(ctx context.Context, baseID string) (accounting.Record, error) {
	h, err := s.UsageHistory(ctx, baseID)
	return accounting.CloneRecord(h.Current), err
}

func (s *Store) UsageHistory(ctx context.Context, baseID string) (accounting.History, error) {
	return usageHistory(ctx, s.db, baseID)
}

type usageQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func usageHistory(ctx context.Context, q usageQueryer, baseID string) (accounting.History, error) {
	var baseBody []byte
	var baseTask, baseSession, baseOperation, baseRole, baseEvidenceKind, baseEvidenceID string
	if err := q.QueryRowContext(ctx, "SELECT task_id,session_id,operation_id,role,evidence_kind,evidence_id,body FROM usage_records WHERE id=?", baseID).Scan(&baseTask, &baseSession, &baseOperation, &baseRole, &baseEvidenceKind, &baseEvidenceID, &baseBody); err != nil {
		return accounting.History{}, err
	}
	var base accounting.Record
	if json.Unmarshal(baseBody, &base) != nil || base.Validate() != nil || base.ID != baseID || base.TaskID != baseTask || base.SessionID != baseSession || base.OperationID != baseOperation || string(base.Role) != baseRole || string(base.EvidenceKind) != baseEvidenceKind || base.EvidenceID != baseEvidenceID {
		return accounting.History{}, accounting.ErrUsage
	}
	rows, err := q.QueryContext(ctx, "SELECT id,base_id,supersedes,task_id,session_id,role,evidence_kind,evidence_id,body FROM usage_corrections WHERE base_id=? ORDER BY rowid LIMIT 101", baseID)
	if err != nil {
		return accounting.History{}, err
	}
	defer rows.Close()
	h := accounting.History{Version: 1, BaseID: baseID, Base: base, Corrections: []accounting.Correction{}}
	for rows.Next() {
		var body []byte
		var id, storedBase, supersedes, task, session, role, evidenceKind, evidenceID string
		var c accounting.Correction
		if rows.Scan(&id, &storedBase, &supersedes, &task, &session, &role, &evidenceKind, &evidenceID, &body) != nil || json.Unmarshal(body, &c) != nil || c.ID != id || c.BaseID != storedBase || c.Supersedes != supersedes || c.Record.TaskID != task || c.Record.SessionID != session || string(c.Record.Role) != role || string(c.Record.EvidenceKind) != evidenceKind || c.Record.EvidenceID != evidenceID {
			return accounting.History{}, accounting.ErrUsage
		}
		h.Corrections = append(h.Corrections, c)
	}
	if err = rows.Err(); err != nil {
		return accounting.History{}, err
	}
	if err = rows.Close(); err != nil {
		return accounting.History{}, err
	}
	if len(h.Corrections) == 0 {
		h.Current = accounting.CloneRecord(base)
	} else {
		h.Current = accounting.CloneRecord(h.Corrections[len(h.Corrections)-1].Record)
	}
	var head string
	if err := q.QueryRowContext(ctx, "SELECT current_id FROM usage_heads WHERE base_id=?", baseID).Scan(&head); err != nil || head != h.Current.ID {
		return accounting.History{}, accounting.ErrUsage
	}
	if h.Validate() != nil {
		return accounting.History{}, accounting.ErrUsage
	}
	return h, nil
}

func (s *Store) UsageTotals(ctx context.Context, scope accounting.Scope) (accounting.Totals, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return accounting.Totals{}, err
	}
	defer tx.Rollback()
	totals, err := usageTotals(ctx, tx, scope)
	if err != nil {
		return accounting.Totals{}, err
	}
	if err = tx.Commit(); err != nil {
		return accounting.Totals{}, err
	}
	return totals, nil
}

// usageTotals lets telemetry snapshots include accounting on their existing
// read transaction, preserving one WAL snapshot without re-entering Store.
func usageTotals(ctx context.Context, q usageQueryer, scope accounting.Scope) (accounting.Totals, error) {
	if scope.Validate() != nil {
		return accounting.Totals{}, accounting.ErrUsage
	}
	var schema int
	if err := q.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil || schema < 1 || schema > currentStorageSchema {
		return accounting.Totals{}, errUsageSchema
	}
	if scope.TaskID != "" {
		var authoritativeSession string
		if err := q.QueryRowContext(ctx, "SELECT session_id FROM task_heads WHERE task_id=?", scope.TaskID).Scan(&authoritativeSession); err != nil {
			return accounting.Totals{}, err
		}
		if scope.SessionID != "" && scope.SessionID != authoritativeSession {
			return accounting.Totals{}, sql.ErrNoRows
		}
		scope.SessionID = authoritativeSession
	}
	terminalQuery := `SELECT count(*) FROM task_heads h WHERE h.state IN('completed','failed','canceled') AND (?='' OR h.task_id=?) AND (?='' OR h.session_id=?)`
	var terminalOperations int64
	if err := q.QueryRowContext(ctx, terminalQuery, scope.TaskID, scope.TaskID, scope.SessionID, scope.SessionID).Scan(&terminalOperations); err != nil {
		return accounting.Totals{}, err
	}
	if schema < 30 {
		t := accounting.Totals{Version: 1, Scope: scope, Coverage: accounting.CompleteCoverage, CalculatedAt: time.Now().UTC()}
		if terminalOperations > 0 {
			t.Coverage, t.UnaccountedRoutedOperations = accounting.LegacyUnavailableCoverage, terminalOperations
		}
		if t.Validate() != nil {
			return accounting.Totals{}, accounting.ErrUsage
		}
		return t, nil
	}
	if err := validateUsageTopology(ctx, q, scope); err != nil {
		return accounting.Totals{}, err
	}
	records, err := currentUsageRecordsForScope(ctx, q, scope)
	if err != nil {
		return accounting.Totals{}, err
	}
	t := accounting.Totals{Version: 1, Scope: scope, Coverage: accounting.CompleteCoverage, CalculatedAt: time.Now().UTC()}
	for _, r := range records {
		if (scope.TaskID != "" && r.TaskID != scope.TaskID) || (scope.SessionID != "" && r.SessionID != scope.SessionID) {
			return accounting.Totals{}, accounting.ErrUsage
		}
		one, totalErr := accounting.TotalFor(r)
		if totalErr != nil {
			return accounting.Totals{}, totalErr
		}
		target := map[accounting.Role]*accounting.Total{accounting.PrimaryExecution: &t.Primary, accounting.Fallback: &t.Fallback, accounting.Classifier: &t.Classifier, accounting.Summarizer: &t.Summarizer, accounting.OrchestratorAudit: &t.OrchestratorAudit, accounting.OptionalJudge: &t.Judge}[r.Role]
		*target, err = accounting.Sum(*target, one)
		if err != nil {
			return accounting.Totals{}, err
		}
	}
	t.Routed, err = accounting.Sum(t.Primary, t.Fallback)
	if err == nil {
		t.Auxiliary, err = accounting.Sum(t.Classifier, t.Summarizer, t.OrchestratorAudit, t.Judge)
	}
	if err == nil {
		t.Overall, err = accounting.Sum(t.Routed, t.Auxiliary)
	}
	if err != nil {
		return accounting.Totals{}, err
	}
	terminalQuery += ` AND NOT EXISTS(SELECT 1 FROM usage_records r WHERE r.task_id=h.task_id AND r.role IN('primary_execution','fallback'))`
	if err = q.QueryRowContext(ctx, terminalQuery, scope.TaskID, scope.TaskID, scope.SessionID, scope.SessionID).Scan(&t.UnaccountedRoutedOperations); err != nil {
		return accounting.Totals{}, err
	}
	if t.UnaccountedRoutedOperations > 0 {
		t.Coverage = accounting.PartialCoverage
		if t.Overall.Records == 0 {
			t.Coverage = accounting.LegacyUnavailableCoverage
		}
	}
	if t.Validate() != nil {
		return accounting.Totals{}, accounting.ErrUsage
	}
	return t, nil
}

const (
	usageAggregateBaseLimit       = 1_000_000
	usageAggregateCorrectionLimit = 1_000_000
	usageBodyLimit                = 1 << 20
)

type usageAggregateState struct {
	id, task, session, operation, role, evidenceKind, evidenceID, head string
	body                                                               []byte
	current                                                            accounting.Record
	corrections                                                        int
}

func currentUsageRecordsForScope(ctx context.Context, q usageQueryer, scope accounting.Scope) ([]accounting.Record, error) {
	baseQuery := `SELECT r.id,r.task_id,r.session_id,r.operation_id,r.role,r.evidence_kind,r.evidence_id,r.body,h.current_id
		FROM usage_records r JOIN usage_heads h ON h.base_id=r.id
		WHERE (?='' OR r.task_id=?) AND (?='' OR r.session_id=?)
		ORDER BY r.id LIMIT ?`
	rows, err := q.QueryContext(ctx, baseQuery, scope.TaskID, scope.TaskID, scope.SessionID, scope.SessionID, usageAggregateBaseLimit+1)
	if err != nil {
		return nil, err
	}
	bases := make([]usageAggregateState, 0)
	for rows.Next() {
		var b usageAggregateState
		if err = rows.Scan(&b.id, &b.task, &b.session, &b.operation, &b.role, &b.evidenceKind, &b.evidenceID, &b.body, &b.head); err != nil {
			rows.Close()
			return nil, err
		}
		if len(b.body) > usageBodyLimit || len(bases) >= usageAggregateBaseLimit {
			rows.Close()
			return nil, accounting.ErrUsage
		}
		bases = append(bases, b)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}

	byBase := make(map[string]int, len(bases))
	for i := range bases {
		b := &bases[i]
		var base accounting.Record
		if json.Unmarshal(b.body, &base) != nil || base.Validate() != nil || base.ID != b.id || base.TaskID != b.task || base.SessionID != b.session || base.OperationID != b.operation || string(base.Role) != b.role || string(base.EvidenceKind) != b.evidenceKind || base.EvidenceID != b.evidenceID {
			return nil, accounting.ErrUsage
		}
		if _, duplicate := byBase[b.id]; duplicate {
			return nil, accounting.ErrUsage
		}
		byBase[b.id] = i
		b.current = accounting.CloneRecord(base)
		b.body = nil
	}

	correctionQuery := `SELECT c.id,c.base_id,c.supersedes,c.task_id,c.session_id,c.role,c.evidence_kind,c.evidence_id,c.body
		FROM usage_corrections c JOIN usage_records r ON r.id=c.base_id
		WHERE (?='' OR r.task_id=?) AND (?='' OR r.session_id=?)
		ORDER BY c.base_id,c.rowid LIMIT ?`
	rows, err = q.QueryContext(ctx, correctionQuery, scope.TaskID, scope.TaskID, scope.SessionID, scope.SessionID, usageAggregateCorrectionLimit+1)
	if err != nil {
		return nil, err
	}
	corrections := 0
	for rows.Next() {
		var body []byte
		var id, baseID, supersedes, task, session, role, evidenceKind, evidenceID string
		var c accounting.Correction
		if err = rows.Scan(&id, &baseID, &supersedes, &task, &session, &role, &evidenceKind, &evidenceID, &body); err != nil {
			rows.Close()
			return nil, err
		}
		corrections++
		index, ok := byBase[baseID]
		if corrections > usageAggregateCorrectionLimit || !ok || len(body) > usageBodyLimit || json.Unmarshal(body, &c) != nil || c.ID != id || c.BaseID != baseID || c.Supersedes != supersedes || c.Record.TaskID != task || c.Record.SessionID != session || string(c.Record.Role) != role || string(c.Record.EvidenceKind) != evidenceKind || c.Record.EvidenceID != evidenceID {
			rows.Close()
			return nil, accounting.ErrUsage
		}
		state := &bases[index]
		state.corrections++
		if state.corrections > 100 || c.Validate(state.current) != nil {
			rows.Close()
			return nil, accounting.ErrUsage
		}
		state.current = accounting.CloneRecord(c.Record)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}

	current := make([]accounting.Record, len(bases))
	for i := range bases {
		if bases[i].head != bases[i].current.ID {
			return nil, accounting.ErrUsage
		}
		current[i] = accounting.CloneRecord(bases[i].current)
	}
	return current, nil
}

func validateUsageTopology(ctx context.Context, q usageQueryer, scope accounting.Scope) error {
	checks := []struct {
		query string
		args  []any
	}{
		{`SELECT EXISTS(SELECT 1 FROM usage_heads h LEFT JOIN usage_records r ON r.id=h.base_id WHERE r.id IS NULL)`, nil},
		{`SELECT EXISTS(SELECT 1 FROM usage_corrections c LEFT JOIN usage_records r ON r.id=c.base_id WHERE r.id IS NULL)`, nil},
		{`SELECT EXISTS(SELECT 1 FROM usage_records r LEFT JOIN usage_heads h ON h.base_id=r.id WHERE (?='' OR r.task_id=?) AND (?='' OR r.session_id=?) GROUP BY r.id HAVING count(h.base_id)<>1)`, []any{scope.TaskID, scope.TaskID, scope.SessionID, scope.SessionID}},
		{`SELECT EXISTS(SELECT 1 FROM usage_heads h JOIN usage_records r ON r.id=h.base_id LEFT JOIN usage_corrections c ON c.id=h.current_id AND c.base_id=h.base_id WHERE (?='' OR r.task_id=?) AND (?='' OR r.session_id=?) AND h.current_id<>h.base_id AND c.id IS NULL)`, []any{scope.TaskID, scope.TaskID, scope.SessionID, scope.SessionID}},
	}
	for _, check := range checks {
		var invalid bool
		if err := q.QueryRowContext(ctx, check.query, check.args...).Scan(&invalid); err != nil || invalid {
			return accounting.ErrUsage
		}
	}
	return nil
}
