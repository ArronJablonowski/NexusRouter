package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func behaviorApprovalFixture(t *testing.T, behavior runtime.ToolBehavior) (*Store, string, approvals.Request) {
	t.Helper()
	s, path := leaseStore(t)
	ctx := context.Background()
	for i, kind := range []runtime.Kind{runtime.TurnStarted, runtime.TurnCompleted, runtime.ToolStarted} {
		e := event(string(kind), int64(i+2), kind)
		e.TurnID = "turn"
		e.AttemptID = "attempt"
		if kind == runtime.TurnCompleted {
			e.Data.ToolCalls = []providers.ToolCall{{ID: "call", Name: "write_file", Arguments: []byte(`{}`)}}
		}
		if kind == runtime.ToolStarted {
			e.Data = runtime.Data{ToolCallID: "call", ToolName: "write_file", Effect: runtime.UncertainEffect, ToolBehavior: behavior}
		}
		if err := s.Append(ctx, int64(i+1), e); err != nil {
			t.Fatal(err)
		}
	}
	req := approvals.Request{Version: 1, ID: "approval", TaskID: "task", TurnID: "turn", ToolCallID: "call", ToolName: "write_file", ToolBehavior: behavior, Scope: "workspace", ArgumentsDigest: strings.Repeat("a", 64), SchemaDigest: strings.Repeat("b", 64), PolicyDigest: strings.Repeat("c", 64), CreatedAt: time.Unix(100, 0).UTC(), ExpiresAt: time.Unix(200, 0).UTC()}
	return s, path, req
}

func TestApprovalExecutionRejectsForgedBehaviorAtOpenAndCompleted(t *testing.T) {
	for _, completed := range []bool{false, true} {
		for _, forged := range []runtime.ToolBehavior{"", runtime.BehaviorReadOnly, runtime.BehaviorNonIdempotentWrite} {
			t.Run(string(forged)+map[bool]string{false: "open", true: "completed"}[completed], func(t *testing.T) {
				ctx := context.Background()
				s, _, req := behaviorApprovalFixture(t, runtime.BehaviorIdempotentWrite)
				record, err := s.RequestApproval(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				if completed {
					e := event("tool_done", 5, runtime.ToolCompleted)
					e.TurnID = "turn"
					e.AttemptID = "attempt"
					e.Data = runtime.Data{ToolCallID: "call", ToolName: "write_file", ToolBehavior: req.ToolBehavior, Effect: runtime.NoEffect, Code: "tool_failed"}
					if err = s.Append(ctx, 4, e); err != nil {
						t.Fatal(err)
					}
				}
				if _, err = s.ApprovalExecutionStatus(ctx, req.TaskID, req.ID, req.CreatedAt.Add(time.Second)); err != nil {
					t.Fatal("valid projection rejected", err)
				}
				record.Request.ToolBehavior = forged
				body, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.db.Exec(`UPDATE tool_approvals SET body=? WHERE id=?`, body, req.ID); err != nil {
					t.Fatal(err)
				}
				out, err := s.ApprovalExecutionStatus(ctx, req.TaskID, req.ID, req.CreatedAt.Add(time.Second))
				if !errors.Is(err, approvals.ErrInvalid) || out.Version != 0 || out.Approval.Request.ID != "" {
					t.Fatal("forged behavior correlated", out, err)
				}
			})
		}
	}
}

func TestApprovalBehaviorPersistsAndBindsConsume(t *testing.T) {
	for _, behavior := range []runtime.ToolBehavior{runtime.BehaviorIdempotentWrite, runtime.BehaviorNonIdempotentWrite} {
		t.Run(string(behavior), func(t *testing.T) {
			ctx := context.Background()
			s, path, req := behaviorApprovalFixture(t, behavior)
			wrong := req
			wrong.ToolBehavior = ""
			if _, err := s.RequestApproval(ctx, wrong); !errors.Is(err, approvals.ErrConflict) {
				t.Fatal("missing behavior admitted", err)
			}
			if _, err := s.RequestApproval(ctx, req); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DecideBoundApproval(ctx, approvals.Command{Expected: wrong, ID: "wrong-decision", Allowed: true}, "operator", req.CreatedAt.Add(time.Second)); !errors.Is(err, approvals.ErrConflict) {
				t.Fatal("unbound decision", err)
			}
			if _, err := s.DecideApproval(ctx, req.ID, approvals.Decision{ID: "decision", Actor: "operator", Allowed: true, Time: req.CreatedAt.Add(time.Second)}); err != nil {
				t.Fatal(err)
			}
			other, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			got, err := other.ReadApproval(ctx, req.ID)
			if err != nil || !got.Request.Matches(req) {
				t.Fatal(got, err)
			}
			page, err := other.ListApprovals(ctx, approvals.ListOptions{TaskID: req.TaskID, Limit: 10})
			if err != nil || len(page.Records) != 1 || page.Records[0].Request.ToolBehavior != behavior {
				t.Fatal(page, err)
			}
			now := req.CreatedAt.Add(2 * time.Second)
			status, err := other.ApprovalExecutionStatus(ctx, req.TaskID, req.ID, now)
			if err != nil || status.Approval.Request.ToolBehavior != behavior {
				t.Fatal(status, err)
			}
			reader, err := other.AcquireLease(ctx, req.TaskID, "reader", req.Scope, false, now, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = other.ConsumeApproval(ctx, req, reader.Token, "reader", now); !errors.Is(err, approvals.ErrConflict) {
				t.Fatal("idempotent writer admitted with reader lease", err)
			}
			if err = other.ReleaseLease(ctx, reader.Token, "reader"); err != nil {
				t.Fatal(err)
			}
			lease, err := other.AcquireLease(ctx, req.TaskID, "owner", req.Scope, true, now, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = other.ConsumeApproval(ctx, wrong, lease.Token, "owner", now); !errors.Is(err, approvals.ErrConflict) {
				t.Fatal("unbound consume", err)
			}
			wrong.ToolBehavior = runtime.BehaviorReadOnly
			if _, err = other.ConsumeApproval(ctx, wrong, lease.Token, "owner", now); !errors.Is(err, approvals.ErrConflict) {
				t.Fatal("downgraded consume", err)
			}
			if _, err = other.ConsumeApproval(ctx, req, lease.Token, "owner", now); err != nil {
				t.Fatal(err)
			}
			if _, err = other.ConsumeApproval(ctx, req, lease.Token, "owner", now); !errors.Is(err, approvals.ErrConflict) {
				t.Fatal("reused idempotent approval", err)
			}
		})
	}
}
