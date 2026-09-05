package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
)

func approvalRecordFixture() approvals.Record {
	now := time.Now().UTC()
	return approvals.Record{Request: approvals.Request{Version: 1, ID: "approval", TaskID: "task", TurnID: "turn", ToolCallID: "call", ToolName: "write_file", Scope: "workspace", ArgumentsDigest: strings.Repeat("a", 64), SchemaDigest: strings.Repeat("b", 64), PolicyDigest: strings.Repeat("c", 64), CreatedAt: now, ExpiresAt: now.Add(time.Minute)}, State: approvals.Pending}
}

func TestApprovalAPIListDetailAndIndependentCapacity(t *testing.T) {
	for _, path := range []string{"/v1/tasks/task/approvals", "/v1/tasks/task/approvals?after=before&limit=2", "/v1/tasks/task/approvals/approval"} {
		t.Run(path, func(t *testing.T) {
			s := services()
			calls := 0
			check := func(ctx context.Context) {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 5*time.Second {
					t.Error("unbounded approval inspection")
				}
			}
			s.Approval = func(ctx context.Context, task, id string) (approvals.Record, error) {
				check(ctx)
				if task != "task" || id != "approval" {
					t.Error(task, id)
				}
				return approvalRecordFixture(), nil
			}
			s.Approvals = func(ctx context.Context, q approvals.ListOptions) (approvals.Page, error) {
				check(ctx)
				want := approvals.ListOptions{TaskID: "task", Limit: 25}
				if strings.Contains(path, "?") {
					want.AfterCallID, want.Limit = "before", 2
				}
				if q != want {
					t.Error(q, want)
				}
				return approvals.Page{Version: 1, Query: q, Records: []approvals.Record{approvalRecordFixture()}}, nil
			}
			h, _ := New(token, 1, s)
			h.slots <- struct{}{}
			h.controls <- struct{}{}
			h.controls <- struct{}{}
			h.steeringSlots <- struct{}{}
			h.steeringSlots <- struct{}{}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("GET", path, ""))
			if w.Code != 200 || calls != 1 || w.Header().Get("Cache-Control") != "no-store" || len(h.approvalSlots) != 0 {
				t.Fatal(w.Code, w.Body.String(), calls)
			}
		})
	}
}

func TestApprovalAPIInvalidAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		status                   int
		mutate                   func(*Handler, *http.Request)
	}{
		{"auth", "GET", "/v1/tasks/task/approvals", "", 401, func(_ *Handler, r *http.Request) { r.Header.Del("Authorization") }},
		{"origin", "GET", "/v1/tasks/task/approvals", "", 403, func(_ *Handler, r *http.Request) { r.Header.Set("Origin", "https://invalid.test") }},
		{"task", "GET", "/v1/tasks//approvals", "", 404, nil},
		{"id", "GET", "/v1/tasks/task/approvals/bad:id", "", 404, nil},
		{"nested", "GET", "/v1/tasks/task/approvals/a/b", "", 404, nil},
		{"trailing", "GET", "/v1/tasks/task/approvals/", "", 404, nil},
		{"suffix", "GET", "/v1/tasks/task/approvals-extra", "", 404, nil},
		{"method", "POST", "/v1/tasks/task/approvals", "", 404, nil},
		{"body", "GET", "/v1/tasks/task/approvals", "{}", 400, nil},
		{"transfer", "GET", "/v1/tasks/task/approvals", "", 400, func(_ *Handler, r *http.Request) {
			r.TransferEncoding = []string{"chunked"}
			r.Body = metricsUnreadBody{t}
		}},
		{"duplicate", "GET", "/v1/tasks/task/approvals?limit=2&limit=3", "", 400, nil},
		{"unknown", "GET", "/v1/tasks/task/approvals?unknown=secret", "", 400, nil},
		{"zero", "GET", "/v1/tasks/task/approvals?limit=0", "", 400, nil},
		{"limit", "GET", "/v1/tasks/task/approvals?limit=999", "", 400, nil},
		{"numeric", "GET", "/v1/tasks/task/approvals?limit=01", "", 400, nil},
		{"after", "GET", "/v1/tasks/task/approvals?after=bad:id", "", 400, nil},
		{"emptyafter", "GET", "/v1/tasks/task/approvals?after=", "", 400, nil},
		{"encoding", "GET", "/v1/tasks/task/approvals?after=%ff", "", 400, nil},
		{"detailquery", "GET", "/v1/tasks/task/approvals/approval?limit=2", "", 400, nil},
		{"detailemptyquery", "GET", "/v1/tasks/task/approvals/approval?", "", 400, nil},
		{"nil_list", "GET", "/v1/tasks/task/approvals", "", 503, func(h *Handler, _ *http.Request) { h.services.Approvals = nil }},
		{"nil_detail", "GET", "/v1/tasks/task/approvals/approval", "", 503, func(h *Handler, _ *http.Request) { h.services.Approval = nil }},
		{"capacity", "GET", "/v1/tasks/task/approvals", "", 503, func(h *Handler, _ *http.Request) { h.approvalSlots <- struct{}{}; h.approvalSlots <- struct{}{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := services()
			s.Approval = func(context.Context, string, string) (approvals.Record, error) {
				t.Error("invalid request dispatched")
				return approvals.Record{}, nil
			}
			s.Approvals = func(context.Context, approvals.ListOptions) (approvals.Page, error) {
				t.Error("invalid request dispatched")
				return approvals.Page{}, nil
			}
			h, _ := New(token, 1, s)
			r := request(tc.method, tc.path, tc.body)
			if tc.mutate != nil {
				tc.mutate(h, r)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status || strings.Contains(w.Body.String(), "secret") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestApprovalAPIRejectsUnboundOrInvalidPayload(t *testing.T) {
	for _, which := range []string{"detail_task", "detail_id", "detail_invalid", "page_task", "page_limit", "page_after", "page_invalid", "record_task", "duplicate", "cursor"} {
		t.Run(which, func(t *testing.T) {
			s := services()
			path := "/v1/tasks/task/approvals"
			s.Approval = func(context.Context, string, string) (approvals.Record, error) {
				r := approvalRecordFixture()
				switch which {
				case "detail_task":
					r.Request.TaskID = "other"
				case "detail_id":
					r.Request.ID = "other"
				case "detail_invalid":
					r.State = "bad"
				}
				return r, nil
			}
			s.Approvals = func(_ context.Context, q approvals.ListOptions) (approvals.Page, error) {
				p := approvals.Page{Version: 1, Query: q, Records: []approvals.Record{approvalRecordFixture()}}
				switch which {
				case "page_task":
					p.Query.TaskID = "other"
				case "page_limit":
					p.Query.Limit = 1
				case "page_after":
					p.Query.AfterCallID = "before"
				case "page_invalid":
					p.Version = 99
				case "record_task":
					p.Records[0].Request.TaskID = "other"
				case "duplicate":
					p.Records = append(p.Records, p.Records[0])
				case "cursor":
					p.NextAfterCallID = "bad:id"
				}
				return p, nil
			}
			if strings.HasPrefix(which, "detail_") {
				path += "/approval"
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("GET", path, ""))
			if w.Code != 500 || strings.Contains(w.Body.String(), "workspace") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestApprovalAPISanitizesBackendFailures(t *testing.T) {
	for _, detail := range []bool{false, true} {
		for _, kind := range []string{"error", "missing", "panic", "canceled"} {
			t.Run(kind, func(t *testing.T) {
				s := services()
				err := errors.New("secret backend path")
				want := 503
				if kind == "missing" {
					err = sql.ErrNoRows
					want = 404
				}
				if kind == "panic" {
					want = 500
				}
				s.Approval = func(context.Context, string, string) (approvals.Record, error) {
					if kind == "panic" {
						panic("secret")
					}
					return approvals.Record{}, err
				}
				s.Approvals = func(context.Context, approvals.ListOptions) (approvals.Page, error) {
					if kind == "panic" {
						panic("secret")
					}
					return approvals.Page{}, err
				}
				path := "/v1/tasks/task/approvals"
				if detail {
					path += "/approval"
				}
				r := request("GET", path, "")
				if kind == "canceled" {
					ctx, cancel := context.WithCancel(r.Context())
					cancel()
					r = r.WithContext(ctx)
				}
				h, _ := New(token, 1, s)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != want || strings.Contains(w.Body.String(), "secret") || len(h.approvalSlots) != 0 {
					t.Fatal(w.Code, w.Body.String())
				}
			})
		}
	}
}
