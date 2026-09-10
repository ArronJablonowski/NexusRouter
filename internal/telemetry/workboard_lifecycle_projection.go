package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

type storedLifecycleClaim struct {
	Version       int        `json:"version"`
	ID            string     `json:"id"`
	BoardID       string     `json:"board_id"`
	CardID        string     `json:"card_id"`
	AttemptID     string     `json:"attempt_id"`
	Revision      int64      `json:"revision"`
	State         string     `json:"state"`
	OwnerID       string     `json:"owner_id"`
	OwnerType     string     `json:"owner_type"`
	TaskID        string     `json:"task_id,omitempty"`
	ExpiresAt     time.Time  `json:"expires_at"`
	LastHeartbeat time.Time  `json:"last_heartbeat"`
	ReleasedAt    *time.Time `json:"released_at,omitempty"`
}

type storedLifecycleAttempt struct {
	Version          int                        `json:"version"`
	ID               string                     `json:"id"`
	BoardID          string                     `json:"board_id"`
	CardID           string                     `json:"card_id"`
	Ordinal          int                        `json:"ordinal"`
	Revision         int64                      `json:"revision"`
	State            string                     `json:"state"`
	WorkerID         string                     `json:"worker_id"`
	CriteriaRevision int64                      `json:"criteria_revision"`
	CriteriaDigest   string                     `json:"criteria_digest"`
	PolicyDigest     string                     `json:"policy_digest"`
	Budget           storedWorkboardBudget      `json:"budget"`
	Criteria         []storedWorkboardCriterion `json:"criteria"`
	TaskIDs          []string                   `json:"task_ids"`
	SessionIDs       []string                   `json:"session_ids"`
	Claim            storedLifecycleClaim       `json:"claim"`
	StartedAt        time.Time                  `json:"started_at"`
	EndedAt          *time.Time                 `json:"ended_at,omitempty"`
}

type storedClaimHeartbeat struct {
	Version    int       `json:"version"`
	ClaimID    string    `json:"claim_id"`
	Revision   int64     `json:"revision"`
	ObservedAt time.Time `json:"observed_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	ActorID    string    `json:"actor_id"`
}

func readLifecycleClaim(ctx context.Context, tx *sql.Tx, boardID, cardID, attemptID, claimID string) (workboard.Lease, storedLifecycleClaim, error) {
	var indexed storedLifecycleClaim
	var expiresAt, heartbeat int64
	var releasedAt sql.NullInt64
	var taskID sql.NullString
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT id,board_id,card_id,attempt_id,revision,state,owner_id,owner_type,task_id,expires_at,last_heartbeat,released_at,body
		FROM workboard_claims WHERE board_id=? AND card_id=? AND attempt_id=? AND id=?`, boardID, cardID, attemptID, claimID).
		Scan(&indexed.ID, &indexed.BoardID, &indexed.CardID, &indexed.AttemptID, &indexed.Revision, &indexed.State, &indexed.OwnerID,
			&indexed.OwnerType, &taskID, &expiresAt, &heartbeat, &releasedAt, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return workboard.Lease{}, storedLifecycleClaim{}, ErrWorkboardNotFound
	}
	if err != nil {
		return workboard.Lease{}, storedLifecycleClaim{}, err
	}
	indexed.Version, indexed.TaskID = 1, nullString(taskID)
	indexed.ExpiresAt, indexed.LastHeartbeat = time.Unix(0, expiresAt).UTC(), time.Unix(0, heartbeat).UTC()
	if releasedAt.Valid {
		value := time.Unix(0, releasedAt.Int64).UTC()
		indexed.ReleasedAt = &value
	}
	var canonical storedLifecycleClaim
	if strictJSON(body, &canonical) != nil || !equalStoredClaim(indexed, canonical) {
		return workboard.Lease{}, storedLifecycleClaim{}, ErrWorkboardCorrupt
	}
	lease := leaseFromStored(canonical)
	if workboard.ValidateLease(lease) != nil {
		return workboard.Lease{}, storedLifecycleClaim{}, ErrWorkboardCorrupt
	}
	return lease, canonical, nil
}

func equalStoredClaim(a, b storedLifecycleClaim) bool {
	if a.Version != b.Version || a.ID != b.ID || a.BoardID != b.BoardID || a.CardID != b.CardID || a.AttemptID != b.AttemptID ||
		a.Revision != b.Revision || a.State != b.State || a.OwnerID != b.OwnerID || a.OwnerType != b.OwnerType || a.TaskID != b.TaskID ||
		!a.ExpiresAt.Equal(b.ExpiresAt) || !a.LastHeartbeat.Equal(b.LastHeartbeat) || (a.ReleasedAt == nil) != (b.ReleasedAt == nil) {
		return false
	}
	return a.ReleasedAt == nil || a.ReleasedAt.Equal(*b.ReleasedAt)
}

func leaseFromStored(claim storedLifecycleClaim) workboard.Lease {
	return workboard.Lease{BoardID: claim.BoardID, CardID: claim.CardID, AttemptID: claim.AttemptID, ClaimID: claim.ID,
		Revision: claim.Revision, State: workboard.LeaseState(claim.State), OwnerID: claim.OwnerID,
		LastHeartbeat: claim.LastHeartbeat, ExpiresAt: claim.ExpiresAt, ReleasedAt: claim.ReleasedAt}
}

func updateStoredClaim(claim storedLifecycleClaim, lease workboard.Lease) storedLifecycleClaim {
	claim.Revision, claim.State = lease.Revision, string(lease.State)
	claim.LastHeartbeat, claim.ExpiresAt, claim.ReleasedAt = lease.LastHeartbeat, lease.ExpiresAt, lease.ReleasedAt
	return claim
}

func encodeLifecycle(value any, limit int) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(body) < 1 || len(body) > limit {
		return nil, &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "transaction_bytes"}
	}
	return body, nil
}

func updateAttemptClaim(ctx context.Context, tx *sql.Tx, mutation workboard.LifecycleMutation, before, after storedLifecycleClaim) (int, error) {
	var indexedRevision int64
	var indexedState, workerID, criteriaDigest, policyDigest string
	var endedAt sql.NullInt64
	var bodyBytes []byte
	err := tx.QueryRowContext(ctx, `SELECT revision,state,worker_id,criteria_digest,policy_digest,ended_at,body FROM workboard_attempts
		WHERE board_id=? AND card_id=? AND id=?`, mutation.BoardID, mutation.CardID, mutation.AttemptID).
		Scan(&indexedRevision, &indexedState, &workerID, &criteriaDigest, &policyDigest, &endedAt, &bodyBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrWorkboardNotFound
	}
	if err != nil {
		return 0, err
	}
	var attempt storedLifecycleAttempt
	if strictJSON(bodyBytes, &attempt) != nil || indexedRevision != attempt.Revision || indexedState != attempt.State || workerID != attempt.WorkerID ||
		criteriaDigest != attempt.CriteriaDigest || policyDigest != attempt.PolicyDigest || endedAt.Valid || attempt.ID != mutation.AttemptID ||
		attempt.BoardID != mutation.BoardID || attempt.CardID != mutation.CardID || attempt.State != "running" || !equalStoredClaim(attempt.Claim, before) {
		return 0, ErrWorkboardCorrupt
	}
	attempt.Revision++
	attempt.Claim = after
	nextBytes, err := encodeLifecycle(attempt, workboard.MaxTransactionBytes)
	if err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE workboard_attempts SET revision=?,body=? WHERE id=? AND revision=? AND state='running'`,
		attempt.Revision, nextBytes, attempt.ID, attempt.Revision-1)
	if err != nil {
		return 0, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return 0, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "attempt_revision"}
	}
	return len(bodyBytes) + len(nextBytes), nil
}
