package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func attentionHTTPFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "attention.db")
	store, err := telemetry.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	e := runtime.Event{Version: 1, ID: "start", TaskID: "task", SessionID: "task", CorrelationID: "task", Sequence: 1, Time: now, Kind: runtime.TaskStarted}
	if err := store.Append(context.Background(), 0, e); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, id := range []string{"attention-a", "attention-b", "attention-c"} {
		item := workers.LeaseAttention{Version: 1, ID: id, TaskID: "task", Writer: true, State: "open", Reason: "expired_unreleased", FirstObserved: now, UpdatedAt: now, LeaseExpires: now.Add(-time.Minute)}
		released := 0
		if id == "attention-b" {
			item.State, item.Reason, released = "resolved", "lease_released", 1
		}
		body, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires,released) VALUES(?,?,?,?,?,?,?)`, "private-token-"+id, "task", "private-owner", "private-scope", 1, item.LeaseExpires.UnixNano(), released); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO lease_attention(id,lease_token,task_id,state,body) VALUES(?,?,?,?,?)`, id, "private-token-"+id, "task", item.State, body); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func attentionHTTPHandler(t *testing.T, path string) *Handler {
	t.Helper()
	s := services()
	s.Run = func(context.Context, app.Request) (app.Result, error) {
		t.Fatal("attention inspection executed task")
		return app.Result{}, nil
	}
	s.LeaseAttention = func(ctx context.Context, options workers.LeaseAttentionOptions) (workers.LeaseAttentionPage, error) {
		return app.InspectLeaseAttention(ctx, path, options)
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestLeaseAttentionHTTPReadOnlySQLite(t *testing.T) {
	path := attentionHTTPFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DARWIN_PROCESS_OWNER_DIR", "")
	t.Setenv("DARWIN_MODE", "private-invalid-config")
	h := attentionHTTPHandler(t, path)
	var first workers.LeaseAttentionPage
	for _, tc := range []struct {
		query  string
		ids    []string
		more   bool
		cursor string
	}{
		{"", []string{"attention-a", "attention-c"}, false, ""},
		{"?limit=1", []string{"attention-a"}, true, "attention-a"},
		{"?limit=1&after=attention-a", []string{"attention-c"}, false, ""},
		{"?state=resolved", []string{"attention-b"}, false, ""},
		{"?state=all", []string{"attention-a", "attention-b", "attention-c"}, false, ""},
		{"?limit=1", []string{"attention-a"}, true, "attention-a"},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, continuationRequest("GET", "/v1/resources/attention"+tc.query, ""))
		var page workers.LeaseAttentionPage
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Validate() != nil || !page.Available || page.StorageSchema != 25 || page.HasMore != tc.more || page.NextCursor != tc.cursor {
			t.Fatal(tc.query, w.Code, w.Body.String())
		}
		ids := []string{}
		for _, item := range page.Items {
			ids = append(ids, item.ID)
		}
		if !reflect.DeepEqual(ids, tc.ids) {
			t.Fatal(tc.query, ids)
		}
		if tc.query == "?limit=1" {
			if first.Version != 0 && !reflect.DeepEqual(first, page) {
				t.Fatal("stable observation changed")
			}
			first = page
		}
		for _, private := range []string{"private-", "lease_token", "process_id", "darwin-owner-", "scope", "owner", path} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatal("private metadata exposed", private)
			}
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("HTTP inspection mutated database", err)
	}
}

func TestLeaseAttentionHTTPLegacyAndMissingStorage(t *testing.T) {
	path := attentionHTTPFixture(t)
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
	w := httptest.NewRecorder()
	attentionHTTPHandler(t, path).ServeHTTP(w, continuationRequest("GET", "/v1/resources/attention", ""))
	var page workers.LeaseAttentionPage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Validate() != nil || page.StorageSchema != 23 || page.Available || len(page.Items) != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("HTTP inspection migrated storage", err)
	}
	missing := filepath.Join(t.TempDir(), "absent", "private-database.db")
	w = httptest.NewRecorder()
	attentionHTTPHandler(t, missing).ServeHTTP(w, continuationRequest("GET", "/v1/resources/attention", ""))
	if w.Code != 503 || strings.Contains(w.Body.String(), missing) || strings.Contains(w.Body.String(), "private-") {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Dir(missing)); !os.IsNotExist(err) {
		t.Fatal("HTTP initialized storage", err)
	}
}
