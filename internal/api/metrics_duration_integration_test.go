package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/stateschema"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/metrics"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
)

func TestTaskDurationPropagatesSQLiteAppHTTPAndSDKExport(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private-duration-database.db")
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	at := time.Now().UTC().Add(-10 * time.Second)
	start := func(task string) runtime.Event {
		return runtime.Event{Version: 1, ID: task + "-start", TaskID: task, SessionID: "private-session-" + task, CorrelationID: task, Sequence: 1, Time: at, Kind: runtime.TaskStarted, Data: runtime.Data{Messages: []providers.Message{{Role: "user", Content: "private-duration-prompt"}}}}
	}
	legacy := start("private-legacy-task")
	if err := db.Append(ctx, 0, legacy); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// Reconstruct the immediately preceding schema: migration must not invent a
	// timing observation for a task that started before instrumentation existed.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DROP INDEX evaluations_routing_key; DROP TABLE usage_corrections; DROP TABLE usage_heads; DROP TABLE usage_records; DROP TABLE usage_metadata; DROP TABLE task_timings; DROP TABLE task_timing_metadata; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=28`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	fresh := start("private-current-task")
	if err := db.Append(ctx, 0, fresh); err != nil {
		t.Fatal(err)
	}
	for _, first := range []runtime.Event{legacy, fresh} {
		terminal := first
		terminal.ID, terminal.Sequence, terminal.Kind = first.TaskID+"-terminal", 2, runtime.TaskCompleted
		terminal.Time = at.Add(1500 * time.Millisecond)
		terminal.Data = runtime.Data{Text: "private-duration-answer"}
		if err := db.Append(ctx, 1, terminal); err != nil {
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
	cfg := config.Defaults()
	cfg.Mode, cfg.Telemetry.Database = "local_only", path
	service, err := app.NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Metrics(ctx)
	if err != nil || snapshot.Validate() != nil || snapshot.StorageSchema != stateschema.Current || snapshot.TaskDuration == nil || snapshot.Accounting == nil {
		t.Fatal("missing duration snapshot", snapshot, err)
	}
	completed := snapshot.TaskDuration.Groups[0]
	if completed.State != "completed" || completed.Count != 1 || completed.SumSeconds != 1.5 || completed.BucketCounts[3] != 1 || completed.Unavailable[0].State != "missing_start" || completed.Unavailable[0].Value != 1 {
		t.Fatal("incorrect observed/unavailable timing", completed)
	}
	s := services()
	s.Metrics = service.Metrics
	s.Run = func(context.Context, app.Request) (app.Result, error) {
		t.Fatal("metrics dispatched inference")
		return app.Result{}, nil
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(http.MethodGet, "/v1/metrics", ""))
	var received metrics.Snapshot
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &received) != nil || received.Validate() != nil || !reflect.DeepEqual(snapshot.TaskDuration, received.TaskDuration) {
		t.Fatal("HTTP lost durable timing", w.Code, w.Body.String())
	}
	assertPrivate := func(body []byte) {
		t.Helper()
		for _, private := range []string{"private-current-task", "private-legacy-task", "private-session", "private-duration-prompt", "private-duration-answer", path} {
			if bytes.Contains(body, []byte(private)) {
				t.Error("task identity/content entered duration export")
			}
		}
	}
	assertPrivate(w.Body.Bytes())
	exported := make(chan []byte, 1)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil {
			t.Error(err)
		}
		exported <- body
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	defer collector.Close()
	client, err := sdk.New(sdk.ConfigOptions{Overrides: map[string]string{"mode": "local_only", "telemetry.database": path}})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ExportMetrics(ctx, sdk.MetricsExportOptions{Endpoint: collector.URL + "/v1/metrics"}); err != nil {
		t.Fatal(err)
	}
	body := <-exported
	assertPrivate(body)
	if !json.Valid(body) || !strings.Contains(string(body), `"histogram"`) || !strings.Contains(string(body), `"sum":1.5`) || !strings.Contains(string(body), `"missing_start"`) {
		t.Fatal("SDK export omitted duration or unavailable observations", string(body))
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("metric interfaces changed durable sources", err)
	}
}
