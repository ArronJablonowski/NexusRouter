package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func TestTaskLeasesHTTPReadOnlySQLite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "leases.db")
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e := runtime.Event{Version: 1, ID: "event", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted,
		Data: runtime.Data{Messages: []providers.Message{{Role: "user", Content: "private-prompt-content"}}}}
	if err := db.Append(ctx, 0, e); err != nil {
		t.Fatal(err)
	}
	private := []string{"private-prompt-content", "private-live-owner", "private-expired-owner", "private-released-owner", "private-read-scope", "private-write-scope"}
	for _, spec := range []struct {
		owner, scope              string
		writer, expired, released bool
	}{
		{"private-live-owner", "private-read-scope", false, false, false},
		{"private-expired-owner", "private-read-scope", false, true, false},
		{"private-released-owner", "private-write-scope", true, false, true},
	} {
		now := time.Now().UTC()
		if spec.expired {
			now = now.Add(-time.Hour)
		}
		lease, err := db.AcquireLease(ctx, "task", spec.owner, spec.scope, spec.writer, now, 10*time.Minute)
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
	s.TaskLeases = func(ctx context.Context, task string) (workers.TaskLeaseStatus, error) {
		return app.InspectTaskLeases(ctx, path, task)
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range []string{"task", "missing", "task"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, continuationRequest("GET", "/v1/tasks/"+task+"/leases", ""))
		for _, value := range private {
			if strings.Contains(w.Body.String(), value) {
				t.Fatal("lease observation leaked private content")
			}
		}
		if task == "missing" {
			if w.Code != 404 {
				t.Fatal(w.Code, w.Body.String())
			}
			continue
		}
		var status workers.TaskLeaseStatus
		want := workers.TaskLeaseCounts{LiveReaders: 1, ExpiredReaders: 1, ReleasedWriters: 1}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil || status.Validate() != nil || status.TaskID != "task" || status.TaskState != "running" || status.Sequence != 1 || status.StorageSchema != 26 || status.Leases == nil || *status.Leases != want || status.Recoveries == nil || *status.Recoveries != (workers.TaskRecoveryCounts{}) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("inspection mutated database bytes", err)
	}
	// SQLite may materialize WAL coordination sidecars even for mode=ro;
	// the durable database bytes and application records must remain unchanged.
}

func TestTaskLeasesHTTPDoesNotInitializeMissingStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "leases.db")
	s := services()
	s.TaskLeases = func(ctx context.Context, task string) (workers.TaskLeaseStatus, error) {
		return app.InspectTaskLeases(ctx, path, task)
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, continuationRequest("GET", "/v1/tasks/task/leases", ""))
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("inspection initialized storage directory", err)
	}
	if strings.Contains(w.Body.String(), path) {
		t.Fatal("storage path leaked")
	}
}
