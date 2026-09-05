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

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
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
	h, _ := New(token, 1, s)
	before, err := db.Read(ctx, "task", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"/v1/tasks/task/approvals?limit=1", "/v1/tasks/task/approvals/approval"} {
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
	}
	stored, err := db.ReadApproval(ctx, r.Request.ID)
	if err != nil || stored.State != approvals.Pending || len(stored.Decisions) != 0 {
		t.Fatal(stored, err)
	}
	after, err := db.Read(ctx, "task", 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("inspection mutated task events", err)
	}
	for _, endpoint := range []string{"/v1/tasks/other/approvals/approval", "/v1/tasks/other/approvals"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("GET", endpoint, ""))
		if w.Code == 200 {
			t.Fatal("cross-task read accepted", w.Body.String())
		}
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
