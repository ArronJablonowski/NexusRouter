package telemetry

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/approvals"
)

func TestBoundApprovalFreshClockRetryAfterConsumption(t *testing.T) {
	ctx := context.Background()
	s, _, req := approvalFixture(t)
	if _, err := s.RequestApproval(ctx, req); err != nil {
		t.Fatal(err)
	}
	command := approvals.Command{Expected: req, ID: "decision", Allowed: true}
	when := req.CreatedAt.Add(time.Second)
	if _, err := s.DecideBoundApproval(ctx, command, "operator", when); err != nil {
		t.Fatal(err)
	}
	lease, err := s.AcquireLease(ctx, req.TaskID, "owner", req.Scope, true, when, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConsumeApproval(ctx, req, lease.Token, lease.Owner, when.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	r, err := s.DecideBoundApproval(ctx, command, "operator", req.ExpiresAt.Add(time.Hour))
	if err != nil || r.State != approvals.Consumed || len(r.Decisions) != 1 || !r.Decisions[0].Time.Equal(when) {
		t.Fatalf("retry resurrected authority: %+v %v", r, err)
	}
	command.ID = "newdecision"
	if _, err = s.DecideBoundApproval(ctx, command, "operator", when.Add(2*time.Second)); !errors.Is(err, approvals.ErrConflict) {
		t.Fatal("consumed reused", err)
	}
}

func TestBoundApprovalBindingCheckedBeforeIdempotency(t *testing.T) {
	for _, mutate := range []func(*approvals.Request){
		func(r *approvals.Request) { r.TaskID = "other" }, func(r *approvals.Request) { r.TurnID = "other" }, func(r *approvals.Request) { r.ToolCallID = "other" }, func(r *approvals.Request) { r.ToolName = "other" }, func(r *approvals.Request) { r.Scope = "other" }, func(r *approvals.Request) { r.ArgumentsDigest = strings.Repeat("d", 64) }, func(r *approvals.Request) { r.SchemaDigest = strings.Repeat("d", 64) }, func(r *approvals.Request) { r.PolicyDigest = strings.Repeat("d", 64) }, func(r *approvals.Request) { r.CreatedAt = r.CreatedAt.Add(time.Second) }, func(r *approvals.Request) { r.ExpiresAt = r.ExpiresAt.Add(time.Second) },
	} {
		s, _, req := approvalFixture(t)
		ctx := context.Background()
		if _, err := s.RequestApproval(ctx, req); err != nil {
			t.Fatal(err)
		}
		c := approvals.Command{Expected: req, ID: "decision", Allowed: true}
		when := req.CreatedAt.Add(2 * time.Second)
		if _, err := s.DecideBoundApproval(ctx, c, "operator", when); err != nil {
			t.Fatal(err)
		}
		mutate(&c.Expected)
		if r, err := s.DecideBoundApproval(ctx, c, "operator", when); !errors.Is(err, approvals.ErrConflict) || r.State != "" {
			t.Fatal("stale binding admitted", r, err)
		}
	}
	for _, which := range []string{"actor", "action"} {
		t.Run(which, func(t *testing.T) {
			s, _, req := approvalFixture(t)
			ctx := context.Background()
			if _, err := s.RequestApproval(ctx, req); err != nil {
				t.Fatal(err)
			}
			c := approvals.Command{Expected: req, ID: "decision", Allowed: true}
			when := req.CreatedAt.Add(time.Second)
			if _, err := s.DecideBoundApproval(ctx, c, "operator", when); err != nil {
				t.Fatal(err)
			}
			actor := "operator"
			if which == "actor" {
				actor = "other"
			} else {
				c.Allowed = false
			}
			if _, err := s.DecideBoundApproval(ctx, c, actor, when); !errors.Is(err, approvals.ErrConflict) {
				t.Fatal("changed decision admitted", err)
			}
		})
	}
}

func TestBoundApprovalExpiryCancellationAndValidation(t *testing.T) {
	for _, which := range []string{"expired", "early", "canceled", "invalidclock", "invalidactor", "missing", "closed"} {
		t.Run(which, func(t *testing.T) {
			s, _, req := approvalFixture(t)
			ctx := context.Background()
			if _, err := s.RequestApproval(ctx, req); err != nil {
				t.Fatal(err)
			}
			c := approvals.Command{Expected: req, ID: "decision", Allowed: true}
			when := req.CreatedAt.Add(time.Second)
			actor := "operator"
			want := approvals.ErrConflict
			switch which {
			case "expired":
				when = req.ExpiresAt
			case "early":
				when = req.CreatedAt.Add(-time.Second)
			case "canceled":
				if _, err := s.RequestCancellation(ctx, req.TaskID); err != nil {
					t.Fatal(err)
				}
			case "invalidclock":
				when = when.In(time.FixedZone("east", 3600))
				want = approvals.ErrInvalid
			case "invalidactor":
				actor = "private\ncredential"
				want = approvals.ErrInvalid
			case "missing":
				c.Expected.ID = "missing"
				want = approvals.ErrUnavailable
			case "closed":
				s.Close()
				want = approvals.ErrUnavailable
			}
			r, err := s.DecideBoundApproval(ctx, c, actor, when)
			if !errors.Is(err, want) || err.Error() != want.Error() || r.State != "" {
				t.Fatalf("unsafe error: %+v %v want %v", r, err, want)
			}
		})
	}
}

func TestBoundApprovalConcurrentApproveAndRevoke(t *testing.T) {
	s, path, req := approvalFixture(t)
	ctx := context.Background()
	if _, err := s.RequestApproval(ctx, req); err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	for _, phase := range []struct {
		id      string
		allowed bool
		state   string
	}{{"approve", true, approvals.Approved}, {"revoke", false, approvals.Revoked}} {
		var group sync.WaitGroup
		results := make(chan error, 2)
		for _, db := range []*Store{s, other} {
			group.Add(1)
			go func(db *Store) {
				defer group.Done()
				r, err := db.DecideBoundApproval(ctx, approvals.Command{Expected: req, ID: phase.id, Allowed: phase.allowed}, "operator", req.CreatedAt.Add(2*time.Second))
				if err == nil && r.State != phase.state {
					err = errors.New("incorrect current state")
				}
				results <- err
			}(db)
		}
		group.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	r, err := s.ReadApproval(ctx, req.ID)
	if err != nil || r.State != approvals.Revoked || len(r.Decisions) != 2 {
		t.Fatal(r, err)
	}
	r, err = s.DecideBoundApproval(ctx, approvals.Command{Expected: req, ID: "approve", Allowed: true}, "operator", req.ExpiresAt.Add(time.Hour))
	if err != nil || r.State != approvals.Revoked {
		t.Fatal("retry undid revocation", r, err)
	}
}

func TestBoundApprovalOpposingCommandsSerialize(t *testing.T) {
	s, path, req := approvalFixture(t)
	ctx := context.Background()
	if _, err := s.RequestApproval(ctx, req); err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, db := range []*Store{s, other} {
		go func(i int, db *Store) {
			<-start
			command := approvals.Command{Expected: req, ID: "deny", Allowed: false}
			if i == 0 {
				command.ID, command.Allowed = "allow", true
			}
			_, err := db.DecideBoundApproval(ctx, command, "operator", req.CreatedAt.Add(time.Second))
			results <- err
		}(i, db)
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil && !errors.Is(err, approvals.ErrConflict) {
			t.Fatal(err)
		}
	}
	r, err := s.ReadApproval(ctx, req.ID)
	if err != nil || r.Validate() != nil || (r.State != approvals.Denied && r.State != approvals.Revoked) {
		t.Fatal("opposing decisions failed to serialize", r, err)
	}
}
