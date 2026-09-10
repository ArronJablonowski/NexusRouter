package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

// TestWorkboardPauseLifecycleRestartAndHistoricalReplay proves both halves of
// the control contract: only the next legal phase can commit, while every
// exact committed command remains replayable after later phases and restart.
func TestWorkboardPauseLifecycleRestartAndHistoricalReplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	worker := workboard.Actor{ID: "pause-worker", Type: "worker"}
	operator := workboard.Actor{ID: "pause-operator", Type: "operator"}
	clock = card.UpdatedAt.Add(time.Second)
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("pause-unused-proof", workboard.EffectFree), &clock)
	claim, err := lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "pause-lifecycle-claim", ExpectedCardRevision: card.Revision})
	if err != nil || claim.CardRevision == nil || claim.ClaimRevision == nil {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	operatorControl := newTestControlService(t, store, operator, &controlVerifier{}, &clock)
	workerControl := newTestControlService(t, store, worker, &controlVerifier{}, &clock)

	assertPauseViolation(t, workboard.CodeIllegalTransition, func() error {
		_, callErr := operatorControl.RequestResume(ctx, workboard.RequestCardControl{BoardID: boardID, CardID: card.ID,
			IdempotencyKey: "resume-before-pause", ExpectedCardRevision: *claim.CardRevision})
		return callErr
	})
	assertPauseViolation(t, workboard.CodeIllegalTransition, func() error {
		_, callErr := workerControl.AcknowledgePause(ctx, workboard.ClaimPauseControl{BoardID: boardID, CardID: card.ID,
			AttemptID: attemptID, ClaimID: claimID, IdempotencyKey: "ack-before-request", ExpectedCardRevision: *claim.CardRevision,
			ExpectedClaimRevision: *claim.ClaimRevision})
		return callErr
	})

	clock = clock.Add(time.Second)
	pauseRequest := workboard.RequestCardControl{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "pause-request-phase", ExpectedCardRevision: *claim.CardRevision}
	paused, err := operatorControl.RequestPause(ctx, pauseRequest)
	if err != nil || paused.CardRevision == nil || paused.ClaimRevision != nil {
		t.Fatalf("pause=%+v err=%v", paused, err)
	}
	assertPauseCard(t, store, boardID, card.ID, workboard.PauseRequested)
	assertPauseViolation(t, workboard.CodeInvalid, func() error {
		_, callErr := operatorControl.RequestPause(ctx, workboard.RequestCardControl{BoardID: boardID, CardID: card.ID,
			IdempotencyKey: "duplicate-pause-request", ExpectedCardRevision: *paused.CardRevision})
		return callErr
	})

	clock = clock.Add(time.Second)
	pauseAck := workboard.ClaimPauseControl{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "pause-acknowledgement", ExpectedCardRevision: *paused.CardRevision, ExpectedClaimRevision: *claim.ClaimRevision}
	acknowledged, err := workerControl.AcknowledgePause(ctx, pauseAck)
	if err != nil || acknowledged.CardRevision == nil || acknowledged.ClaimRevision == nil || *acknowledged.ClaimRevision != *claim.ClaimRevision {
		t.Fatalf("ack=%+v err=%v", acknowledged, err)
	}
	assertPauseCard(t, store, boardID, card.ID, workboard.PauseAcknowledged)
	assertPauseViolation(t, workboard.CodeIllegalTransition, func() error {
		_, callErr := workerControl.AcknowledgePause(ctx, workboard.ClaimPauseControl{BoardID: boardID, CardID: card.ID,
			AttemptID: attemptID, ClaimID: claimID, IdempotencyKey: "duplicate-pause-ack", ExpectedCardRevision: *acknowledged.CardRevision,
			ExpectedClaimRevision: *claim.ClaimRevision})
		return callErr
	})

	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	assertPauseCard(t, store, boardID, card.ID, workboard.PauseAcknowledged)
	operatorControl = newTestControlService(t, store, operator, &controlVerifier{}, &clock)
	workerControl = newTestControlService(t, store, worker, &controlVerifier{}, &clock)

	clock = clock.Add(time.Second)
	resumeRequest := workboard.RequestCardControl{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "resume-request-phase", ExpectedCardRevision: *acknowledged.CardRevision}
	resuming, err := operatorControl.RequestResume(ctx, resumeRequest)
	if err != nil || resuming.CardRevision == nil || resuming.ClaimRevision != nil {
		t.Fatalf("resume=%+v err=%v", resuming, err)
	}
	assertPauseCard(t, store, boardID, card.ID, workboard.ResumeRequested)
	assertPauseViolation(t, workboard.CodeIllegalTransition, func() error {
		_, callErr := workerControl.AcknowledgePause(ctx, workboard.ClaimPauseControl{BoardID: boardID, CardID: card.ID,
			AttemptID: attemptID, ClaimID: claimID, IdempotencyKey: "pause-ack-during-resume", ExpectedCardRevision: *resuming.CardRevision,
			ExpectedClaimRevision: *claim.ClaimRevision})
		return callErr
	})

	clock = clock.Add(time.Second)
	resumeAck := workboard.ClaimPauseControl{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "resume-acknowledgement", ExpectedCardRevision: *resuming.CardRevision, ExpectedClaimRevision: *claim.ClaimRevision}
	resumed, err := workerControl.AcknowledgeResume(ctx, resumeAck)
	if err != nil || resumed.CardRevision == nil || resumed.ClaimRevision == nil {
		t.Fatalf("resume ack=%+v err=%v", resumed, err)
	}
	assertPauseCard(t, store, boardID, card.ID, workboard.PauseNone)
	assertPauseViolation(t, workboard.CodeIllegalTransition, func() error {
		_, callErr := workerControl.AcknowledgeResume(ctx, workboard.ClaimPauseControl{BoardID: boardID, CardID: card.ID,
			AttemptID: attemptID, ClaimID: claimID, IdempotencyKey: "duplicate-resume-ack", ExpectedCardRevision: *resumed.CardRevision,
			ExpectedClaimRevision: *claim.ClaimRevision})
		return callErr
	})

	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	operatorControl = newTestControlService(t, store, operator, &controlVerifier{}, &clock)
	workerControl = newTestControlService(t, store, worker, &controlVerifier{}, &clock)
	for name, test := range map[string]struct {
		want workboard.OperationReceipt
		call func() (workboard.OperationReceipt, error)
	}{
		"pause request":  {paused, func() (workboard.OperationReceipt, error) { return operatorControl.RequestPause(ctx, pauseRequest) }},
		"pause ack":      {acknowledged, func() (workboard.OperationReceipt, error) { return workerControl.AcknowledgePause(ctx, pauseAck) }},
		"resume request": {resuming, func() (workboard.OperationReceipt, error) { return operatorControl.RequestResume(ctx, resumeRequest) }},
		"resume ack":     {resumed, func() (workboard.OperationReceipt, error) { return workerControl.AcknowledgeResume(ctx, resumeAck) }},
	} {
		got, replayErr := test.call()
		if replayErr != nil || !reflect.DeepEqual(got, test.want) {
			t.Fatalf("%s replay=%+v want=%+v err=%v", name, got, test.want, replayErr)
		}
	}
	assertPauseCard(t, store, boardID, card.ID, workboard.PauseNone)
}

func TestWorkboardPauseTerminalFailureClearsPhase(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	worker := workboard.Actor{ID: "terminal-pause-worker", Type: "worker"}
	clock = card.UpdatedAt.Add(time.Second)
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("terminal-unused-proof", workboard.EffectFree), &clock)
	claimed, err := lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "terminal-pause-claim", ExpectedCardRevision: card.Revision})
	if err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	operator := newTestControlService(t, store, workboard.Actor{ID: "terminal-pause-operator", Type: "operator"}, &controlVerifier{}, &clock)
	clock = clock.Add(time.Second)
	paused, err := operator.RequestPause(ctx, workboard.RequestCardControl{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "terminal-pause-request", ExpectedCardRevision: *claimed.CardRevision})
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	failed, err := lifecycle.Fail(ctx, workboard.FailClaimRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "terminal-pause-failure", ExpectedCardRevision: *paused.CardRevision,
		ExpectedClaimRevision: *claimed.ClaimRevision, EffectResolution: workboard.EffectFree})
	if err != nil || failed.CardRevision == nil {
		t.Fatalf("failure=%+v err=%v", failed, err)
	}
	current, err := store.GetCard(ctx, boardID, card.ID)
	if err != nil || current.State != workboard.Ready || current.PauseRequested || current.PausePhase != workboard.PauseNone {
		t.Fatalf("terminal card=%+v err=%v", current, err)
	}
	assertStoredPauseJSON(t, store, boardID, card.ID, false, "")
}

func TestWorkboardCancelPreemptsAcknowledgedPauseAndClearsPhase(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	worker := workboard.Actor{ID: "cancel-paused-worker", Type: "worker"}
	clock = card.UpdatedAt.Add(time.Second)
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("cancel-paused-unused", workboard.EffectFree), &clock)
	claimed, err := lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "cancel-paused-claim", ExpectedCardRevision: card.Revision})
	if err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	proof := workboard.RecoveryProof{StopProofID: "cancel-paused-proof", TaskHeadDigest: strings.Repeat("1", 64),
		ProcessProofDigest: strings.Repeat("2", 64), EffectEvidenceDigest: strings.Repeat("3", 64),
		EffectResolution: workboard.ResolvedNoReplay, TaskTerminal: true, ProcessStopped: true}
	verifier := &controlVerifier{proof: proof}
	operator := newTestControlService(t, store, workboard.Actor{ID: "cancel-paused-operator", Type: "operator"}, verifier, &clock)
	workerControl := newTestControlService(t, store, worker, verifier, &clock)
	clock = clock.Add(time.Second)
	paused, err := operator.RequestPause(ctx, workboard.RequestCardControl{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "cancel-paused-request", ExpectedCardRevision: *claimed.CardRevision})
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	acknowledged, err := workerControl.AcknowledgePause(ctx, workboard.ClaimPauseControl{BoardID: boardID, CardID: card.ID,
		AttemptID: attemptID, ClaimID: claimID, IdempotencyKey: "cancel-paused-acknowledge", ExpectedCardRevision: *paused.CardRevision,
		ExpectedClaimRevision: *claimed.ClaimRevision})
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	cancelRequested, err := operator.RequestCancel(ctx, workboard.RequestCardControl{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "cancel-paused-request-stop", ExpectedCardRevision: *acknowledged.CardRevision})
	if err != nil {
		t.Fatal(err)
	}
	assertPauseViolation(t, workboard.CodeIllegalTransition, func() error {
		_, callErr := operator.RequestResume(ctx, workboard.RequestCardControl{BoardID: boardID, CardID: card.ID,
			IdempotencyKey: "resume-after-cancel", ExpectedCardRevision: *cancelRequested.CardRevision})
		return callErr
	})
	clock = clock.Add(time.Second)
	finalized, err := operator.FinalizeCancel(ctx, workboard.FinalizeCancelRequest{BoardID: boardID, CardID: card.ID,
		AttemptID: attemptID, ClaimID: claimID, IdempotencyKey: "cancel-paused-finalize", ExpectedCardRevision: *cancelRequested.CardRevision,
		ExpectedClaimRevision: *claimed.ClaimRevision, Proof: recoveryIntent(proof)})
	if err != nil || finalized.CardRevision == nil || finalized.ClaimRevision == nil {
		t.Fatalf("finalize=%+v err=%v", finalized, err)
	}
	current, err := store.GetCard(ctx, boardID, card.ID)
	if err != nil || current.State != workboard.Canceled || current.PauseRequested || current.PausePhase != workboard.PauseNone {
		t.Fatalf("canceled card=%+v err=%v", current, err)
	}
	assertStoredPauseJSON(t, store, boardID, card.ID, false, "")
}

func assertPauseViolation(t *testing.T, code workboard.ErrorCode, call func() error) {
	t.Helper()
	if err := call(); !errors.Is(err, &workboard.Violation{Code: code}) {
		t.Fatalf("violation=%v want=%s", err, code)
	}
}

func assertPauseCard(t *testing.T, store *Store, boardID, cardID string, phase workboard.PausePhase) {
	t.Helper()
	card, err := store.GetCard(context.Background(), boardID, cardID)
	if err != nil || card.PausePhase != phase || card.PauseRequested != (phase != workboard.PauseNone) {
		t.Fatalf("pause card=%+v phase=%q err=%v", card, phase, err)
	}
	assertStoredPauseJSON(t, store, boardID, cardID, phase != workboard.PauseNone, string(phase))
}

func assertStoredPauseJSON(t *testing.T, store *Store, boardID, cardID string, requested bool, phase string) {
	t.Helper()
	var indexed int
	var storedRequested int
	var storedPhase sql.NullString
	if err := store.db.QueryRow(`SELECT pause_requested,
		CAST(json_extract(body,'$.pause_requested') AS INTEGER),json_extract(body,'$.pause_phase')
		FROM workboard_cards WHERE board_id=? AND id=?`, boardID, cardID).Scan(&indexed, &storedRequested, &storedPhase); err != nil {
		t.Fatal(err)
	}
	want := 0
	if requested {
		want = 1
	}
	if indexed != want || storedRequested != want || storedPhase.Valid != (phase != "") || storedPhase.String != phase {
		t.Fatalf("pause storage index/body=%d/%d phase=%q valid=%t", indexed, storedRequested, storedPhase.String, storedPhase.Valid)
	}
}

func TestWorkboardPausePhaseMigrationLegacyAndFailClosedRollback(t *testing.T) {
	t.Run("legacy true becomes requested and survives restart", func(t *testing.T) {
		ctx := context.Background()
		path := filepath.Join(t.TempDir(), "state.db")
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		clock := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
		card, boardID := readyLifecycleCard(t, ctx, store, clock)
		worker := workboard.Actor{ID: "migration-pause-worker", Type: "worker"}
		clock = card.UpdatedAt.Add(time.Second)
		lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("migration-unused-proof", workboard.EffectFree), &clock)
		claimed, err := lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID,
			IdempotencyKey: "migration-pause-claim", ExpectedCardRevision: card.Revision})
		if err != nil {
			t.Fatal(err)
		}
		control := newTestControlService(t, store, workboard.Actor{ID: "migration-operator", Type: "operator"}, &controlVerifier{}, &clock)
		clock = clock.Add(time.Second)
		if _, err = control.RequestPause(ctx, workboard.RequestCardControl{BoardID: boardID, CardID: card.ID,
			IdempotencyKey: "migration-pause-request", ExpectedCardRevision: *claimed.CardRevision}); err != nil {
			t.Fatal(err)
		}
		downgradePausePhaseTo38(t, store.db)
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
		store, err = Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		assertPauseCard(t, store, boardID, card.ID, workboard.PauseRequested)
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
		store, err = Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		assertPauseCard(t, store, boardID, card.ID, workboard.PauseRequested)
	})

	for _, corruption := range []struct {
		name   string
		poison string
	}{
		{"missing legacy boolean", `UPDATE workboard_cards SET body=json_remove(body,'$.pause_requested') WHERE pause_requested=1`},
		{"boolean index mismatch", `UPDATE workboard_cards SET body=json_set(body,'$.pause_requested',false) WHERE pause_requested=1`},
		{"preexisting phase", `UPDATE workboard_cards SET body=json_set(body,'$.pause_phase','acknowledged') WHERE pause_requested=1`},
	} {
		corruption := corruption
		t.Run(corruption.name, func(t *testing.T) {
			ctx := context.Background()
			path, store, boardID, cardID := legacyPausedDatabase(t, ctx)
			if _, err := store.db.Exec(corruption.poison); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if reopened, err := Open(ctx, path); err == nil {
				reopened.Close()
				t.Fatal("corrupt schema-38 pause state migrated")
			}
			assertRawMigrationState(t, path, boardID, cardID, 38, corruption.name == "preexisting phase")
		})
	}

	t.Run("write failure rolls back phase backfill and version", func(t *testing.T) {
		ctx := context.Background()
		path, store, boardID, cardID := legacyPausedDatabase(t, ctx)
		if _, err := store.db.Exec(`CREATE TRIGGER reject_pause_phase_migration BEFORE UPDATE OF body ON workboard_cards
			WHEN NEW.pause_requested=1 BEGIN SELECT RAISE(ABORT,'injected pause migration failure'); END`); err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		if reopened, err := Open(ctx, path); err == nil {
			reopened.Close()
			t.Fatal("injected migration failure accepted")
		} else if !strings.Contains(err.Error(), "injected pause migration failure") {
			t.Fatalf("migration error=%v", err)
		}
		assertRawMigrationState(t, path, boardID, cardID, 38, false)
	})
}

func legacyPausedDatabase(t *testing.T, ctx context.Context) (string, *Store, string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	worker := workboard.Actor{ID: "legacy-pause-worker", Type: "worker"}
	clock = card.UpdatedAt.Add(time.Second)
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("legacy-unused-proof", workboard.EffectFree), &clock)
	claimed, err := lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "legacy-pause-claim", ExpectedCardRevision: card.Revision})
	if err != nil {
		t.Fatal(err)
	}
	control := newTestControlService(t, store, workboard.Actor{ID: "legacy-pause-operator", Type: "operator"}, &controlVerifier{}, &clock)
	clock = clock.Add(time.Second)
	if _, err = control.RequestPause(ctx, workboard.RequestCardControl{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "legacy-pause-request", ExpectedCardRevision: *claimed.CardRevision}); err != nil {
		t.Fatal(err)
	}
	downgradePausePhaseTo38(t, store.db)
	return path, store, boardID, card.ID
}

func downgradePausePhaseTo38(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE workboard_cards SET body=json_remove(body,'$.pause_phase')`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`PRAGMA user_version=38`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func assertRawMigrationState(t *testing.T, path, boardID, cardID string, version int, phasePresent bool) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var gotVersion, phaseTypeCount int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&gotVersion); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM workboard_cards WHERE board_id=? AND id=? AND json_type(body,'$.pause_phase') IS NOT NULL`,
		boardID, cardID).Scan(&phaseTypeCount); err != nil {
		t.Fatal(err)
	}
	wantPhase := 0
	if phasePresent {
		wantPhase = 1
	}
	if gotVersion != version || phaseTypeCount != wantPhase {
		t.Fatalf("version=%d phase-present=%d want=%d/%d", gotVersion, phaseTypeCount, version, wantPhase)
	}
}
