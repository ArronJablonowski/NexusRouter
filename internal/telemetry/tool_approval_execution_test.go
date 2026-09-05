package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func executionApprovalFixture(t *testing.T) (*Store, string, approvals.Request, Lease) {
	t.Helper()
	s, path, req := approvalFixture(t)
	ctx := context.Background()
	if _, err := s.RequestApproval(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecideBoundApproval(ctx, approvals.Command{Expected: req, ID: "decision", Allowed: true}, "operator", req.CreatedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	lease, err := s.AcquireLease(ctx, req.TaskID, "private-owner", req.Scope, true, req.CreatedAt.Add(time.Second), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConsumeApproval(ctx, req, lease.Token, lease.Owner, req.CreatedAt.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	return s, path, req, lease
}

func completeApprovalCall(t *testing.T, s *Store, effect runtime.Effect) {
	t.Helper()
	e := event("tool_completed", 5, runtime.ToolCompleted)
	e.TurnID = "turn"
	e.AttemptID = "attempt"
	e.Data = runtime.Data{ToolCallID: "call", ToolName: "write_file", Effect: effect, Text: "private-result"}
	if err := s.Append(context.Background(), 4, e); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalExecutionOpenAndWriterStateNonmutation(t *testing.T) {
	s, path, req, lease := executionApprovalFixture(t)
	ctx := context.Background()
	reader, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for _, tc := range []struct {
		now  time.Time
		want string
	}{{req.CreatedAt.Add(3 * time.Second), "live"}, {lease.Expires, "expired"}, {lease.Expires.Add(time.Hour), "expired"}} {
		out, err := reader.ApprovalExecutionStatus(ctx, req.TaskID, req.ID, tc.now)
		if err != nil || out.Validate() != nil || out.Approval.State != approvals.Consumed || out.TaskState != "running" || out.Sequence != 4 || out.CallState != "open" || out.RecordedEffect != "" || out.ScopeWriterState != tc.want || !out.ObservedAt.Equal(tc.now) {
			t.Fatalf("out=%+v err=%v", out, err)
		}
		body, _ := json.Marshal(out)
		if strings.Contains(string(body), lease.Token) || strings.Contains(string(body), lease.Owner) || strings.Contains(string(body), "private-result") {
			t.Fatal("private execution data escaped")
		}
	}
	var released int
	if err := s.db.QueryRow(`SELECT released FROM resource_leases WHERE token=?`, lease.Token).Scan(&released); err != nil || released != 0 {
		t.Fatal("inspection released writer", released, err)
	}
	if _, err := s.AcquireLease(ctx, req.TaskID, "other", req.Scope, true, lease.Expires.Add(time.Hour), time.Minute); err != ErrLeaseBusy {
		t.Fatal("inspection enabled takeover", err)
	}
	if err := s.ReleaseLease(ctx, lease.Token, lease.Owner); err != nil {
		t.Fatal(err)
	}
	out, err := reader.ApprovalExecutionStatus(ctx, req.TaskID, req.ID, lease.Expires.Add(time.Hour))
	if err != nil || out.ScopeWriterState != "none" || out.CallState != "open" {
		t.Fatal(out, err)
	}
	r, err := s.ReadApproval(ctx, req.ID)
	if err != nil || r.State != approvals.Consumed {
		t.Fatal("inspection changed approval", r, err)
	}
}

func TestApprovalExecutionCompletedEffects(t *testing.T) {
	for _, effect := range []runtime.Effect{runtime.NoEffect, runtime.ConfirmedEffect, runtime.UncertainEffect} {
		t.Run(string(effect), func(t *testing.T) {
			s, _, req, lease := executionApprovalFixture(t)
			completeApprovalCall(t, s, effect)
			if err := s.ReleaseLease(context.Background(), lease.Token, lease.Owner); err != nil {
				t.Fatal(err)
			}
			out, err := s.ApprovalExecutionStatus(context.Background(), req.TaskID, req.ID, req.CreatedAt.Add(3*time.Second))
			if err != nil || out.CallState != "completed" || out.RecordedEffect != string(effect) || out.ScopeWriterState != "none" || out.Sequence != 5 {
				t.Fatal(out, err)
			}
		})
	}
}

func TestApprovalExecutionPendingWithoutWriter(t *testing.T) {
	s, _, req := approvalFixture(t)
	ctx := context.Background()
	if _, err := s.RequestApproval(ctx, req); err != nil {
		t.Fatal(err)
	}
	out, err := s.ApprovalExecutionStatus(ctx, req.TaskID, req.ID, req.CreatedAt.Add(time.Second))
	if err != nil || out.Approval.State != approvals.Pending || out.CallState != "open" || out.ScopeWriterState != "none" || out.RecordedEffect != "" {
		t.Fatal("pending review observation changed state", out, err)
	}
}

func TestApprovalExecutionRejectsCorruptOrMismatchedState(t *testing.T) {
	for _, which := range []string{"task", "request_turn", "history", "head", "writer_duplicate", "writer_expiry", "writer_owner", "missing", "canceled"} {
		t.Run(which, func(t *testing.T) {
			s, _, req, lease := executionApprovalFixture(t)
			ctx := context.Background()
			task, id := req.TaskID, req.ID
			want := approvals.ErrInvalid
			switch which {
			case "task":
				task = "other"
				want = approvals.ErrConflict
			case "missing":
				id = "missing"
				want = approvals.ErrUnavailable
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = approvals.ErrUnavailable
			case "request_turn":
				r, err := s.ReadApproval(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				r.Request.TurnID = "wrong"
				body, _ := json.Marshal(r)
				if _, err = s.db.Exec(`UPDATE tool_approvals SET body=? WHERE id=?`, body, id); err != nil {
					t.Fatal(err)
				}
			case "history":
				completeApprovalCall(t, s, runtime.ConfirmedEffect)
				if _, err := s.db.Exec(`UPDATE events SET body=? WHERE task_id=? AND sequence=5`, []byte(`{"private":"bad"}`), task); err != nil {
					t.Fatal(err)
				}
			case "head":
				if _, err := s.db.Exec(`UPDATE task_heads SET sequence=99 WHERE task_id=?`, task); err != nil {
					t.Fatal(err)
				}
			case "writer_duplicate":
				if _, err := s.db.Exec(`INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires) VALUES(?,?,?,?,1,?)`, "second", task, "owner", req.Scope, lease.Expires.UnixNano()); err != nil {
					t.Fatal(err)
				}
			case "writer_expiry":
				if _, err := s.db.Exec(`UPDATE resource_leases SET expires=-1 WHERE token=?`, lease.Token); err != nil {
					t.Fatal(err)
				}
			case "writer_owner":
				if _, err := s.db.Exec(`UPDATE resource_leases SET owner=? WHERE token=?`, strings.Repeat("x", 513), lease.Token); err != nil {
					t.Fatal(err)
				}
			}
			out, err := s.ApprovalExecutionStatus(ctx, task, id, req.CreatedAt.Add(3*time.Second))
			if !errors.Is(err, want) || out.Version != 0 || err.Error() != want.Error() {
				t.Fatalf("invalid state admitted: %+v %v want=%v", out, err, want)
			}
		})
	}
}
