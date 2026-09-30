package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/approvals"
)

func insertObservedLease(t *testing.T, s *Store, token, task, scope string, writer int, expires any) {
	t.Helper()
	_, err := s.db.Exec(`INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires) VALUES(?,?,?,?,?,?)`, token, task, "private-owner", scope, writer, expires)
	if err != nil {
		t.Fatal(err)
	}
}

func TestScopeLeaseObservationReadOnlyAliases(t *testing.T) {
	s, path, req := approvalFixture(t)
	req.Scope = "workspace"
	ctx := context.Background()
	if _, err := s.RequestApproval(ctx, req); err != nil {
		t.Fatal(err)
	}
	now := req.CreatedAt.Add(time.Second)
	for i, row := range []struct {
		scope  string
		writer int
		expiry time.Time
	}{
		{"create_a", 1, now.Add(time.Hour)}, {"create_b", 1, now},
		{"create_c", 0, now.Add(time.Hour)}, {"workspace", 0, now},
		{"unrelated", 1, now.Add(time.Hour)},
	} {
		insertObservedLease(t, s, fmt.Sprintf("private-token-%d", i), req.TaskID, row.scope, row.writer, row.expiry.UnixNano())
	}
	reader, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for i := 0; i < 2; i++ {
		out, err := reader.ApprovalExecutionStatus(ctx, req.TaskID, req.ID, now)
		want := approvals.ScopeLeaseObservation{Version: 1, OverlapPolicyVersion: 1, LiveReaders: 1, ExpiredReaders: 1, LiveWriters: 1, ExpiredWriters: 1}
		if err != nil || out.ScopeLeases == nil || *out.ScopeLeases != want || out.ScopeWriterState != "none" || out.Validate() != nil {
			t.Fatalf("status=%+v err=%v", out, err)
		}
		body, _ := json.Marshal(out.ScopeLeases)
		for _, secret := range []string{"private", "create_", req.TaskID} {
			if strings.Contains(string(body), secret) {
				t.Fatal("identity escaped", string(body))
			}
		}
	}
	var released, count int
	if err := s.db.QueryRow(`SELECT count(*),sum(released) FROM resource_leases`).Scan(&count, &released); err != nil || count != 5 || released != 0 {
		t.Fatal(count, released, err)
	}
	r, err := s.ReadApproval(ctx, req.ID)
	if err != nil || r.State != approvals.Pending {
		t.Fatal(r, err)
	}
}

func TestScopeLeaseObservationRejectsMalformedOrOverflow(t *testing.T) {
	for _, mode := range []string{"duplicate_writer", "bad_expiry", "bad_owner", "bad_utf8", "oversized", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			s, _, req := approvalFixture(t)
			req.Scope = "workspace"
			ctx := context.Background()
			if _, err := s.RequestApproval(ctx, req); err != nil {
				t.Fatal(err)
			}
			now := req.CreatedAt.Add(time.Second)
			insertObservedLease(t, s, "private-token", req.TaskID, "create_a", 1, now.Add(time.Hour).UnixNano())
			switch mode {
			case "duplicate_writer":
				insertObservedLease(t, s, "private-token-2", req.TaskID, "create_a", 1, now.UnixNano())
			case "bad_expiry":
				_, _ = s.db.Exec(`UPDATE resource_leases SET expires='bad'`)
			case "bad_owner":
				_, _ = s.db.Exec("UPDATE resource_leases SET owner=?", "bad\nowner")
			case "bad_utf8":
				_, _ = s.db.Exec("UPDATE resource_leases SET owner=?", string([]byte{255}))
			case "oversized":
				_, _ = s.db.Exec("UPDATE resource_leases SET owner=?", strings.Repeat("x", 513))
			case "overflow":
				for i := 0; i < 1000; i++ {
					insertObservedLease(t, s, fmt.Sprint("extra-", i), req.TaskID, "create_b", 0, now.UnixNano())
				}
			}
			out, err := s.ApprovalExecutionStatus(ctx, req.TaskID, req.ID, now)
			if err != approvals.ErrInvalid || out.Version != 0 || out.ScopeLeases != nil {
				t.Fatal("partial or accepted observation", out, err)
			}
		})
	}
}

func TestScopeLeaseObservationGenericExactAndCancellation(t *testing.T) {
	s, _, req := approvalFixture(t)
	now := req.CreatedAt.Add(time.Second)
	insertObservedLease(t, s, "private-token", req.TaskID, "create_a", 1, now.UnixNano())
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	out, err := observeScopeLeases(context.Background(), tx, "other", now)
	if err != nil || *out != (approvals.ScopeLeaseObservation{Version: 1, OverlapPolicyVersion: 1}) {
		t.Fatal(out, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, err := observeScopeLeases(ctx, tx, "workspace", now); err == nil || out != nil {
		t.Fatal(out, err)
	}
}

func TestScopeLeaseObservationAcceptsExactHolderLimit(t *testing.T) {
	s, _, req := approvalFixture(t)
	req.Scope = "workspace"
	ctx := context.Background()
	if _, err := s.RequestApproval(ctx, req); err != nil {
		t.Fatal(err)
	}
	now := req.CreatedAt.Add(time.Second)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < 1000; i++ {
		expires := now
		if i%2 == 0 {
			expires = expires.Add(time.Minute)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires) VALUES(?,?,?,?,0,?)`, fmt.Sprint("reader-", i), req.TaskID, "holder", "create_legacy", expires.UnixNano()); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	out, err := s.ApprovalExecutionStatus(ctx, req.TaskID, req.ID, now)
	want := approvals.ScopeLeaseObservation{Version: 1, OverlapPolicyVersion: 1, LiveReaders: 500, ExpiredReaders: 500}
	if err != nil || out.ScopeLeases == nil || *out.ScopeLeases != want {
		t.Fatal("exact limit rejected", out, err)
	}
}
