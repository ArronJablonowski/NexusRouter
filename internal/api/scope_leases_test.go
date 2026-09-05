package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
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

func TestAPIScopeLeaseObservationDurableReadOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "scope.db")
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
			e.Data.ToolCalls = []providers.ToolCall{{ID: "call", Name: "write_file", Arguments: json.RawMessage(`{"content":"private-source"}`)}}
		}
		if kind == runtime.ToolStarted {
			e.Data = runtime.Data{ToolCallID: "call", ToolName: "write_file", Effect: runtime.UncertainEffect}
		}
		if err = db.Append(ctx, int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	record := approvalRecordFixture()
	if _, err = db.RequestApproval(ctx, record.Request); err != nil {
		t.Fatal(err)
	}
	// Readers may coexist despite expiry. Both still block a workspace writer.
	old, err := db.AcquireLease(ctx, "task", "private-expired-owner", "create_private-old", false, time.Now().Add(-time.Minute), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	live, err := db.AcquireLease(ctx, "task", "private-live-owner", "workspace", false, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.Read(ctx, "task", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	oldBefore, err := db.InspectLeases(ctx, old.Scope)
	if err != nil {
		t.Fatal(err)
	}
	liveBefore, err := db.InspectLeases(ctx, live.Scope)
	if err != nil {
		t.Fatal(err)
	}
	savedBefore, err := db.ReadApproval(ctx, "approval")
	if err != nil {
		t.Fatal(err)
	}
	services := services()
	services.ApprovalExecution = func(ctx context.Context, task, id string) (approvals.ExecutionStatus, error) {
		return app.ApprovalExecutionStatus(ctx, path, task, id)
	}
	h, err := New(token, 1, services)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("GET", "/v1/tasks/task/approvals/approval/execution", ""))
		var status approvals.ExecutionStatus
		want := approvals.ScopeLeaseObservation{Version: 1, OverlapPolicyVersion: 1, LiveReaders: 1, ExpiredReaders: 1}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil || status.Validate() != nil || status.ScopeLeases == nil || *status.ScopeLeases != want || status.ScopeWriterState != "none" {
			t.Fatal("incorrect scope observation", w.Code, w.Body.String())
		}
		for _, private := range []string{old.Token, live.Token, old.Owner, live.Owner, old.Scope, "private-source", `"token"`, `"arguments":`} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatal("inspection leaked private capability")
			}
		}
	}
	after, err := db.Read(ctx, "task", 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("journal mutated", err)
	}
	oldAfter, err := db.InspectLeases(ctx, old.Scope)
	if err != nil {
		t.Fatal(err)
	}
	liveAfter, err := db.InspectLeases(ctx, live.Scope)
	if err != nil {
		t.Fatal(err)
	}
	savedAfter, err := db.ReadApproval(ctx, "approval")
	if err != nil || !reflect.DeepEqual(savedBefore, savedAfter) || !reflect.DeepEqual(oldBefore, oldAfter) || !reflect.DeepEqual(liveBefore, liveAfter) {
		t.Fatal("inspection mutated durable authority", err)
	}
}

func TestAPIScopeLeaseObservationRejectsInvalidBackendMetadata(t *testing.T) {
	for _, mode := range []string{"version", "policy", "negative_reader", "negative_writer", "overflow", "legacy_nil"} {
		t.Run(mode, func(t *testing.T) {
			s := services()
			s.ApprovalExecution = func(context.Context, string, string) (approvals.ExecutionStatus, error) {
				status := executionStatusFixture()
				status.ScopeLeases = &approvals.ScopeLeaseObservation{Version: 1, OverlapPolicyVersion: 1}
				switch mode {
				case "version":
					status.ScopeLeases.Version = 2
				case "policy":
					status.ScopeLeases.OverlapPolicyVersion = 2
				case "negative_reader":
					status.ScopeLeases.ExpiredReaders = -1
				case "negative_writer":
					status.ScopeLeases.LiveWriters = -1
				case "overflow":
					status.ScopeLeases.LiveReaders = int(^uint(0) >> 1)
				case "legacy_nil":
					status.ScopeLeases = nil
				}
				return status, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("GET", "/v1/tasks/task/approvals/approval/execution", ""))
			want := 500
			if mode == "legacy_nil" {
				want = 200
			}
			if w.Code != want {
				t.Fatal("backend shape accepted", w.Code, w.Body.String())
			}
			if want == 500 && (strings.Contains(w.Body.String(), `"scope_leases"`) || strings.Contains(w.Body.String(), `"approval":`)) {
				t.Fatal("partial invalid backend leaked", w.Body.String())
			}
		})
	}
}
