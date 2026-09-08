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

func TestSDKLeaseAttentionHistoryReadOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	e := runtime.Event{Version: 1, ID: "start", TaskID: "task", SessionID: "task", CorrelationID: "task", Sequence: 1, Time: now, Kind: runtime.TaskStarted}
	if err := store.Append(ctx, 0, e); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires,released) VALUES('private-token','task','private-owner','private-scope',1,?,0)`, now.Add(-time.Minute).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ObserveLeaseAttentionPage(ctx, "", now, 100); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRow(`SELECT id FROM lease_attention`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE resource_leases SET released=1 WHERE token='private-token'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ObserveLeaseAttentionPage(ctx, "", now.Add(time.Second), 100); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
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
	first, err := client.ListLeaseAttentionHistory(ctx, id, sdk.LeaseAttentionHistoryOptions{Limit: 1})
	if err != nil || first.Version != 1 || first.StorageSchema != 31 || !first.Available || first.AttentionID != id || len(first.Items) != 1 || !first.HasMore || first.NextSequence != 1 || first.Items[0].Sequence != 1 || first.Items[0].Observation.State != "open" {
		t.Fatal(first, err)
	}
	second, err := client.ListLeaseAttentionHistory(ctx, id, sdk.LeaseAttentionHistoryOptions{AfterSequence: first.NextSequence, Limit: 1})
	if err != nil || len(second.Items) != 1 || second.HasMore || second.Items[0].Sequence != 2 || second.Items[0].Observation.State != "resolved" || second.Items[0].Observation.ID != id {
		t.Fatal(second, err)
	}
	for _, page := range []sdk.LeaseAttentionHistoryPage{first, second} {
		body, err := json.Marshal(page)
		if err != nil {
			t.Fatal(err)
		}
		for _, private := range []string{"private-", "lease_token", "process_id", path} {
			if strings.Contains(string(body), private) {
				t.Fatal("private data exposed")
			}
		}
	}
	missing, err := client.ListLeaseAttentionHistory(ctx, "missing", sdk.LeaseAttentionHistoryOptions{Limit: 25})
	if !errors.Is(err, sql.ErrNoRows) || missing.Items != nil {
		t.Fatal("missing identity lost", missing, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("history inspection mutated storage", err)
	}
}

func TestSDKLeaseAttentionHistoryAdmission(t *testing.T) {
	valid := sdk.LeaseAttentionHistoryOptions{Limit: 25}
	for _, client := range []*sdk.Client{nil, {}} {
		page, err := client.ListLeaseAttentionHistory(context.Background(), "record", valid)
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
		page, err := client.ListLeaseAttentionHistory(ctx, "record", valid)
		if err == nil || page.Version != 1 || page.Items != nil || strings.Contains(err.Error(), path) {
			t.Fatal(page, err)
		}
		if ctx == canceled && !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation lost", err)
		}
	}
	for _, id := range []string{"", "private:bad"} {
		page, err := client.ListLeaseAttentionHistory(context.Background(), id, valid)
		if !errors.Is(err, sdk.ErrAdmission) || page.Items != nil {
			t.Fatal(page, err)
		}
	}
	for _, options := range []sdk.LeaseAttentionHistoryOptions{{}, {Limit: 101}, {Limit: 1, AfterSequence: -1}, {Limit: 1, AfterSequence: 9223372036854775807}} {
		page, err := client.ListLeaseAttentionHistory(context.Background(), "record", options)
		if !errors.Is(err, sdk.ErrAdmission) || page.Items != nil {
			t.Fatal(page, err)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("created storage", err)
	}
}
