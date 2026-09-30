package v1_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
)

func TestSDKScopeLeasesReadOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "leases.db")
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for i, task := range []string{"holder", "other"} {
		event := runtime.Event{Version: 1, ID: task, TaskID: task, SessionID: task, CorrelationID: task, Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted}
		if err := db.Append(ctx, 0, event); err != nil {
			t.Fatal(err)
		}
		scope := "create_private-alias"
		if i == 1 {
			scope = "private-other-scope"
		}
		if _, err := db.AcquireLease(ctx, task, "private-owner", scope, false, time.Now().UTC(), time.Minute); err != nil {
			t.Fatal(err)
		}
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
	status, err := client.InspectScopeLeases(ctx, "workspace")
	if err != nil || status.Validate() != nil || status.Version != 1 || status.Scope != "workspace" || !status.Available || len(status.Holders) != 1 || status.Holders[0].TaskID != "holder" || status.Holders[0].LiveReaders != 1 || status.ObservedAt.IsZero() {
		t.Fatal(status, err)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-", "token", "owner", "process_id", path} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("exposed %s", forbidden)
		}
	}
	empty, err := client.InspectScopeLeases(ctx, "empty-scope")
	if err != nil || !empty.Available || len(empty.Holders) != 0 {
		t.Fatal(empty, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("inspection changed database", err)
	}
}

func TestSDKScopeLeasesRejectsWithoutMutation(t *testing.T) {
	for _, client := range []*sdk.Client{nil, {}} {
		status, err := client.InspectScopeLeases(context.Background(), "workspace")
		if !errors.Is(err, sdk.ErrAdmission) || status.Version != 1 || status.Scope != "" || status.Holders != nil {
			t.Fatal(status, err)
		}
	}
	path := filepath.Join(t.TempDir(), "missing.db")
	client, err := sdk.New(sdk.ConfigOptions{Overrides: map[string]string{"telemetry.database": path}})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, canceled, context.Background()} {
		status, err := client.InspectScopeLeases(ctx, "workspace")
		if err == nil || status.Version != 1 || status.Scope != "" || status.Holders != nil || strings.Contains(err.Error(), path) {
			t.Fatal(status, err)
		}
		if ctx == canceled && !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation identity lost", err)
		}
	}
	for _, scope := range []string{"", " workspace", "workspace ", "private\nmarker", strings.Repeat("a", 513), string([]byte{255})} {
		status, err := client.InspectScopeLeases(context.Background(), scope)
		if !errors.Is(err, sdk.ErrAdmission) || status.Scope != "" || status.Holders != nil {
			t.Fatal(status, err)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created storage", err)
	}
}
