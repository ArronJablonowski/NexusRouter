package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/processguard"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/workboard"
)

// ErrWorkboardRecoveryProof is intentionally nonspecific: recovery proof
// failures can contain private process and task metadata and must not become a
// transport oracle.
var ErrWorkboardRecoveryProof = errors.New("workboard recovery proof unavailable")

// WorkboardRecoveryTarget is a trusted supervisor input. It contains no proof
// assertions; every conclusion is re-derived from durable state and an OS lock
// probe. Browser and model payloads must not construct this value directly.
type WorkboardRecoveryTarget struct {
	BoardID, CardID, AttemptID, ClaimID string
}

// PrepareWorkboardRecovery derives the opaque intent consumed by the existing
// lifecycle and control services. Those services call this Store again as an
// independent verifier after their exact-replay check and before mutation.
func (s *Store) PrepareWorkboardRecovery(ctx context.Context, target WorkboardRecoveryTarget, actor workboard.Actor) (workboard.RecoveryIntent, error) {
	if s == nil || ctx == nil || ctx.Err() != nil || !validRecoveryTarget(target) || actor.Validate() != nil ||
		(actor.Type != "operator" && actor.Type != "system") {
		return workboard.RecoveryIntent{}, ErrWorkboardRecoveryProof
	}
	proof, err := s.observeWorkboardRecovery(ctx, target, actor)
	if err != nil {
		// A committed recovery/finalization remains replayable after restart
		// without probing the old process again. The domain service will still
		// require the exact original idempotency key and request digest.
		return s.readCommittedWorkboardRecoveryIntent(ctx, target)
	}
	return recoveryIntentFromProof(proof), nil
}

// VerifyRecovery implements workboard.RecoveryVerifier. The request's proof
// fields are comparison fences only; they never establish terminality, process
// death, or effect resolution.
func (s *Store) VerifyRecovery(ctx context.Context, request workboard.RecoverClaimRequest, actor workboard.Actor) (workboard.RecoveryProof, error) {
	proof, err := s.observeWorkboardRecovery(ctx, WorkboardRecoveryTarget{
		BoardID: request.BoardID, CardID: request.CardID, AttemptID: request.AttemptID, ClaimID: request.ClaimID,
	}, actor)
	if err != nil || recoveryIntentFromProof(proof) != request.Proof {
		return workboard.RecoveryProof{}, ErrWorkboardRecoveryProof
	}
	return proof, nil
}

// VerifyControlStop implements workboard.ControlVerifier with the same durable
// evidence path used for claim recovery.
func (s *Store) VerifyControlStop(ctx context.Context, request workboard.FinalizeCancelRequest, actor workboard.Actor) (workboard.RecoveryProof, error) {
	proof, err := s.observeWorkboardRecovery(ctx, WorkboardRecoveryTarget{
		BoardID: request.BoardID, CardID: request.CardID, AttemptID: request.AttemptID, ClaimID: request.ClaimID,
	}, actor)
	if err != nil || recoveryIntentFromProof(proof) != request.Proof {
		return workboard.RecoveryProof{}, ErrWorkboardRecoveryProof
	}
	return proof, nil
}

func recoveryIntentFromProof(proof workboard.RecoveryProof) workboard.RecoveryIntent {
	return workboard.RecoveryIntent{StopProofID: proof.StopProofID, TaskHeadDigest: proof.TaskHeadDigest,
		ProcessProofDigest: proof.ProcessProofDigest, EffectEvidenceDigest: proof.EffectEvidenceDigest,
		EffectResolution: proof.EffectResolution}
}

func (s *Store) observeWorkboardRecovery(ctx context.Context, target WorkboardRecoveryTarget, actor workboard.Actor) (workboard.RecoveryProof, error) {
	zero := workboard.RecoveryProof{}
	if s == nil || ctx == nil || ctx.Err() != nil || !validRecoveryTarget(target) || actor.Validate() != nil ||
		(actor.Type != "operator" && actor.Type != "system") {
		return zero, ErrWorkboardRecoveryProof
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, ErrWorkboardRecoveryProof
	}
	defer tx.Rollback()
	attempt, _, err := readCanonicalAttemptSnapshot(ctx, tx, target.BoardID, target.CardID, target.AttemptID)
	if err != nil || attempt.State != "running" || attempt.Claim == nil || attempt.Claim.ID != target.ClaimID ||
		(attempt.Claim.State != string(workboard.LeaseActive) && attempt.Claim.State != string(workboard.LeaseAttention)) ||
		attempt.Claim.TaskID == "" || attempt.WorkerID != attempt.Claim.OwnerID {
		return zero, ErrWorkboardRecoveryProof
	}
	var events []runtime.Event
	snapshot, err := taskSnapshotWithEvents(ctx, tx, attempt.Claim.TaskID, &events)
	if err != nil || snapshot.SessionID == "" || !containsRecoveryBinding(attempt.TaskIDs, snapshot.TaskID) ||
		!containsRecoveryBinding(attempt.SessionIDs, snapshot.SessionID) ||
		(snapshot.State != "completed" && snapshot.State != "failed" && snapshot.State != "canceled") ||
		len(snapshot.Pending) != 0 || snapshot.UncertainEffects || snapshot.InterruptedTurn {
		return zero, ErrWorkboardRecoveryProof
	}
	taskDigest, effectDigest, err := canonicalRecoveryTaskEvidence(ctx, tx, snapshot, events)
	if err != nil {
		return zero, ErrWorkboardRecoveryProof
	}
	processDigest, observations, err := observeRecoveryProcesses(ctx, tx, snapshot.TaskID, attempt.WorkerID)
	if err != nil {
		return zero, ErrWorkboardRecoveryProof
	}
	defer closeRecoveryObservations(observations)
	for _, observation := range observations {
		if observation.ConfirmUnlocked(ctx) != nil {
			return zero, ErrWorkboardRecoveryProof
		}
	}
	if err = tx.Commit(); err != nil {
		return zero, ErrWorkboardRecoveryProof
	}
	seed, _ := json.Marshal(struct {
		Target        WorkboardRecoveryTarget `json:"target"`
		TaskDigest    string                  `json:"task_digest"`
		ProcessDigest string                  `json:"process_digest"`
		EffectDigest  string                  `json:"effect_digest"`
	}{target, taskDigest, processDigest, effectDigest})
	id := sha256.Sum256(seed)
	return workboard.RecoveryProof{StopProofID: "wb-stop-" + hex.EncodeToString(id[:16]), TaskHeadDigest: taskDigest,
		ProcessProofDigest: processDigest, EffectEvidenceDigest: effectDigest, EffectResolution: workboard.EffectFree,
		TaskTerminal: true, ProcessStopped: true}, nil
}

func (s *Store) readCommittedWorkboardRecoveryIntent(ctx context.Context, target WorkboardRecoveryTarget) (workboard.RecoveryIntent, error) {
	if s == nil || ctx == nil || ctx.Err() != nil || !validRecoveryTarget(target) {
		return workboard.RecoveryIntent{}, ErrWorkboardRecoveryProof
	}
	var indexed storedRecoveryProof
	var body []byte
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT id,board_id,card_id,attempt_id,claim_id,task_head_digest,process_proof_digest,effect_evidence_digest,effect_resolution,created_at,body
		FROM workboard_recovery_proofs WHERE board_id=? AND card_id=? AND attempt_id=? AND claim_id=?`,
		target.BoardID, target.CardID, target.AttemptID, target.ClaimID).Scan(&indexed.ID, &indexed.BoardID, &indexed.CardID,
		&indexed.AttemptID, &indexed.ClaimID, &indexed.TaskHeadDigest, &indexed.ProcessProofDigest, &indexed.EffectEvidenceDigest,
		&indexed.EffectResolution, &created, &body)
	if err != nil {
		return workboard.RecoveryIntent{}, ErrWorkboardRecoveryProof
	}
	indexed.Version, indexed.CreatedAt = 1, time.Unix(0, created).UTC()
	var stored storedRecoveryProof
	if strictJSON(body, &stored) != nil || stored != indexed || stored.BoardID != target.BoardID || stored.CardID != target.CardID ||
		stored.AttemptID != target.AttemptID || stored.ClaimID != target.ClaimID || !validWorkboardID(stored.ID) ||
		stored.CreatedAt.Location() != time.UTC || stored.CreatedAt.Year() < 1970 || stored.CreatedAt.Year() >= 2261 || !validDigest(stored.TaskHeadDigest) ||
		!validDigest(stored.ProcessProofDigest) || !validDigest(stored.EffectEvidenceDigest) ||
		(stored.EffectResolution != string(workboard.EffectFree) && stored.EffectResolution != string(workboard.ResolvedNoReplay)) {
		return workboard.RecoveryIntent{}, ErrWorkboardRecoveryProof
	}
	return workboard.RecoveryIntent{StopProofID: stored.ID, TaskHeadDigest: stored.TaskHeadDigest,
		ProcessProofDigest: stored.ProcessProofDigest, EffectEvidenceDigest: stored.EffectEvidenceDigest,
		EffectResolution: workboard.EffectResolution(stored.EffectResolution)}, nil
}

func validRecoveryTarget(target WorkboardRecoveryTarget) bool {
	for _, value := range []string{target.BoardID, target.CardID, target.AttemptID, target.ClaimID} {
		if !validWorkboardID(value) {
			return false
		}
	}
	return true
}

func containsRecoveryBinding(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type recoveryEffectEvidence struct {
	Sequence     int64                `json:"sequence"`
	Kind         runtime.Kind         `json:"kind"`
	ToolCallID   string               `json:"tool_call_id"`
	ToolName     string               `json:"tool_name"`
	ToolBehavior runtime.ToolBehavior `json:"tool_behavior"`
	Effect       runtime.Effect       `json:"effect"`
}

func canonicalRecoveryTaskEvidence(ctx context.Context, tx *sql.Tx, snapshot sessions.Snapshot, events []runtime.Event) (string, string, error) {
	if len(events) == 0 || events[len(events)-1].Sequence != snapshot.Sequence {
		return "", "", ErrWorkboardRecoveryProof
	}
	eventDigests := make([]string, 0, len(events))
	effects := []recoveryEffectEvidence{}
	for _, event := range events {
		body, err := canonicalRawEvent(ctx, tx, event)
		if err != nil {
			return "", "", err
		}
		digest := sha256.Sum256(body)
		eventDigests = append(eventDigests, hex.EncodeToString(digest[:]))
		if event.Kind != runtime.ToolStarted && event.Kind != runtime.ToolCompleted {
			continue
		}
		if event.Kind == runtime.ToolCompleted && event.Data.Effect != runtime.NoEffect {
			// No durable operator resolution record exists yet. Confirmed and
			// uncertain effects therefore both fail closed instead of accepting
			// a caller's resolved_no_replay assertion.
			return "", "", ErrWorkboardRecoveryProof
		}
		effects = append(effects, recoveryEffectEvidence{Sequence: event.Sequence, Kind: event.Kind,
			ToolCallID: event.Data.ToolCallID, ToolName: event.Data.ToolName,
			ToolBehavior: event.Data.ToolBehavior, Effect: event.Data.Effect})
	}
	taskBody, err := json.Marshal(struct {
		TaskID, SessionID, State string
		Sequence                 int64
		Events                   []string
	}{snapshot.TaskID, snapshot.SessionID, snapshot.State, snapshot.Sequence, eventDigests})
	if err != nil {
		return "", "", ErrWorkboardRecoveryProof
	}
	effectBody, err := json.Marshal(struct {
		TaskID, SessionID string
		HeadSequence      int64
		Effects           []recoveryEffectEvidence
	}{snapshot.TaskID, snapshot.SessionID, snapshot.Sequence, effects})
	if err != nil {
		return "", "", ErrWorkboardRecoveryProof
	}
	taskDigest, effectDigest := sha256.Sum256(taskBody), sha256.Sum256(effectBody)
	return hex.EncodeToString(taskDigest[:]), hex.EncodeToString(effectDigest[:]), nil
}

func observeRecoveryProcesses(ctx context.Context, tx *sql.Tx, taskID, workerID string) (string, []*processguard.Observation, error) {
	rows, err := tx.QueryContext(ctx, `SELECT token FROM resource_leases WHERE task_id=? ORDER BY token LIMIT 1001`, taskID)
	if err != nil {
		return "", nil, err
	}
	tokens := []string{}
	for rows.Next() {
		var token string
		if rows.Scan(&token) != nil || len(tokens) >= 1000 {
			rows.Close()
			return "", nil, ErrWorkboardRecoveryProof
		}
		tokens = append(tokens, token)
	}
	if rows.Err() != nil || rows.Close() != nil || len(tokens) == 0 {
		return "", nil, ErrWorkboardRecoveryProof
	}
	leases := make([]recoveryLease, 0, len(tokens))
	references := map[string]processguard.Reference{}
	boundOwner := false
	for _, token := range tokens {
		lease, readErr := readRecoveryLease(ctx, tx, token)
		if readErr != nil || lease.Task != taskID || lease.Process == "" || lease.Reference.Validate() != nil {
			return "", nil, ErrWorkboardRecoveryProof
		}
		boundOwner = boundOwner || lease.Owner == workerID
		leases = append(leases, lease)
		references[lease.Process] = lease.Reference
	}
	if !boundOwner {
		return "", nil, ErrWorkboardRecoveryProof
	}
	body, err := json.Marshal(leases)
	if err != nil {
		return "", nil, ErrWorkboardRecoveryProof
	}
	digest := sha256.Sum256(body)
	ids := make([]string, 0, len(references))
	for id := range references {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	observations := make([]*processguard.Observation, 0, len(ids))
	for _, id := range ids {
		observation, probeErr := processguard.Probe(ctx, references[id])
		if probeErr != nil || observation == nil || observation.State != processguard.Unlocked {
			if observation != nil {
				_ = observation.Close()
			}
			closeRecoveryObservations(observations)
			return "", nil, ErrWorkboardRecoveryProof
		}
		observations = append(observations, observation)
	}
	return hex.EncodeToString(digest[:]), observations, nil
}

func closeRecoveryObservations(observations []*processguard.Observation) {
	for _, observation := range observations {
		_ = observation.Close()
	}
}

func canonicalRawEvent(ctx context.Context, tx *sql.Tx, event runtime.Event) ([]byte, error) {
	var body []byte
	if err := tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(body AS BLOB)) BETWEEN 1 AND 8388608 THEN body END FROM events WHERE task_id=? AND sequence=? AND id=?`,
		event.TaskID, event.Sequence, event.ID).Scan(&body); err != nil {
		return nil, ErrWorkboardRecoveryProof
	}
	canonical, err := event.Encode()
	if err != nil || !bytes.Equal(body, canonical) {
		return nil, ErrWorkboardRecoveryProof
	}
	return body, nil
}
