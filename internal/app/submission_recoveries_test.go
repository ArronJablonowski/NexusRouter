package app

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
)

func TestSubmissionRecoveryHistoryNoStorageCreation(t *testing.T) {
	s := submissionService(t)
	if _, err := s.SubmissionRecoveries(context.Background(), "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.settings.Telemetry.Database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("history created storage", err)
	}
}

func TestSubmissionRecoveryHistoryAcrossServiceInstances(t *testing.T) {
	s := submissionService(t)
	ctx := context.Background()
	status, err := s.Submit(ctx, "recovery-history-key", Request{ModelID: "fixture", Prompt: "private prompt"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(ctx, s.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ClaimSubmission(ctx, status.ConfigDigest, time.Now(), time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	if recovered, err := db.RecoverUndispatched(ctx, status.ID, status.ConfigDigest, time.Now()); err != nil || !recovered {
		t.Fatal(recovered, err)
	}
	fresh, err := NewService(s.settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	history, err := fresh.SubmissionRecoveries(ctx, status.ID)
	if err != nil || len(history) != 1 || history[0].SubmissionID != status.ID || history[0].Action != "queued" {
		t.Fatal(history, err)
	}
}
