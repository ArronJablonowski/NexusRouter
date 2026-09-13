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
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/metrics"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestMetricsSurviveServiceRestartWithoutExecution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/api/chat" || n > 2 {
			t.Error("metrics caused unexpected provider request")
			http.Error(w, "unexpected request", 500)
			return
		}
		answer := "private-output"
		if n == 2 {
			answer = " "
		}
		fmt.Fprintf(w, `{"message":{"content":%q},"done":true,"done_reason":"stop"}`, answer)
	}))
	defer provider.Close()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "metrics.db")
	cfg.Providers = []config.Provider{{ID: "private-provider", Kind: "ollama", Endpoint: provider.URL}}
	cfg.Models = []config.Model{{ID: "private-model", Provider: "private-provider", Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}}}
	svc, err := newAPIFixtureService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	success, err := svc.Run(ctx, app.Request{ModelID: "private-model", Prompt: "private-prompt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Run(ctx, app.Request{ModelID: "private-model", Prompt: "private-prompt"}); err == nil {
		t.Fatal("empty final answer should fail")
	}
	for _, key := range []string{"0123456789abcdef", "0123456789abcdeg"} {
		job, err := svc.Submit(ctx, key, app.Request{ModelID: "private-model", Prompt: "private-prompt"})
		if err != nil {
			t.Fatal(err)
		}
		if key == "0123456789abcdeg" {
			if _, err := svc.CancelSubmission(ctx, job.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	before, err := svc.Metrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A fresh application has no process-local counters from those executions.
	restarted, err := newAPIFixtureService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(token, 1, Services{
		Run: func(context.Context, app.Request) (app.Result, error) {
			t.Error("metrics dispatched task")
			return app.Result{}, nil
		},
		Inspect: func(context.Context, string) (sessions.Snapshot, error) {
			t.Error("metrics inspected private history")
			return sessions.Snapshot{}, nil
		},
		Health:  func(context.Context) error { t.Error("metrics ran health probe"); return nil },
		Metrics: restarted.Metrics,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	r, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/v1/metrics", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	response, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	// Schema-30 accounting adds a fixed, identifier-free totals projection to
	// the otherwise bounded metrics snapshot.
	if err != nil || response.StatusCode != 200 || len(body) > 16<<10 {
		t.Fatal("metrics response", response.StatusCode, err)
	}
	for _, secret := range []string{"private-", token, success.TaskID, cfg.Telemetry.Database, provider.URL, "0123456789abcdef"} {
		if strings.Contains(string(body), secret) {
			t.Fatal("sensitive metrics response")
		}
	}
	var after metrics.Snapshot
	if json.Unmarshal(body, &after) != nil || after.Validate() != nil || !reflect.DeepEqual(before.Groups, after.Groups) {
		t.Fatal("restart changed stored metrics", before, after)
	}
	want := map[string]map[string]int64{
		"tasks":       {"completed": 1, "failed": 1},
		"submissions": {"queued": 1, "canceled": 1},
	}
	for _, group := range after.Groups {
		if expected, ok := want[group.Name]; ok {
			for _, count := range group.Counts {
				if count.Value != expected[count.State] {
					t.Fatal(group.Name, count)
				}
			}
		}
	}
	if calls.Load() != 2 {
		t.Fatal("metrics called provider", calls.Load())
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	history, err := sessions.Replay(ctx, db, success.TaskID)
	if err != nil || history.State != "completed" {
		t.Fatal("original task changed", history, err)
	}
}
