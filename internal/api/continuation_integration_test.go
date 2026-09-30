package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func TestTaskContinuationHTTPReadsCoherentSQLiteMetadata(t *testing.T) {
	ctx := context.Background()
	db, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "continuation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TaskCompleted} {
		e := runtime.Event{Version: 1, ID: fmt.Sprintf("event-%d", i), TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: int64(i + 1), Time: time.Now(), Kind: kind}
		if i == 0 {
			e.Data.Messages = []providers.Message{{Role: "user", Content: "private prompt must never be returned"}}
		}
		if err := db.Append(ctx, int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	before, err := db.Read(ctx, "task", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	s := services()
	s.TaskContinuation = db.TaskContinuation
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	for _, task := range []string{"task", "missing"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/tasks/"+task+"/continuation", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "private") {
			t.Fatal("history leaked")
		}
		if task == "missing" {
			if resp.StatusCode != 404 {
				t.Fatal(resp.StatusCode, string(body))
			}
			continue
		}
		var status sessions.ContinuationStatus
		if resp.StatusCode != 200 || json.Unmarshal(body, &status) != nil || !status.HistoryEligible || status.Reason != "completed" || status.Sequence != 2 || status.TaskID != "task" {
			t.Fatal(resp.StatusCode, string(body))
		}
	}
	after, err := db.Read(ctx, "task", 0, 10)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("inspection changed journal", err)
	}
}
