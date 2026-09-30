package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/approvals"
)

func insertListApproval(t *testing.T, s *Store, req approvals.Request, id, call string) {
	t.Helper()
	req.ID = id
	req.ToolCallID = call
	r := approvals.Record{Request: req, State: approvals.Pending}
	body, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO tool_approvals(id,task_id,tool_call_id,state,body) VALUES(?,?,?,?,?)`, id, req.TaskID, call, r.State, body); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalListPaginationReadOnlyAndNoMutation(t *testing.T) {
	s, path, req := approvalFixture(t)
	for _, call := range []string{"z", "a", "m"} {
		insertListApproval(t, s, req, "approval_"+call, call)
	}
	reader, err := OpenReadOnly(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	q := approvals.ListOptions{TaskID: req.TaskID, Limit: 2}
	first, err := reader.ListApprovals(context.Background(), q)
	if err != nil || first.Validate() != nil || len(first.Records) != 2 || first.Records[0].Request.ToolCallID != "a" || first.Records[1].Request.ToolCallID != "m" || first.NextAfterCallID != "m" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	// A cursor is not a snapshot: a later row is visible on the next page.
	insertListApproval(t, s, req, "approval_y", "y")
	q.AfterCallID = first.NextAfterCallID
	second, err := reader.ListApprovals(context.Background(), q)
	if err != nil || len(second.Records) != 2 || second.Records[0].Request.ToolCallID != "y" || second.Records[1].Request.ToolCallID != "z" || second.NextAfterCallID != "" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	q.AfterCallID = "z"
	empty, err := reader.ListApprovals(context.Background(), q)
	if err != nil || len(empty.Records) != 0 || empty.NextAfterCallID != "" {
		t.Fatal(empty, err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM tool_approvals WHERE state='pending'`).Scan(&count); err != nil || count != 4 {
		t.Fatal("listing mutated approvals", count, err)
	}
	var sequence int
	if err := s.db.QueryRow(`SELECT sequence FROM task_heads WHERE task_id=?`, req.TaskID).Scan(&sequence); err != nil || sequence != 4 {
		t.Fatal("listing mutated task", sequence, err)
	}
	first.Records[0].Request.ToolName = "changed"
	again, err := reader.ListApprovals(context.Background(), approvals.ListOptions{TaskID: req.TaskID, Limit: 1})
	if err != nil || again.Records[0].Request.ToolName == "changed" {
		t.Fatal("page mutated storage", err)
	}
}

func TestApprovalListInvalidMissingCanceledAndEmpty(t *testing.T) {
	s, _, req := approvalFixture(t)
	q := approvals.ListOptions{TaskID: req.TaskID, Limit: 1}
	p, err := s.ListApprovals(context.Background(), q)
	if err != nil || p.Validate() != nil || len(p.Records) != 0 {
		t.Fatal(p, err)
	}
	q.TaskID = "missing"
	p, err = s.ListApprovals(context.Background(), q)
	if !errors.Is(err, approvals.ErrUnavailable) || p.Version != 0 {
		t.Fatal(p, err)
	}
	q.TaskID = req.TaskID
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p, err = s.ListApprovals(ctx, q)
	if !errors.Is(err, approvals.ErrUnavailable) || p.Version != 0 {
		t.Fatal(p, err)
	}
	q.Limit = 101
	p, err = s.ListApprovals(context.Background(), q)
	if !errors.Is(err, approvals.ErrInvalid) || p.Version != 0 {
		t.Fatal(p, err)
	}
}

func TestApprovalListCorruptRowFailsWholePage(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		value     any
	}{
		{"body oversized", `UPDATE tool_approvals SET body=? WHERE tool_call_id='z'`, strings.Repeat("x", 65537)},
		{"body malformed", `UPDATE tool_approvals SET body=? WHERE tool_call_id='z'`, `{"private":"secret"}`},
		{"id mismatch", `UPDATE tool_approvals SET id=? WHERE tool_call_id='z'`, "different"},
		{"id oversized", `UPDATE tool_approvals SET id=? WHERE tool_call_id='z'`, strings.Repeat("x", 129)},
		{"call mismatch", `UPDATE tool_approvals SET tool_call_id=? WHERE tool_call_id='z'`, "zz"},
		{"empty call", `UPDATE tool_approvals SET tool_call_id=? WHERE tool_call_id='z'`, ""},
		{"call oversized", `UPDATE tool_approvals SET tool_call_id=? WHERE tool_call_id='z'`, strings.Repeat("z", 129)},
		{"state mismatch", `UPDATE tool_approvals SET state=? WHERE tool_call_id='z'`, approvals.Consumed},
		{"state oversized", `UPDATE tool_approvals SET state=? WHERE tool_call_id='z'`, strings.Repeat("x", 17)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, req := approvalFixture(t)
			insertListApproval(t, s, req, "a", "a")
			insertListApproval(t, s, req, "z", "z")
			if _, err := s.db.Exec(tc.sql, tc.value); err != nil {
				t.Fatal(err)
			}
			// Corruption fails even in lookahead; an empty call sorts first and
			// must not be skipped by the initial page's range predicate.
			p, err := s.ListApprovals(context.Background(), approvals.ListOptions{TaskID: req.TaskID, Limit: 1})
			if !errors.Is(err, approvals.ErrInvalid) || p.Version != 0 || p.Records != nil {
				t.Fatalf("partial corrupt page: %+v %v", p, err)
			}
		})
	}
}
