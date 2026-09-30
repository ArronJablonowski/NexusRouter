package v1_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
)

func TestSDKTaskLeasesRejectsWithoutCreatingStorage(t *testing.T) {
	for _, client := range []*sdk.Client{nil, {}} {
		status, err := client.InspectTaskLeases(context.Background(), "task")
		if !errors.Is(err, sdk.ErrAdmission) || status.Version != 1 || status.TaskID != "" || status.Leases != nil || status.Recoveries != nil {
			t.Fatal(status, err)
		}
	}
	path := filepath.Join(t.TempDir(), "absent.db")
	client, err := sdk.New(sdk.ConfigOptions{Overrides: map[string]string{"telemetry.database": path}})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, canceled, context.Background()} {
		status, err := client.InspectTaskLeases(ctx, "task")
		if err == nil || status.Version != 1 || status.TaskID != "" || status.Leases != nil || status.Recoveries != nil || strings.Contains(err.Error(), path) {
			t.Fatal(status, err)
		}
		if ctx == canceled && !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation identity lost", err)
		}
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("inspection created storage", err)
	}
}

func TestSDKTaskLeasesMetadataDoesNotMutate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "task.db")
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	event := runtime.Event{Version: 1, ID: "start", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted}
	event.Data.Messages = []providers.Message{{Role: "user", Content: "private-prompt-marker"}}
	if err := db.Append(ctx, 0, event); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	client, err := sdk.New(sdk.ConfigOptions{Overrides: map[string]string{"telemetry.database": path}})
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.InspectTaskLeases(ctx, "task")
	if err != nil || status.Version != 1 || status.TaskID != "task" || status.Sequence != 1 || status.TaskState != "running" || status.Leases == nil || status.Recoveries == nil {
		t.Fatal(status, err)
	}
	metadata, err := json.Marshal(status)
	if err != nil || strings.Contains(string(metadata), "private-") || strings.Contains(string(metadata), path) {
		t.Fatal("private content exposed", err)
	}
	for _, task := range []string{"missing", "bad:id", ""} {
		failed, err := client.InspectTaskLeases(ctx, task)
		if err == nil || failed.Version != 1 || failed.TaskID != "" || failed.Leases != nil || failed.Recoveries != nil {
			t.Fatal("partial status on failure", failed, err)
		}
		if task == "missing" && !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("missing task identity lost", err)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("inspection changed database", err)
	}
}
