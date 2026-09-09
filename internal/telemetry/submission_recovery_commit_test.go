package telemetry

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestInterruptedRecoveryCommitReturnsExactOwnedEventsOnce(t *testing.T) {
	t.Run("model", func(t *testing.T) {
		db, _ := submissionStore(t)
		job := queuedSubmission(t, db, "commit-model")
		claim := claimSubmission(t, db)
		interruptedModelFixture(t, db, claim, "")
		commit, err := db.RecoverInterruptedModelCommit(context.Background(), job.ID, submitDigest("config"), time.Now().Add(2*time.Minute))
		if err != nil || !commit.Changed || len(commit.Events) != 1 || commit.Events[0].Kind != runtime.TaskFailed {
			t.Fatal(commit, err)
		}
		page, err := db.ReadEventPage(context.Background(), "model-task", 3, 10)
		if err != nil || !reflect.DeepEqual(commit.Events, page.Events) {
			t.Fatal("commit result differed from durable tail", commit.Events, page.Events, err)
		}
		commit.Events[0].Data.Code = "mutated"
		again, err := db.ReadEventPage(context.Background(), "model-task", 3, 10)
		if err != nil || again.Events[0].Data.Code == "mutated" {
			t.Fatal("caller mutation changed storage", again, err)
		}
		replay, err := db.RecoverInterruptedModelCommit(context.Background(), job.ID, submitDigest("config"), time.Now().Add(3*time.Minute))
		if err != nil || replay.Changed || len(replay.Events) != 0 {
			t.Fatal("replay returned delivery events", replay, err)
		}
	})

	t.Run("delegation", func(t *testing.T) {
		db, _ := submissionStore(t)
		job := queuedSubmission(t, db, "commit-delegation")
		claim := claimSubmission(t, db)
		interruptedTree(t, db, claim)
		commit, err := db.RecoverInterruptedDelegationCommit(context.Background(), job.ID, submitDigest("config"), time.Now().Add(2*time.Minute))
		if err != nil || !commit.Changed || len(commit.Events) != 2 || commit.Events[0].Kind != runtime.ToolCompleted || commit.Events[1].Kind != runtime.TaskFailed || commit.Events[1].Sequence != commit.Events[0].Sequence+1 {
			t.Fatal(commit, err)
		}
		page, err := db.ReadEventPage(context.Background(), "parent", 4, 10)
		if err != nil || !reflect.DeepEqual(commit.Events, page.Events) {
			t.Fatal("delegation commit result differed from durable tail", commit.Events, page.Events, err)
		}
	})
}

func TestInterruptedRecoveryCommitReturnsNoEventsWithoutCommit(t *testing.T) {
	db, _ := submissionStore(t)
	job := queuedSubmission(t, db, "live-model")
	claim := claimSubmission(t, db)
	interruptedModelFixture(t, db, claim, "")
	commit, err := db.RecoverInterruptedModelCommit(context.Background(), job.ID, submitDigest("config"), time.Now())
	if err != nil || commit.Changed || len(commit.Events) != 0 {
		t.Fatal(commit, err)
	}
}

func TestInterruptedDelegationScreeningFailsClosedBeforeCommit(t *testing.T) {
	db, _ := submissionStore(t)
	job := queuedSubmission(t, db, "screened-delegation")
	claim := claimSubmission(t, db)
	interruptedTree(t, db, claim)
	commit, err := db.RecoverInterruptedDelegationCommitScreened(context.Background(), job.ID, submitDigest("config"), time.Now().Add(2*time.Minute), []string{"answer"})
	if !errors.Is(err, ErrRecoveryRedaction) || commit.Changed || len(commit.Events) != 0 {
		t.Fatal(commit, err)
	}
	page, readErr := db.ReadEventPage(context.Background(), "parent", 0, 100)
	if readErr != nil || page.State != "running" || page.HeadSequence != 4 {
		t.Fatal("screening changed parent history", page, readErr)
	}
	history, historyErr := db.RecoveryHistory(context.Background(), job.ID)
	if historyErr != nil || len(history) != 0 {
		t.Fatal("screening wrote a receipt", history, historyErr)
	}
	commit, err = db.RecoverInterruptedDelegationCommitScreened(context.Background(), job.ID, submitDigest("config"), time.Now().Add(3*time.Minute), nil)
	if err != nil || !commit.Changed || len(commit.Events) != 2 {
		t.Fatal("safe later policy could not recover", commit, err)
	}
}

func TestInterruptedRecoveryScreeningCanonicalizesSecretSetBeforeBounds(t *testing.T) {
	t.Run("empty and duplicate references do not consume capacity", func(t *testing.T) {
		db, _ := submissionStore(t)
		job := queuedSubmission(t, db, "canonical-secret-set")
		claim := claimSubmission(t, db)
		interruptedModelFixture(t, db, claim, "")
		secrets := []string{"", ""}
		for i := 0; i < 64; i++ {
			secret := fmt.Sprintf("unmatched-secret-%02d", i)
			secrets = append(secrets, secret, secret, "")
		}
		commit, err := db.RecoverInterruptedModelCommitScreened(context.Background(), job.ID, submitDigest("config"), time.Now().Add(2*time.Minute), secrets)
		if err != nil || !commit.Changed || len(commit.Events) != 1 {
			t.Fatal(commit, err)
		}
	})

	t.Run("sixty five distinct references fail before writes", func(t *testing.T) {
		db, _ := submissionStore(t)
		job := queuedSubmission(t, db, "oversized-secret-set")
		claim := claimSubmission(t, db)
		interruptedModelFixture(t, db, claim, "")
		secrets := make([]string, 65)
		for i := range secrets {
			secrets[i] = fmt.Sprintf("distinct-secret-%02d", i)
		}
		commit, err := db.RecoverInterruptedModelCommitScreened(context.Background(), job.ID, submitDigest("config"), time.Now().Add(2*time.Minute), secrets)
		if !errors.Is(err, ErrRecoveryRedaction) || commit.Changed || len(commit.Events) != 0 {
			t.Fatal(commit, err)
		}
		page, readErr := db.ReadEventPage(context.Background(), "model-task", 0, 100)
		if readErr != nil || page.State != "running" || page.HeadSequence != 3 {
			t.Fatal("bounds failure changed event history", page, readErr)
		}
		history, historyErr := db.RecoveryHistory(context.Background(), job.ID)
		if historyErr != nil || len(history) != 0 {
			t.Fatal("bounds failure wrote a receipt", history, historyErr)
		}
	})
}
