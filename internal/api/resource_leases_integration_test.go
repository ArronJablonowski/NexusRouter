package api

import (
	"bytes"
	"context"
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
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func TestResourceLeasesHTTPReadOnlySQLite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "leases.db")
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	private := []string{"private-prompt-content", "private-owner", "create_private-alias", "darwin-owner-", "process_id", "lease_token"}
	for _, task := range []string{"task-a", "task-b"} {
		e := runtime.Event{Version: 1, ID: "event-" + task, TaskID: task, SessionID: "session", CorrelationID: task, Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted, Data: runtime.Data{Messages: []providers.Message{{Role: "user", Content: "private-prompt-content"}}}}
		if err := db.Append(ctx, 0, e); err != nil {
			t.Fatal(err)
		}
	}
	for _, spec := range []struct {
		task, scope       string
		expired, released bool
	}{
		{"task-a", "workspace", false, false},
		{"task-a", "create_private-alias", true, false},
		{"task-b", "create_private-alias", false, false},
		{"task-b", "workspace", false, true},
	} {
		now := time.Now().UTC()
		if spec.expired {
			now = now.Add(-time.Hour)
		}
		lease, err := db.AcquireLease(ctx, spec.task, "private-owner", spec.scope, false, now, 10*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		private = append(private, lease.Token)
		if spec.released {
			if err := db.ReleaseLease(ctx, lease.Token, lease.Owner); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := services()
	s.Run = func(context.Context, app.Request) (app.Result, error) {
		t.Fatal("inspection executed task")
		return app.Result{}, nil
	}
	s.ScopeLeases = func(ctx context.Context, scope string) (workers.ScopeLeaseStatus, error) {
		return app.InspectScopeLeases(ctx, path, scope)
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"workspace", "unheld", "workspace"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, continuationRequest("GET", "/v1/resources/leases?scope="+scope, ""))
		for _, value := range private {
			if strings.Contains(w.Body.String(), value) {
				t.Fatal("inspection leaked private content")
			}
		}
		var out workers.ScopeLeaseStatus
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Validate() != nil || out.Scope != scope || !out.Available || out.StorageSchema != 32 {
			t.Fatal(w.Code, w.Body.String())
		}
		want := []workers.ScopeLeaseHolder{}
		if scope == "workspace" {
			want = []workers.ScopeLeaseHolder{{TaskID: "task-a", LiveReaders: 1, ExpiredReaders: 1}, {TaskID: "task-b", LiveReaders: 1}}
		}
		if !reflect.DeepEqual(out.Holders, want) {
			t.Fatal(out.Holders, want)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("inspection mutated database bytes", err)
	}
	// SQLite may create WAL coordination sidecars on a read-only open; durable
	// database bytes remain unchanged and the inspector never obtains ownership.
}

func TestResourceLeasesHTTPDoesNotInitializeMissingStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "leases.db")
	s := services()
	s.ScopeLeases = func(ctx context.Context, scope string) (workers.ScopeLeaseStatus, error) {
		return app.InspectScopeLeases(ctx, path, scope)
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, continuationRequest("GET", "/v1/resources/leases?scope=workspace", ""))
	if w.Code != 503 || strings.Contains(w.Body.String(), path) {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("inspection initialized storage", err)
	}
}
