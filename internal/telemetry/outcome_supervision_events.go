package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

var (
	ErrOutcomeSupervisionJournal = errors.New("outcome supervision journal invalid")
	ErrOutcomeSupervisionCorrupt = errors.New("outcome supervision journal corrupt")
	outcomeSupervisionSkillID    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
)

type OutcomeSupervisionCode string

const (
	OutcomeSupervisionWaiting    OutcomeSupervisionCode = "waiting"
	OutcomeSupervisionReady      OutcomeSupervisionCode = "ready"
	OutcomeSupervisionNoAction   OutcomeSupervisionCode = "no_action"
	OutcomeSupervisionRolledBack OutcomeSupervisionCode = "rolled_back"
	OutcomeSupervisionError      OutcomeSupervisionCode = "error"
)

// OutcomeSupervisionEventRequest is the complete caller-controlled journal
// input. Its closed shape admits only structural identifiers and lifecycle
// codes; there is intentionally no text, prompt, output, or error-detail field.
type OutcomeSupervisionEventRequest struct {
	Version            int                    `json:"version"`
	OperationID        string                 `json:"operation_id"`
	CheckID            string                 `json:"check_id"`
	Code               OutcomeSupervisionCode `json:"code"`
	SkillScope         string                 `json:"skill_scope"`
	SkillName          string                 `json:"skill_name"`
	ActivationID       string                 `json:"activation_id"`
	ActivationRevision string                 `json:"activation_revision"`
	PolicyID           string                 `json:"policy_id"`
}

func (r OutcomeSupervisionEventRequest) Validate() error {
	if r.Version != 1 || !outcomeSupervisionDigest(r.OperationID) || !outcomeSupervisionVersion(r.CheckID) ||
		!validOutcomeSupervisionCode(r.Code) || !outcomeSupervisionSkillID.MatchString(r.SkillScope) ||
		!outcomeSupervisionSkillID.MatchString(r.SkillName) || !outcomeSupervisionVersion(r.ActivationID) ||
		!outcomeSupervisionDigest(r.ActivationRevision) || !outcomeSupervisionDigest(r.PolicyID) {
		return ErrOutcomeSupervisionJournal
	}
	return nil
}

// OutcomeSupervisionEvent is an immutable stored lifecycle observation. ID is
// the domain-separated digest of Request; RecordedAt is assigned only once by
// the store, so acknowledgement-loss retries return the original event.
type OutcomeSupervisionEvent struct {
	OutcomeSupervisionEventRequest
	ID         string    `json:"id"`
	Sequence   int64     `json:"sequence"`
	RecordedAt time.Time `json:"recorded_at"`
}

func (e OutcomeSupervisionEvent) Validate() error {
	if e.OutcomeSupervisionEventRequest.Validate() != nil || e.ID != outcomeSupervisionEventID(e.OutcomeSupervisionEventRequest) || e.Sequence < 1 || e.Sequence > 1_000_000_000 ||
		e.RecordedAt.Year() < 1970 || e.RecordedAt.Year() >= 2261 {
		return ErrOutcomeSupervisionJournal
	}
	_, offset := e.RecordedAt.Zone()
	if offset != 0 || e.RecordedAt != e.RecordedAt.UTC() {
		return ErrOutcomeSupervisionJournal
	}
	return nil
}

type OutcomeSupervisionEventPage struct {
	Version           int                       `json:"version"`
	OperationID       string                    `json:"operation_id"`
	HighWaterSequence int64                     `json:"high_water_sequence"`
	HasMore           bool                      `json:"has_more"`
	Items             []OutcomeSupervisionEvent `json:"items"`
}

// RecordOutcomeSupervisionEvent atomically reconciles or appends one event. The
// store assigns sequence under BEGIN IMMEDIATE, avoiding a read-high/write race
// across processes. An exact (operation, check, code) retry returns the original
// event and timestamp; reuse with changed structural bindings conflicts.
func (s *Store) RecordOutcomeSupervisionEvent(ctx context.Context, request OutcomeSupervisionEventRequest) (OutcomeSupervisionEvent, error) {
	if s == nil || s.db == nil || ctx == nil || request.Validate() != nil {
		return OutcomeSupervisionEvent{}, ErrOutcomeSupervisionJournal
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return OutcomeSupervisionEvent{}, err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return OutcomeSupervisionEvent{}, err
	}
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	if err = outcomeSupervisionSchemaAvailable(ctx, conn); err != nil {
		return OutcomeSupervisionEvent{}, err
	}
	count, high, lastCode, err := inspectOutcomeSupervisionJournal(ctx, conn, request.OperationID)
	if err != nil {
		return OutcomeSupervisionEvent{}, err
	}
	if count != high {
		return OutcomeSupervisionEvent{}, ErrOutcomeSupervisionCorrupt
	}
	id := outcomeSupervisionEventID(request)
	prior, found, err := readOutcomeSupervisionEventByCheckCode(ctx, conn, request.OperationID, request.CheckID, request.Code)
	if err != nil {
		return OutcomeSupervisionEvent{}, err
	}
	if found {
		if prior.ID != id || prior.OutcomeSupervisionEventRequest != request {
			return OutcomeSupervisionEvent{}, ErrConflict
		}
		if _, err = conn.ExecContext(ctx, `COMMIT`); err != nil {
			return OutcomeSupervisionEvent{}, err
		}
		return prior, nil
	}
	var sameID int
	if err = conn.QueryRowContext(ctx, `SELECT count(*) FROM outcome_supervision_events WHERE id=?`, id).Scan(&sameID); err != nil {
		return OutcomeSupervisionEvent{}, err
	}
	if sameID != 0 {
		return OutcomeSupervisionEvent{}, ErrConflict
	}
	if high >= 1_000_000_000 || outcomeSupervisionTerminal(lastCode) || lastCode == OutcomeSupervisionReady && request.Code == OutcomeSupervisionWaiting {
		return OutcomeSupervisionEvent{}, ErrConflict
	}
	event := OutcomeSupervisionEvent{OutcomeSupervisionEventRequest: request, ID: id, Sequence: high + 1, RecordedAt: time.Now().UTC()}
	body, err := json.Marshal(event)
	if err != nil || len(body) < 1 || len(body) > 4096 || event.Validate() != nil {
		return OutcomeSupervisionEvent{}, ErrOutcomeSupervisionJournal
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO outcome_supervision_events
		(id,operation_id,check_id,sequence,code,skill_scope,skill_name,activation_id,activation_revision,policy_id,recorded_at,body)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, event.ID, event.OperationID, event.CheckID, event.Sequence, event.Code, event.SkillScope, event.SkillName,
		event.ActivationID, event.ActivationRevision, event.PolicyID, event.RecordedAt.UnixNano(), body)
	if err != nil {
		return OutcomeSupervisionEvent{}, err
	}
	if _, err = conn.ExecContext(ctx, `COMMIT`); err != nil {
		return OutcomeSupervisionEvent{}, err
	}
	return event, nil
}

// OutcomeSupervisionEvents replays a stable high-water page and verifies the
// complete operation prefix before returning any event.
func (s *Store) OutcomeSupervisionEvents(ctx context.Context, operationID string, after int64, limit int) (OutcomeSupervisionEventPage, error) {
	if s == nil || s.db == nil || ctx == nil || !outcomeSupervisionDigest(operationID) || after < 0 || limit < 1 || limit > 1000 {
		return OutcomeSupervisionEventPage{}, ErrOutcomeSupervisionJournal
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return OutcomeSupervisionEventPage{}, err
	}
	defer tx.Rollback()
	if err = outcomeSupervisionSchemaAvailable(ctx, tx); err != nil {
		return OutcomeSupervisionEventPage{}, err
	}
	count, high, _, err := inspectOutcomeSupervisionJournal(ctx, tx, operationID)
	if err != nil {
		return OutcomeSupervisionEventPage{}, err
	}
	if count != high || after > high {
		return OutcomeSupervisionEventPage{}, ErrOutcomeSupervisionCorrupt
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,check_id,sequence,code,skill_scope,skill_name,activation_id,activation_revision,policy_id,recorded_at,body
		FROM outcome_supervision_events WHERE operation_id=? AND sequence>? AND sequence<=? ORDER BY sequence LIMIT ?`,
		operationID, after, high, limit+1)
	if err != nil {
		return OutcomeSupervisionEventPage{}, err
	}
	defer rows.Close()
	items := make([]OutcomeSupervisionEvent, 0, limit+1)
	for rows.Next() {
		event, scanErr := scanOutcomeSupervisionEvent(rows, operationID)
		if scanErr != nil {
			return OutcomeSupervisionEventPage{}, scanErr
		}
		if event.Sequence != after+int64(len(items))+1 || event.Sequence > high {
			return OutcomeSupervisionEventPage{}, ErrOutcomeSupervisionCorrupt
		}
		items = append(items, event)
	}
	if err = rows.Err(); err != nil {
		return OutcomeSupervisionEventPage{}, err
	}
	page := OutcomeSupervisionEventPage{Version: 1, OperationID: operationID, HighWaterSequence: high, Items: items}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.HasMore = true
	}
	if err = tx.Commit(); err != nil {
		return OutcomeSupervisionEventPage{}, err
	}
	return page, nil
}

type outcomeSupervisionQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func outcomeSupervisionSchemaAvailable(ctx context.Context, tx outcomeSupervisionQueryer) error {
	var schema int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&schema); err != nil {
		return err
	}
	if schema != currentStorageSchema {
		return ErrOutcomeSupervisionJournal
	}
	return nil
}

func inspectOutcomeSupervisionJournal(ctx context.Context, tx outcomeSupervisionQueryer, operationID string) (count, high int64, last OutcomeSupervisionCode, err error) {
	var low int64
	err = tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(min(sequence),0),COALESCE(max(sequence),0)
		FROM outcome_supervision_events WHERE operation_id=?`, operationID).Scan(&count, &low, &high)
	if err != nil || count == 0 {
		return count, high, "", err
	}
	if low != 1 || count != high {
		return count, high, "", ErrOutcomeSupervisionCorrupt
	}
	event, err := readOutcomeSupervisionEventBySequence(ctx, tx, operationID, high)
	if err != nil {
		return count, high, "", err
	}
	return count, high, event.Code, nil
}

func readOutcomeSupervisionEventByCheckCode(ctx context.Context, tx outcomeSupervisionQueryer, operationID, checkID string, code OutcomeSupervisionCode) (OutcomeSupervisionEvent, bool, error) {
	row := tx.QueryRowContext(ctx, `SELECT id,check_id,sequence,code,skill_scope,skill_name,activation_id,activation_revision,policy_id,recorded_at,body
		FROM outcome_supervision_events WHERE operation_id=? AND check_id=? AND code=?`, operationID, checkID, code)
	event, err := scanOutcomeSupervisionEvent(row, operationID)
	if errors.Is(err, sql.ErrNoRows) {
		return OutcomeSupervisionEvent{}, false, nil
	}
	return event, err == nil, err
}

func readOutcomeSupervisionEventBySequence(ctx context.Context, tx outcomeSupervisionQueryer, operationID string, sequence int64) (OutcomeSupervisionEvent, error) {
	row := tx.QueryRowContext(ctx, `SELECT id,check_id,sequence,code,skill_scope,skill_name,activation_id,activation_revision,policy_id,recorded_at,body
		FROM outcome_supervision_events WHERE operation_id=? AND sequence=?`, operationID, sequence)
	return scanOutcomeSupervisionEvent(row, operationID)
}

type outcomeSupervisionScanner interface{ Scan(...any) error }

func scanOutcomeSupervisionEvent(row outcomeSupervisionScanner, operationID string) (OutcomeSupervisionEvent, error) {
	var indexed OutcomeSupervisionEvent
	var code string
	var recorded int64
	var body []byte
	indexed.Version, indexed.OperationID = 1, operationID
	if err := row.Scan(&indexed.ID, &indexed.CheckID, &indexed.Sequence, &code, &indexed.SkillScope, &indexed.SkillName, &indexed.ActivationID,
		&indexed.ActivationRevision, &indexed.PolicyID, &recorded, &body); err != nil {
		return OutcomeSupervisionEvent{}, err
	}
	indexed.Code, indexed.RecordedAt = OutcomeSupervisionCode(code), time.Unix(0, recorded).UTC()
	var event OutcomeSupervisionEvent
	if len(body) < 1 || len(body) > 4096 || strictJSON(body, &event) != nil || event.Validate() != nil || event != indexed {
		return OutcomeSupervisionEvent{}, ErrOutcomeSupervisionCorrupt
	}
	canonical, err := json.Marshal(event)
	if err != nil || !bytes.Equal(canonical, body) {
		return OutcomeSupervisionEvent{}, ErrOutcomeSupervisionCorrupt
	}
	return event, nil
}

func outcomeSupervisionEventID(request OutcomeSupervisionEventRequest) string {
	body, err := json.Marshal(request)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(append([]byte("darwin-outcome-supervision-event-v1\x00"), body...))
	return hex.EncodeToString(sum[:])
}

func outcomeSupervisionDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func outcomeSupervisionVersion(value string) bool {
	if len(value) != 32 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func validOutcomeSupervisionCode(code OutcomeSupervisionCode) bool {
	return code == OutcomeSupervisionWaiting || code == OutcomeSupervisionReady || code == OutcomeSupervisionNoAction ||
		code == OutcomeSupervisionRolledBack || code == OutcomeSupervisionError
}

func outcomeSupervisionTerminal(code OutcomeSupervisionCode) bool {
	return code == OutcomeSupervisionNoAction || code == OutcomeSupervisionRolledBack
}
