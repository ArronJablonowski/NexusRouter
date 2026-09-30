package telemetry

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func approvalFixture(t *testing.T) (*Store, string, approvals.Request) {
	t.Helper()
	s, path := leaseStore(t)
	ctx := context.Background()
	e := event("turn", 2, runtime.TurnStarted)
	e.TurnID = "turn"
	e.AttemptID = "attempt"
	if err := s.Append(ctx, 1, e); err != nil {
		t.Fatal(err)
	}
	e = event("turn_done", 3, runtime.TurnCompleted)
	e.TurnID = "turn"
	e.AttemptID = "attempt"
	e.Data.ToolCalls = []providers.ToolCall{{ID: "call", Name: "write_file", Arguments: []byte(`{}`)}}
	if err := s.Append(ctx, 2, e); err != nil {
		t.Fatal(err)
	}
	e = event("tool_start", 4, runtime.ToolStarted)
	e.TurnID = "turn"
	e.AttemptID = "attempt"
	e.Data = runtime.Data{ToolCallID: "call", ToolName: "write_file", Effect: runtime.UncertainEffect}
	if err := s.Append(ctx, 3, e); err != nil {
		t.Fatal(err)
	}
	req := approvals.Request{Version: 1, ID: "approval", TaskID: "task", TurnID: "turn", ToolCallID: "call", ToolName: "write_file", Scope: "workspace", ArgumentsDigest: strings.Repeat("a", 64), SchemaDigest: strings.Repeat("b", 64), PolicyDigest: strings.Repeat("c", 64), CreatedAt: time.Unix(100, 0).UTC(), ExpiresAt: time.Unix(200, 0).UTC()}
	return s, path, req
}

func TestApprovalConsumeOnceAcrossConnectionsAndRestart(t *testing.T) {
	ctx := context.Background()
	s, path, req := approvalFixture(t)
	r, err := s.RequestApproval(ctx, req)
	if err != nil || r.State != "pending" {
		t.Fatal(r, err)
	}
	d := approvals.Decision{ID: "decision", Actor: "operator", Allowed: true, Time: req.CreatedAt.Add(time.Second)}
	if _, err = s.DecideApproval(ctx, req.ID, d); err != nil {
		t.Fatal(err)
	}
	lease, err := s.AcquireLease(ctx, req.TaskID, "owner", req.Scope, true, req.CreatedAt, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	var wins atomic.Int32
	for _, db := range []*Store{s, other} {
		wg.Add(1)
		go func(db *Store) {
			defer wg.Done()
			r, e := db.ConsumeApproval(ctx, req, lease.Token, lease.Owner, req.CreatedAt.Add(2*time.Second))
			if e == nil {
				if r.State != "consumed" {
					t.Error(r)
				}
				wins.Add(1)
			} else if !errors.Is(e, approvals.ErrConflict) {
				t.Error(e)
			}
		}(db)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal(wins.Load())
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err = reopened.ConsumeApproval(ctx, req, lease.Token, lease.Owner, req.CreatedAt.Add(3*time.Second)); !errors.Is(err, approvals.ErrConflict) {
		t.Fatal(err)
	}
	r, err = reopened.DecideApproval(ctx, req.ID, d)
	if err != nil || r.State != "consumed" {
		t.Fatal(r, err)
	}
	r, err = reopened.RequestApproval(ctx, req)
	if err != nil || r.State != "consumed" {
		t.Fatal(r, err)
	}
}

func TestApprovalBindingsAndLeaseFences(t *testing.T) {
	for _, which := range []string{"arguments", "schema", "policy", "scope", "owner", "token", "reader", "expired_lease", "expired_approval", "canceled", "completed_call", "revoked"} {
		t.Run(which, func(t *testing.T) {
			ctx := context.Background()
			s, _, req := approvalFixture(t)
			if _, err := s.RequestApproval(ctx, req); err != nil {
				t.Fatal(err)
			}
			d := approvals.Decision{ID: "allow", Actor: "operator", Allowed: true, Time: req.CreatedAt.Add(time.Second)}
			if _, err := s.DecideApproval(ctx, req.ID, d); err != nil {
				t.Fatal(err)
			}
			lease, err := s.AcquireLease(ctx, req.TaskID, "owner", req.Scope, which != "reader", req.CreatedAt, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			now := req.CreatedAt.Add(2 * time.Second)
			switch which {
			case "arguments":
				req.ArgumentsDigest = strings.Repeat("d", 64)
			case "schema":
				req.SchemaDigest = strings.Repeat("d", 64)
			case "policy":
				req.PolicyDigest = strings.Repeat("d", 64)
			case "scope":
				req.Scope = "elsewhere"
			case "owner":
				lease.Owner = "other"
			case "token":
				lease.Token = "other"
			case "expired_lease":
				now = req.CreatedAt.Add(time.Minute)
			case "expired_approval":
				now = req.ExpiresAt
			case "canceled":
				if _, err = s.RequestCancellation(ctx, req.TaskID); err != nil {
					t.Fatal(err)
				}
			case "completed_call":
				e := event("tool_done", 5, runtime.ToolCompleted)
				e.TurnID = req.TurnID
				e.AttemptID = "attempt"
				e.Data = runtime.Data{ToolCallID: req.ToolCallID, ToolName: req.ToolName, Effect: runtime.NoEffect}
				if err = s.Append(ctx, 4, e); err != nil {
					t.Fatal(err)
				}
			case "revoked":
				d.ID = "deny"
				d.Allowed = false
				d.Time = now
				if _, err = s.DecideApproval(ctx, req.ID, d); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = s.ConsumeApproval(ctx, req, lease.Token, lease.Owner, now); !errors.Is(err, approvals.ErrConflict) {
				t.Fatal(err)
			}
			r, err := s.ReadApproval(ctx, req.ID)
			if err != nil || r.State == "consumed" {
				t.Fatal(r, err)
			}
		})
	}
}

func TestApprovalProposalAndCorruption(t *testing.T) {
	ctx := context.Background()
	s, _, req := approvalFixture(t)
	bad := req
	bad.ToolCallID = "other"
	if _, err := s.RequestApproval(ctx, bad); !errors.Is(err, approvals.ErrConflict) {
		t.Fatal(err)
	}
	if _, err := s.RequestApproval(ctx, req); err != nil {
		t.Fatal(err)
	}
	bad = req
	bad.ID = "second"
	if _, err := s.RequestApproval(ctx, bad); !errors.Is(err, approvals.ErrConflict) {
		t.Fatal(err)
	}
	bad = req
	bad.PolicyDigest = strings.Repeat("d", 64)
	if _, err := s.RequestApproval(ctx, bad); !errors.Is(err, approvals.ErrConflict) {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tool_approvals SET state='approved' WHERE id=?`, req.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadApproval(ctx, req.ID); !errors.Is(err, approvals.ErrInvalid) {
		t.Fatal(err)
	}
}

func TestApprovalConsumptionRollback(t *testing.T) {
	ctx := context.Background()
	s, _, req := approvalFixture(t)
	if _, err := s.RequestApproval(ctx, req); err != nil {
		t.Fatal(err)
	}
	d := approvals.Decision{ID: "allow", Actor: "operator", Allowed: true, Time: req.CreatedAt.Add(time.Second)}
	if _, err := s.DecideApproval(ctx, req.ID, d); err != nil {
		t.Fatal(err)
	}
	l, err := s.AcquireLease(ctx, req.TaskID, "owner", req.Scope, true, req.CreatedAt, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER reject_approval BEFORE UPDATE ON tool_approvals WHEN NEW.state='consumed' BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConsumeApproval(ctx, req, l.Token, l.Owner, req.CreatedAt.Add(2*time.Second)); !errors.Is(err, approvals.ErrUnavailable) {
		t.Fatal(err)
	}
	r, err := s.ReadApproval(ctx, req.ID)
	if err != nil || r.State != "approved" || r.ConsumedAt != nil {
		t.Fatal(r, err)
	}
}

func TestApprovalRejectsForgedPriorHistory(t *testing.T) {
	for _, mutation := range []string{
		`UPDATE events SET body=json_set(body,'$.data.tool_calls',json('[]')) WHERE sequence=3`,
		`UPDATE events SET body=json_set(body,'$.attempt_id','wrong') WHERE sequence=2`,
		`UPDATE events SET body=json_set(body,'$.padding',?) WHERE sequence=1`,
	} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			s, _, req := approvalFixture(t)
			var err error
			if strings.Contains(mutation, "?") {
				_, err = s.db.Exec(mutation, strings.Repeat("x", 8<<20))
			} else {
				_, err = s.db.Exec(mutation)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.RequestApproval(ctx, req); !errors.Is(err, approvals.ErrInvalid) {
				t.Fatal(err)
			}
		})
	}
}

func TestApprovalCommitFailureReturnsNoAuthority(t *testing.T) {
	ctx := context.Background()
	s, _, req := approvalFixture(t)
	if _, err := s.RequestApproval(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecideApproval(ctx, req.ID, approvals.Decision{ID: "allow", Actor: "operator", Allowed: true, Time: req.CreatedAt.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	l, err := s.AcquireLease(ctx, req.TaskID, "owner", req.Scope, true, req.CreatedAt, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER fail_approval_commit AFTER UPDATE ON tool_approvals WHEN NEW.state='consumed' BEGIN UPDATE tool_approvals SET task_id='missing_task' WHERE id=NEW.id; END; PRAGMA defer_foreign_keys=ON;`); err != nil {
		t.Fatal(err)
	}
	r, err := s.ConsumeApproval(ctx, req, l.Token, l.Owner, req.CreatedAt.Add(2*time.Second))
	if !errors.Is(err, approvals.ErrUnavailable) || r.Request.ID != "" || r.State != "" {
		t.Fatal(r, err)
	}
}

func TestApprovalRejectsInvalidConsumeClock(t *testing.T) {
	ctx := context.Background()
	s, _, req := approvalFixture(t)
	for _, now := range []time.Time{time.Time{}, time.Date(2261, 1, 1, 0, 0, 0, 0, time.UTC), time.Unix(101, 0).In(time.FixedZone("offset", 3600))} {
		if _, err := s.ConsumeApproval(ctx, req, "token", "owner", now); !errors.Is(err, approvals.ErrInvalid) {
			t.Fatal(now, err)
		}
	}
}

func TestApprovalRevocationConsumptionRace(t *testing.T) {
	ctx := context.Background()
	s, path, req := approvalFixture(t)
	if _, err := s.RequestApproval(ctx, req); err != nil {
		t.Fatal(err)
	}
	d := approvals.Decision{ID: "allow", Actor: "operator", Allowed: true, Time: req.CreatedAt.Add(time.Second)}
	if _, err := s.DecideApproval(ctx, req.ID, d); err != nil {
		t.Fatal(err)
	}
	l, err := s.AcquireLease(ctx, req.TaskID, "owner", req.Scope, true, req.CreatedAt, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	start := make(chan struct{})
	results := make(chan error, 2)
	now := req.CreatedAt.Add(2 * time.Second)
	go func() { <-start; _, e := s.ConsumeApproval(ctx, req, l.Token, l.Owner, now); results <- e }()
	go func() {
		<-start
		_, e := other.DecideApproval(ctx, req.ID, approvals.Decision{ID: "deny", Actor: "operator", Time: now})
		results <- e
	}()
	close(start)
	wins := 0
	for range 2 {
		e := <-results
		if e == nil {
			wins++
		} else if !errors.Is(e, approvals.ErrConflict) {
			t.Fatal(e)
		}
	}
	if wins != 1 {
		t.Fatal(wins)
	}
	r, err := s.ReadApproval(ctx, req.ID)
	if err != nil || (r.State != "consumed" && r.State != "revoked") {
		t.Fatal(r, err)
	}
}
