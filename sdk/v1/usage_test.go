package v1_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
)

func TestSDKTaskUsageReadOnlyRestartAndGuards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	options := sdk.ConfigOptions{Overrides: map[string]string{"telemetry.database": path}}
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		client *sdk.Client
		ctx    context.Context
		task   string
	}{
		{nil, context.Background(), "task"}, {client, nil, "task"}, {client, context.Background(), "bad:task"},
	} {
		if _, err := tc.client.InspectTaskUsage(tc.ctx, tc.task); err == nil {
			t.Fatal("invalid inspection accepted", tc)
		}
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("guard created storage", err)
	}
	db, err := telemetry.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	start := runtime.Event{Version: 1, ID: "start", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted}
	if err = db.Append(context.Background(), 0, start); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := client.InspectTaskUsage(context.Background(), "task")
	if err != nil || got.Validate() != nil || got.Scope.TaskID != "task" || got.Scope.SessionID != "session" {
		t.Fatal(got, err)
	}
	restarted, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	again, err := restarted.InspectTaskUsage(context.Background(), "task")
	if err != nil || again.Validate() != nil || again.Scope != got.Scope {
		t.Fatal(again, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = client.InspectTaskUsage(canceled, "task"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation identity lost", err)
	}
}
