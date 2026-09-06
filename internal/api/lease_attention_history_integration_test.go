package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func TestAttentionHistoryHTTPReadOnlySQLite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	e := runtime.Event{Version: 1, ID: "start", TaskID: "task", SessionID: "task", CorrelationID: "task", Sequence: 1, Time: now, Kind: runtime.TaskStarted}
	if err := store.Append(ctx, 0, e); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires,released) VALUES('private-token','task','private-owner','private-scope',1,?,0)`, now.Add(-time.Minute).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, n, err := store.ObserveLeaseAttentionPage(ctx, "", now, 100); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	page, err := store.ListLeaseAttention(ctx, workers.LeaseAttentionOptions{State: "open", Limit: 1})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	id := page.Items[0].ID
	if _, err := raw.Exec(`UPDATE resource_leases SET released=1`); err != nil {
		t.Fatal(err)
	}
	if _, n, err := store.ObserveLeaseAttentionPage(ctx, "", now.Add(time.Second), 100); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := services()
	s.LeaseAttentionHistory = func(ctx context.Context, id string, o workers.LeaseAttentionHistoryOptions) (workers.LeaseAttentionHistoryPage, error) {
		return app.InspectLeaseAttentionHistory(ctx, path, id, o)
	}
	h, _ := New(token, 1, s)
	for _, tc := range []struct {
		query    string
		sequence int64
		state    string
		more     bool
	}{{"?limit=1", 1, "open", true}, {"?after_sequence=1&limit=1", 2, "resolved", false}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, continuationRequest("GET", "/v1/resources/attention/"+id+"/history"+tc.query, ""))
		var got workers.LeaseAttentionHistoryPage
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Validate() != nil || len(got.Items) != 1 || got.Items[0].Sequence != tc.sequence || got.Items[0].Observation.State != tc.state || got.HasMore != tc.more {
			t.Fatal(w.Code, w.Body.String())
		}
		for _, secret := range []string{"private-token", "private-owner", "private-scope", "lease_token", "process_id", "darwin-owner-"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("private authority leaked")
			}
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, continuationRequest("GET", "/v1/resources/attention/missing/history", ""))
	if w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("HTTP inspection changed database", err)
	}
}
