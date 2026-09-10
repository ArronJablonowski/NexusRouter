package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func TestWorkboardCriteriaAndCheckpointPersistenceReplayRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, now)
	clock := card.UpdatedAt.Add(time.Second)
	criteria := []workboard.AcceptanceCriterion{{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic", ValidatorID: "go-test", Description: "Race tests pass.", Required: true},
		{Version: 1, ID: "approval", Kind: "subjective", RequiredSource: "user_feedback", ValidatorID: "operator", Description: "Operator approves.", Required: true}}
	operatorService := newTestProgressService(t, store, workboard.Actor{ID: "operator", Type: "operator"}, &clock)
	criteriaRequest := workboard.ReviseCriteriaRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "criteria-revise-01",
		ExpectedCardRevision: card.Revision, ExpectedCriteriaRevision: card.CriteriaRevision, Criteria: criteria}
	revised, err := operatorService.ReviseCriteria(ctx, criteriaRequest)
	if err != nil || revised.BoardRevision != 4 || *revised.CardRevision != 3 || revised.ClaimRevision != nil {
		t.Fatalf("criteria receipt=%+v err=%v", revised, err)
	}
	stored, err := store.GetCard(ctx, boardID, card.ID)
	if err != nil || stored.CriteriaRevision != 2 || !reflect.DeepEqual(stored.Criteria, criteria) {
		t.Fatalf("criteria card=%+v err=%v", stored, err)
	}
	worker := workboard.Actor{ID: "worker-a", Type: "worker"}
	verifier := verifiedLifecycleRecovery("progress-proof", workboard.EffectFree)
	workerLifecycle := newTestLifecycleService(t, store, worker, verifier, &clock)
	clock = clock.Add(time.Second)
	if _, err = workerLifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "progress-claim-01", ExpectedCardRevision: 3}); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	clock = clock.Add(time.Second)
	workerProgress := newTestProgressService(t, store, worker, &clock)
	checkpointRequest := workboard.AppendCheckpointRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "checkpoint-append1", ExpectedCardRevision: 4, ExpectedClaimRevision: 1, CriteriaRevision: 2, Evidence: "go test -race ./... passed"}
	checkpoint, err := workerProgress.AppendCheckpoint(ctx, checkpointRequest)
	if err != nil || checkpoint.BoardRevision != 6 || *checkpoint.CardRevision != 4 || *checkpoint.ClaimRevision != 1 {
		t.Fatalf("checkpoint receipt=%+v err=%v", checkpoint, err)
	}
	assertCheckpointProjection(t, store, boardID, card.ID, attemptID, claimID, 1, 1, criteria)
	clock = clock.Add(time.Second)
	if _, err = workerLifecycle.Heartbeat(ctx, workboard.HeartbeatRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "progress-heartbeat", ExpectedClaimRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock = clock.Add(time.Hour)
	operatorService = newTestProgressService(t, store, workboard.Actor{ID: "operator", Type: "operator"}, &clock)
	criteriaReplay, err := operatorService.ReviseCriteria(ctx, criteriaRequest)
	if err != nil || !reflect.DeepEqual(criteriaReplay, revised) {
		t.Fatalf("criteria replay=%+v err=%v", criteriaReplay, err)
	}
	workerProgress = newTestProgressService(t, store, worker, &clock)
	checkpointReplay, err := workerProgress.AppendCheckpoint(ctx, checkpointRequest)
	if err != nil || !reflect.DeepEqual(checkpointReplay, checkpoint) {
		t.Fatalf("checkpoint replay=%+v err=%v", checkpointReplay, err)
	}
	changed := checkpointRequest
	changed.Evidence = "different evidence"
	if _, err = workerProgress.AppendCheckpoint(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay error=%v", err)
	}
}

func TestWorkboardCheckpointAuthorityFencesAndRollback(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	card, boardID := readyLifecycleCard(t, ctx, store, time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	clock := card.UpdatedAt.Add(time.Second)
	worker := workboard.Actor{ID: "worker-a", Type: "worker"}
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("proof-a", workboard.EffectFree), &clock)
	if _, err = lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "checkpoint-claim01", ExpectedCardRevision: card.Revision}); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	clock = clock.Add(time.Second)
	request := workboard.AppendCheckpointRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "checkpoint-rollback", ExpectedCardRevision: card.Revision + 1, ExpectedClaimRevision: 1,
		CriteriaRevision: card.CriteriaRevision, Evidence: "durable evidence"}
	other := newTestProgressService(t, store, workboard.Actor{ID: "worker-b", Type: "worker"}, &clock)
	if _, err = other.AppendCheckpoint(ctx, request); !errors.Is(err, &workboard.Violation{Code: workboard.CodeLeaseOwner}) {
		t.Fatalf("wrong owner error=%v", err)
	}
	operatorAuthority := newTestProgressService(t, store, workboard.Actor{ID: "operator", Type: "operator"}, &clock)
	if _, err = operatorAuthority.AppendCheckpoint(ctx, request); err == nil {
		t.Fatal("operator appended worker checkpoint")
	}
	service := newTestProgressService(t, store, worker, &clock)
	stale := request
	stale.ExpectedClaimRevision = 2
	stale.IdempotencyKey = "checkpoint-stale-01"
	if _, err = service.AppendCheckpoint(ctx, stale); !errors.Is(err, &workboard.Violation{Code: workboard.CodeStaleRevision}) {
		t.Fatalf("stale checkpoint error=%v", err)
	}
	if _, err = store.db.Exec(`CREATE TRIGGER fail_checkpoint_event BEFORE INSERT ON workboard_events WHEN NEW.kind='checkpoint.append' BEGIN SELECT RAISE(ABORT,'checkpoint event failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = service.AppendCheckpoint(ctx, request); err == nil || !strings.Contains(err.Error(), "checkpoint event failure") {
		t.Fatalf("checkpoint rollback error=%v", err)
	}
	var checkpoints int
	if err = store.db.QueryRow(`SELECT count(*) FROM workboard_checkpoints`).Scan(&checkpoints); err != nil || checkpoints != 0 {
		t.Fatalf("checkpoints=%d err=%v", checkpoints, err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER fail_checkpoint_event`); err != nil {
		t.Fatal(err)
	}
	if _, err = service.AppendCheckpoint(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err = operatorAuthority.ReviseCriteria(ctx, workboard.ReviseCriteriaRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "active-criteria-01",
		ExpectedCardRevision: card.Revision + 1, ExpectedCriteriaRevision: card.CriteriaRevision, Criteria: workboardTestCriteria()}); !errors.Is(err, &workboard.Violation{Code: workboard.CodeIllegalTransition}) {
		t.Fatalf("active criteria error=%v", err)
	}
}

func newTestProgressService(t *testing.T, store *Store, actor workboard.Actor, clock *time.Time) *workboard.ProgressService {
	t.Helper()
	service, err := workboard.NewProgressService(store, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session", Actor: actor}}, func() time.Time { return *clock })
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func assertCheckpointProjection(t *testing.T, store *Store, boardID, cardID, attemptID, claimID string, revision, claimRevision int64, criteria []workboard.AcceptanceCriterion) {
	t.Helper()
	var gotRevision, gotClaimRevision, gotCriteriaRevision int64
	var gotBoard, gotCard, gotAttempt, gotClaim, gotCriteriaDigest, evidence, actor string
	if err := store.db.QueryRow(`SELECT board_id,card_id,attempt_id,claim_id,revision,claim_revision,criteria_revision,criteria_digest,evidence,actor_id
		FROM workboard_checkpoints WHERE board_id=? AND card_id=?`, boardID, cardID).Scan(&gotBoard, &gotCard, &gotAttempt, &gotClaim,
		&gotRevision, &gotClaimRevision, &gotCriteriaRevision, &gotCriteriaDigest, &evidence, &actor); err != nil {
		t.Fatal(err)
	}
	if gotBoard != boardID || gotCard != cardID || gotAttempt != attemptID || gotClaim != claimID || gotRevision != revision ||
		gotClaimRevision != claimRevision || gotCriteriaRevision != 2 || gotCriteriaDigest != criteriaDigest(criteria) || evidence == "" || actor != "worker-a" {
		t.Fatalf("checkpoint projection mismatch: %s %s %s %s %d %d %d %s %s", gotBoard, gotCard, gotAttempt, gotClaim, gotRevision, gotClaimRevision, gotCriteriaRevision, gotCriteriaDigest, actor)
	}
}
