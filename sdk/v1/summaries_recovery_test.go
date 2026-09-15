package v1_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func sdkSummaryRecoveryFixture(t *testing.T) (*sdk.Client, sessions.SummaryRecovery) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	database := filepath.Join(dir, "summary-recovery.db")
	db, err := telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(200, 0).UTC()
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted} {
		event := runtime.Event{Version: 1, ID: fmt.Sprintf("event-%d", i+1), TaskID: "source", SessionID: "session", CorrelationID: "source", Sequence: int64(i + 1), Time: now, Kind: kind}
		if kind == runtime.TaskStarted {
			event.Data.Messages = []providers.Message{{Role: "user", Content: "source requirement"}}
		}
		if kind == runtime.TurnStarted || kind == runtime.TurnCompleted {
			event.TurnID, event.AttemptID = "turn", "attempt"
		}
		if kind == runtime.TurnCompleted {
			event.Data.Text = "source result"
		}
		if err = db.Append(ctx, int64(i), event); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	source, err := sessions.Replay(ctx, db, "source")
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	request := sessions.CompactionRequest{Keep: 1, Summary: sessions.Summary{Decisions: []string{"preserve requirement"}}}
	_, checkpoint, err := sessions.PrepareContinuation(source, request)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	attempt := sessions.SummaryAttempt{Version: 1, ID: "summary-attempt", TaskID: "source", SourceDigest: checkpoint.SourceDigest, Model: "model", Provider: "provider", Status: "started", SourceSequence: source.Sequence, Keep: 1, StartedAt: now}
	if err = db.BeginSummary(ctx, attempt); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	attempt.Status, attempt.Code, attempt.FinishedAt = "interrupted", "owner_interrupted", now.Add(time.Second)
	recovery := sessions.SummaryRecovery{Version: 1, ID: strings.Repeat("b", 64), AttemptID: attempt.ID, TaskID: attempt.TaskID, SourceDigest: attempt.SourceDigest, SourceSequence: attempt.SourceSequence, State: "interrupted", Code: "owner_interrupted", RecoveredAt: now.Add(time.Second)}
	attemptBody, _ := json.Marshal(attempt)
	recoveryBody, _ := json.Marshal(recovery)
	raw, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.ExecContext(ctx, `UPDATE summary_attempts SET body=? WHERE id=?`, attemptBody, attempt.ID); err == nil {
		_, err = raw.ExecContext(ctx, `INSERT INTO summary_attempt_recoveries(id,attempt_id,task_id,recovered_at,body) VALUES(?,?,?,?,?)`, recovery.ID, recovery.AttemptID, recovery.TaskID, recovery.RecoveredAt.UnixNano(), recoveryBody)
	}
	closeErr := raw.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	configPath := filepath.Join(dir, "config.yaml")
	configBody := fmt.Sprintf("version: 1\ntelemetry:\n  database: %q\n", database)
	if err = os.WriteFile(configPath, []byte(configBody), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := sdk.New(sdk.ConfigOptions{ProjectFile: configPath})
	if err != nil {
		t.Fatal(err)
	}
	return client, recovery
}

func TestSDKSummaryRecoveryInspectionAndExplicitReconciliation(t *testing.T) {
	client, want := sdkSummaryRecoveryFixture(t)
	ctx := context.Background()
	got, err := client.InspectSummaryRecovery(ctx, want.AttemptID)
	if err != nil || got != want {
		t.Fatal(got, err)
	}
	page, err := client.ListSummaryRecoveries(ctx, want.TaskID, "", 1)
	if err != nil || !reflect.DeepEqual(page, []sdk.SummaryRecovery{want}) {
		t.Fatal(page, err)
	}
	empty, err := client.ListSummaryRecoveries(ctx, want.TaskID, want.ID, 1)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatal(empty, err)
	}
	next, recovered, err := client.ReconcileInterruptedSummaries(ctx, "", 1)
	if err != nil || next != "" || recovered != 0 {
		t.Fatal(next, recovered, err)
	}
	if _, err = client.InspectSummaryRecovery(ctx, "missing"); !errors.Is(err, sdk.ErrInspection) {
		t.Fatal("missing receipt did not preserve inspection error", err)
	}
}

func TestSDKSummaryRecoveryArgumentsAndCancellation(t *testing.T) {
	client, _ := sdkSummaryRecoveryFixture(t)
	ctx := context.Background()
	for _, test := range []struct {
		after string
		limit int
	}{
		{after: "-1", limit: 1},
		{after: "01", limit: 1},
		{after: "not-a-cursor", limit: 1},
		{limit: 0},
		{limit: 101},
	} {
		if _, _, err := client.ReconcileInterruptedSummaries(ctx, test.after, test.limit); !errors.Is(err, sdk.ErrAdmission) {
			t.Fatal(test, err)
		}
	}
	if _, err := client.ListSummaryRecoveries(ctx, "", "", 0); !errors.Is(err, sdk.ErrAdmission) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := client.ReconcileInterruptedSummaries(canceled, "", 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := client.InspectSummaryRecovery(canceled, "attempt"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
