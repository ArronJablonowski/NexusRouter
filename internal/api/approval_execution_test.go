package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/approvals"
)

func executionStatusFixture() approvals.ExecutionStatus {
	return approvals.ExecutionStatus{Version: 1, Approval: approvalRecordFixture(), TaskState: "running", Sequence: 4, CallState: "open", ScopeWriterState: "none", ObservedAt: time.Now().UTC()}
}

func TestApprovalExecutionAPIValidAndIndependentCapacity(t *testing.T) {
	s := services()
	calls := 0
	s.ApprovalExecution = func(ctx context.Context, task, id string) (approvals.ExecutionStatus, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if task != "task" || id != "approval" || !ok || time.Until(deadline) > 5*time.Second {
			t.Fatal("wrong/unbounded request", task, id)
		}
		return executionStatusFixture(), nil
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	h.slots <- struct{}{}
	h.controls <- struct{}{}
	h.controls <- struct{}{}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("GET", "/v1/tasks/task/approvals/approval/execution", ""))
	var out approvals.ExecutionStatus
	if w.Code != 200 || calls != 1 || w.Header().Get("Cache-Control") != "no-store" || len(h.approvalSlots) != 0 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Validate() != nil {
		t.Fatal(w.Code, w.Body.String(), calls)
	}
}

func TestApprovalExecutionAPIAdmission(t *testing.T) {
	const endpoint = "/v1/tasks/task/approvals/approval/execution"
	for _, tc := range []struct {
		name, method, path, body string
		want                     int
		mutate                   func(*Handler, *http.Request)
	}{
		{"auth", "GET", endpoint, "", 401, func(_ *Handler, r *http.Request) { r.Header.Del("Authorization") }},
		{"origin", "GET", endpoint, "", 403, func(_ *Handler, r *http.Request) { r.Header.Set("Origin", "https://invalid.test") }},
		{"method", "POST", endpoint, "", 404, nil},
		{"task", "GET", "/v1/tasks/bad:task/approvals/approval/execution", "", 404, nil},
		{"id", "GET", "/v1/tasks/task/approvals/bad:id/execution", "", 404, nil},
		{"suffix", "GET", endpoint + "/extra", "", 404, nil},
		{"query", "GET", endpoint + "?x=private", "", 400, nil},
		{"emptyquery", "GET", endpoint + "?", "", 400, nil},
		{"body", "GET", endpoint, "{}", 400, nil},
		{"transfer", "GET", endpoint, "", 400, func(_ *Handler, r *http.Request) {
			r.TransferEncoding = []string{"chunked"}
			r.Body = metricsUnreadBody{t}
		}},
		{"unavailable", "GET", endpoint, "", 503, func(h *Handler, _ *http.Request) { h.services.ApprovalExecution = nil }},
		{"capacity", "GET", endpoint, "", 503, func(h *Handler, _ *http.Request) { h.approvalSlots <- struct{}{}; h.approvalSlots <- struct{}{} }},
		{"canceled", "GET", endpoint, "", 503, func(_ *Handler, r *http.Request) {
			ctx, cancel := context.WithCancel(r.Context())
			cancel()
			*r = *r.WithContext(ctx)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := services()
			calls := 0
			s.ApprovalExecution = func(context.Context, string, string) (approvals.ExecutionStatus, error) {
				calls++
				return executionStatusFixture(), nil
			}
			h, err := New(token, 1, s)
			if err != nil {
				t.Fatal(err)
			}
			r := request(tc.method, tc.path, tc.body)
			if tc.mutate != nil {
				tc.mutate(h, r)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want || calls != 0 {
				t.Fatal(w.Code, w.Body.String(), calls)
			}
		})
	}
}

func TestApprovalExecutionAPIRejectsInvalidServicePayload(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*approvals.ExecutionStatus)
		err    error
		want   int
	}{
		{"task", func(s *approvals.ExecutionStatus) { s.Approval.Request.TaskID = "other" }, nil, 500},
		{"approval", func(s *approvals.ExecutionStatus) { s.Approval.Request.ID = "other" }, nil, 500},
		{"version", func(s *approvals.ExecutionStatus) { s.Version = 2 }, nil, 500},
		{"call", func(s *approvals.ExecutionStatus) { s.CallState = "retryable" }, nil, 500},
		{"effect", func(s *approvals.ExecutionStatus) { s.RecordedEffect = "confirmed" }, nil, 500},
		{"writer", func(s *approvals.ExecutionStatus) { s.ScopeWriterState = "private-token" }, nil, 500},
		{"sequence", func(s *approvals.ExecutionStatus) { s.Sequence = 0 }, nil, 500},
		{"clock", func(s *approvals.ExecutionStatus) { s.ObservedAt = time.Time{} }, nil, 500},
		{"backend", nil, errors.New("private storage failure"), 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := services()
			s.ApprovalExecution = func(context.Context, string, string) (approvals.ExecutionStatus, error) {
				out := executionStatusFixture()
				if tc.mutate != nil {
					tc.mutate(&out)
				}
				return out, tc.err
			}
			h, err := New(token, 1, s)
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("GET", "/v1/tasks/task/approvals/approval/execution", ""))
			if w.Code != tc.want || strings.Contains(w.Body.String(), "private") || len(h.approvalSlots) != 0 {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}
