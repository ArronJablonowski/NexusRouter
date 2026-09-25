package app

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/internal/browserops"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	contract "github.com/ArronJablonowski/DarwinRouter/webui"
)

func TestBrowserApprovalReceiptRetryPreservesCommittedDenial(t *testing.T) {
	ctx := context.Background()
	svc, cfg := autoFixture(t)
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	request := browserApprovalRecord(approvals.Pending, nil, now.Add(time.Minute)).Request
	start := inspectionEvent(request.TaskID, 1, runtime.TaskStarted, now)
	turn := inspectionEvent(request.TaskID, 2, runtime.TurnStarted, now)
	turn.TurnID, turn.AttemptID, turn.Data.ModelID, turn.Data.ProviderID = request.TurnID, "attempt", "model", "provider"
	done := inspectionEvent(request.TaskID, 3, runtime.TurnCompleted, now)
	done.TurnID, done.AttemptID, done.Data.FinishReason = turn.TurnID, turn.AttemptID, "tool_calls"
	done.Data.ToolCalls = []providers.ToolCall{{ID: request.ToolCallID, Name: request.ToolName, Arguments: []byte(`{}`)}}
	tool := inspectionEvent(request.TaskID, 4, runtime.ToolStarted, now)
	tool.TurnID, tool.AttemptID = turn.TurnID, turn.AttemptID
	tool.Data.ToolCallID, tool.Data.ToolName, tool.Data.ToolBehavior, tool.Data.Effect = request.ToolCallID, request.ToolName, request.ToolBehavior, runtime.UncertainEffect
	for _, event := range []runtime.Event{start, turn, done, tool} {
		appendInspectionEvent(t, db, event)
	}
	if _, err = db.RequestApproval(ctx, request); err != nil {
		t.Fatal(err)
	}
	ops, err := browserops.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer ops.Close()
	mutations, err := NewBrowserMutations(svc, ops)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	// Failure occurs after the approval decision commits, at the independent
	// browser receipt boundary. It must surface without undoing or duplicating
	// that decision; an exact retry can safely finish the pending receipt.
	if _, err = raw.Exec(`CREATE TRIGGER fail_browser_receipt BEFORE UPDATE ON browser_operations WHEN NEW.state='committed' BEGIN SELECT RAISE(ABORT,'injected receipt failure'); END`); err != nil {
		t.Fatal(err)
	}
	command := contract.ApprovalRequest{Version: 1, IdempotencyKey: "browser-denial-receipt-retry", TaskID: request.TaskID, ApprovalID: request.ID, Action: contract.ApprovalDeny, ExpectedRevision: 1}
	first, err := mutations.DecideApproval(ctx, browserMutationTestSubject, command)
	if !errors.Is(err, ErrBrowserMutation) || first.State != approvals.Denied {
		t.Fatalf("receipt failure hidden or denial missing: %+v %v", first, err)
	}
	current, err := InspectApproval(ctx, cfg.Telemetry.Database, request.TaskID, request.ID)
	if err != nil || current.State != approvals.Denied || len(current.Decisions) != 1 {
		t.Fatalf("committed denial lost: %+v %v", current, err)
	}
	if _, err = raw.Exec(`DROP TRIGGER fail_browser_receipt`); err != nil {
		t.Fatal(err)
	}
	second, err := mutations.DecideApproval(ctx, browserMutationTestSubject, command)
	if err != nil || second != first {
		t.Fatalf("exact retry changed durable receipt: first=%+v second=%+v err=%v", first, second, err)
	}
	replay, err := mutations.DecideApproval(ctx, browserMutationTestSubject, command)
	if err != nil || replay != second {
		t.Fatal("committed receipt replay changed", replay, err)
	}
	command.Action = contract.ApprovalAllow
	if _, err = mutations.DecideApproval(ctx, browserMutationTestSubject, command); !errors.Is(err, browserops.ErrConflict) {
		t.Fatal("changed command reused denial key", err)
	}
	current, err = InspectApproval(ctx, cfg.Telemetry.Database, request.TaskID, request.ID)
	if err != nil || current.State != approvals.Denied || len(current.Decisions) != 1 {
		t.Fatal("retry altered denial authority", current, err)
	}
}
