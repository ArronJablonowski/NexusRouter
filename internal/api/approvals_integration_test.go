package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestApprovalAPIReadsDurableLedgerWithoutMutation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "approvals.db")
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.ToolStarted} {
		e := runtime.Event{Version: 1, ID: string(kind), TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: int64(i + 1), Time: time.Now().UTC(), Kind: kind}
		if i > 0 {
			e.TurnID, e.AttemptID = "turn", "attempt"
		}
		if kind == runtime.TurnCompleted {
			e.Data.ToolCalls = []providers.ToolCall{{ID: "call", Name: "write_file", Arguments: json.RawMessage(`{"content":"private-source-argument"}`)}}
		}
		if kind == runtime.ToolStarted {
			e.Data = runtime.Data{ToolCallID: "call", ToolName: "write_file", Effect: runtime.UncertainEffect}
		}
		if err = db.Append(ctx, int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	r := approvalRecordFixture()
	if _, err = db.RequestApproval(ctx, r.Request); err != nil {
		t.Fatal(err)
	}
	s := services()
	s.Run = func(context.Context, app.Request) (app.Result, error) {
		t.Error("inspection invoked runtime")
		return app.Result{}, nil
	}
	s.Approval = func(ctx context.Context, task, id string) (approvals.Record, error) {
		return app.InspectApproval(ctx, path, task, id)
	}
	s.Approvals = func(ctx context.Context, q approvals.ListOptions) (approvals.Page, error) {
		return app.ListApprovals(ctx, path, q)
	}
	s.ApprovalExecution = func(ctx context.Context, task, id string) (approvals.ExecutionStatus, error) {
		return app.ApprovalExecutionStatus(ctx, path, task, id)
	}
	h, _ := New(token, 1, s)
	before, err := db.Read(ctx, "task", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"/v1/tasks/task/approvals?limit=1", "/v1/tasks/task/approvals/approval", "/v1/tasks/task/approvals/approval/execution"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("GET", endpoint, ""))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "private-source-argument") || strings.Contains(w.Body.String(), `"arguments":`) {
			t.Fatal("inspection disclosed raw arguments")
		}
		if endpoint == "/v1/tasks/task/approvals/approval" {
			var got approvals.Record
			if json.Unmarshal(w.Body.Bytes(), &got) != nil || got.State != approvals.Pending || !got.Request.Matches(r.Request) {
				t.Fatal(w.Body.String())
			}
		}
		if endpoint == "/v1/tasks/task/approvals/approval/execution" {
			var got approvals.ExecutionStatus
			if json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Validate() != nil || got.CallState != "open" || got.ScopeWriterState != "none" || got.Sequence != 4 || !got.Approval.Request.Matches(r.Request) {
				t.Fatal("incorrect durable execution observation", w.Body.String())
			}
		}
	}
	stored, err := db.ReadApproval(ctx, r.Request.ID)
	if err != nil || stored.State != approvals.Pending || len(stored.Decisions) != 0 {
		t.Fatal(stored, err)
	}
	after, err := db.Read(ctx, "task", 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("inspection mutated task events", err)
	}
	for _, endpoint := range []string{"/v1/tasks/other/approvals/approval", "/v1/tasks/other/approvals", "/v1/tasks/other/approvals/approval/execution"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("GET", endpoint, ""))
		if w.Code == 200 {
			t.Fatal("cross-task read accepted", w.Body.String())
		}
	}
	// The separate authenticated decision control changes authority, not task
	// execution. A stable command retry returns the same persisted decision.
	cfg := config.Defaults()
	cfg.Telemetry.Database = path
	svc, err := app.NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.DecideApproval = func(ctx context.Context, c approvals.Command) (approvals.Record, error) {
		return svc.DecideApproval(ctx, c, "api_operator")
	}
	h, err = New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	command := approvals.Command{Expected: r.Request, ID: "http-decision", Allowed: true}
	for range 2 {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("POST", "/v1/tasks/task/approvals/approval/decision", decisionCommandBody(command)))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	stored, err = db.ReadApproval(ctx, r.Request.ID)
	if err != nil || stored.State != approvals.Approved || len(stored.Decisions) != 1 || stored.Decisions[0].Actor != "api_operator" {
		t.Fatal(stored, err)
	}
	after, err = db.Read(ctx, "task", 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("decision invoked task execution", err)
	}
}

func TestApprovalAPIAbsentDatabaseNotCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.db")
	s := services()
	s.Approvals = func(ctx context.Context, q approvals.ListOptions) (approvals.Page, error) {
		return app.ListApprovals(ctx, path, q)
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("GET", "/v1/tasks/task/approvals", ""))
	if w.Code == 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("inspection created database", err)
	}
}
