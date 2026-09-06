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

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
)

func TestSDKLeaseAttentionReadOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "attention.db")
	db, err := telemetry.Open(ctx, path)
	if err != nil {
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
	for _, state := range []string{"open", "resolved", "all"} {
		page, err := client.ListLeaseAttention(ctx, sdk.LeaseAttentionOptions{State: state, Limit: 25})
		if err != nil || page.Version != 1 || page.StorageSchema != 26 || !page.Available || page.Items == nil || len(page.Items) != 0 || page.HasMore || page.NextCursor != "" {
			t.Fatal(page, err)
		}
		body, err := json.Marshal(page)
		if err != nil || strings.Contains(string(body), path) || strings.Contains(string(body), "owner") || strings.Contains(string(body), "token") {
			t.Fatal("private metadata exposed", err)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("inspection changed database", err)
	}
}

func TestSDKLeaseAttentionListsDurableRecordsWithoutPrivateLeaseFields(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "attention.db")
	store, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	e := runtime.Event{Version: 1, ID: "start", TaskID: "task", SessionID: "task", CorrelationID: "task", Sequence: 1, Time: now, Kind: runtime.TaskStarted}
	if err := store.Append(ctx, 0, e); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"record-a", "record-b"} {
		item := sdk.LeaseAttention{Version: 1, ID: id, TaskID: "task", Writer: true, State: "open", Reason: "expired_unreleased", FirstObserved: now, UpdatedAt: now, LeaseExpires: now.Add(-time.Minute)}
		body, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires,released) VALUES(?,?,?,?,?,?,0)`, "private-token-"+id, "task", "private-owner", "private-scope", 1, item.LeaseExpires.UnixNano()); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO lease_attention(id,lease_token,task_id,state,body) VALUES(?,?,?,?,?)`, id, "private-token-"+id, "task", "open", body); err != nil {
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
	t.Setenv("DARWIN_PROCESS_OWNER_DIR", "")
	first, err := client.ListLeaseAttention(ctx, sdk.LeaseAttentionOptions{State: "open", Limit: 1})
	if err != nil || first.Validate() != nil || len(first.Items) != 1 || first.Items[0].ID != "record-a" || !first.HasMore || first.NextCursor != "record-a" {
		t.Fatal(first, err)
	}
	second, err := client.ListLeaseAttention(ctx, sdk.LeaseAttentionOptions{State: "open", Limit: 1, After: first.NextCursor})
	if err != nil || second.Validate() != nil || len(second.Items) != 1 || second.Items[0].ID != "record-b" || second.HasMore {
		t.Fatal(second, err)
	}
	resolved, err := client.ListLeaseAttention(ctx, sdk.LeaseAttentionOptions{State: "resolved", Limit: 25})
	if err != nil || len(resolved.Items) != 0 {
		t.Fatal(resolved, err)
	}
	for _, page := range []sdk.LeaseAttentionPage{first, second} {
		body, err := json.Marshal(page)
		if err != nil {
			t.Fatal(err)
		}
		for _, private := range []string{"private-", "lease_token", "process_id", "owner", "scope", path} {
			if strings.Contains(string(body), private) {
				t.Fatal("private lease fields exposed", private)
			}
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("inspection mutated durable records", err)
	}
}

func TestSDKLeaseAttentionRejectsWithoutCreation(t *testing.T) {
	valid := sdk.LeaseAttentionOptions{State: "open", Limit: 25}
	for _, client := range []*sdk.Client{nil, {}} {
		page, err := client.ListLeaseAttention(context.Background(), valid)
		if !errors.Is(err, sdk.ErrAdmission) || page.Version != 1 || page.Items != nil {
			t.Fatal(page, err)
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
		page, err := client.ListLeaseAttention(ctx, valid)
		if err == nil || page.Version != 1 || page.Items != nil || page.Available || strings.Contains(err.Error(), path) {
			t.Fatal(page, err)
		}
		if ctx == canceled && !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation lost", err)
		}
	}
	for _, options := range []sdk.LeaseAttentionOptions{{}, {State: "private-invalid", Limit: 25}, {State: "open", Limit: 0}, {State: "open", Limit: 101}, {State: "open", Limit: 25, After: "private:cursor"}} {
		page, err := client.ListLeaseAttention(context.Background(), options)
		if !errors.Is(err, sdk.ErrAdmission) || page.Items != nil || strings.Contains(err.Error(), "private") {
			t.Fatal(page, err)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created storage", err)
	}
}

func TestSDKLeaseAttentionLegacyAvailabilityWithoutMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	store, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE lease_attention; PRAGMA user_version=23`); err != nil {
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
	page, err := client.ListLeaseAttention(ctx, sdk.LeaseAttentionOptions{State: "open", Limit: 25})
	if err != nil || page.Validate() != nil || page.StorageSchema != 23 || page.Available || len(page.Items) != 0 {
		t.Fatal(page, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("inspection migrated legacy storage", err)
	}
}
