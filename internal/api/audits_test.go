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

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
)

const (
	auditTask      = "task-audit"
	auditOperation = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	auditKey       = "audit-key-0123456789"
)

func auditPending() evaluation.AuditStatus {
	started := time.Unix(100, 0).UTC()
	return evaluation.AuditStatus{
		Version: 1, ID: auditOperation, TaskID: auditTask, SourceAttemptID: "attempt",
		ReviewerID: "reviewer", EvaluatorModel: "review-model", EvaluatorProvider: "provider",
		Status: "pending", Findings: []evaluation.AuditFinding{}, EvidenceRefs: []string{},
		EvidencePrecedence: evaluation.AuditEvidencePrecedence(), StartedAt: &started,
	}
}

func auditCanceled() evaluation.AuditStatus {
	status := auditPending()
	finished := status.StartedAt.Add(time.Second)
	status.Status, status.TerminalDisposition, status.ErrorCode = "canceled", "canceled", "canceled"
	status.FinishedAt = &finished
	return status
}

func auditCompleted(summary string) evaluation.AuditStatus {
	status := auditPending()
	finished := status.StartedAt.Add(time.Second)
	status.Status, status.TerminalDisposition = "completed", "completed"
	status.AuditID, status.RubricVersion, status.Domain = "audit-record", "rubric", "code"
	status.Findings = []evaluation.AuditFinding{{Summary: summary, EvidenceRefs: []string{"candidate"}}}
	status.EvidenceRefs = []string{"candidate"}
	status.FinishedAt = &finished
	return status
}

func auditEvent(status evaluation.AuditStatus, sequence int64) evaluation.AuditEvent {
	return evaluation.AuditEvent{Version: 1, AuditID: status.ID, Sequence: sequence, Status: status}
}

func auditRequest(method, path, body string) *http.Request {
	r := request(method, path, body)
	r.Header.Set("Idempotency-Key", auditKey)
	return r
}

func TestAuditRunStreamsBoundRequestAndDurableLifecycle(t *testing.T) {
	s := services()
	called := 0
	s.RunAudit = func(_ context.Context, req evaluation.AuditRequest, emit func(evaluation.AuditEvent) error) (evaluation.AuditStatus, error) {
		called++
		if req.Version != 1 || req.TaskID != auditTask || req.IdempotencyKey != auditKey || req.ReviewerModelID != "reviewer" || req.MaxCost != .25 {
			t.Fatal("request was not bound to transport", req)
		}
		pending, terminal := auditPending(), auditCanceled()
		if err := emit(auditEvent(pending, 1)); err != nil {
			return evaluation.AuditStatus{}, err
		}
		if err := emit(auditEvent(terminal, 2)); err != nil {
			return evaluation.AuditStatus{}, err
		}
		return terminal, errors.New("private provider failure with super-secret")
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, auditRequest(http.MethodPost, "/v1/tasks/"+auditTask+"/audits", `{"reviewer_model_id":"reviewer","max_cost":0.25}`))
	body := w.Body.String()
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/event-stream" || called != 1 {
		t.Fatal(w.Code, w.Header(), called, body)
	}
	for _, want := range []string{"event: audit", "id: " + auditOperation + ":1", "id: " + auditOperation + ":2", `"status":"pending"`, `"status":"canceled"`} {
		if !strings.Contains(body, want) {
			t.Fatal("missing", want, body)
		}
	}
	for _, forbidden := range []string{auditKey, "private provider", "super-secret", "event: error"} {
		if strings.Contains(body, forbidden) {
			t.Fatal("sensitive or redundant output", forbidden, body)
		}
	}
}

func TestAuditRunRejectsInvalidTransportWithoutDispatch(t *testing.T) {
	valid := `{"reviewer_model_id":"reviewer","max_cost":1}`
	for _, tc := range []struct {
		name, path, body string
		status           int
		mutate           func(*http.Request)
	}{
		{"auth", "/v1/tasks/" + auditTask + "/audits", valid, 401, func(r *http.Request) { r.Header.Del("Authorization") }},
		{"origin", "/v1/tasks/" + auditTask + "/audits", valid, 403, func(r *http.Request) { r.Header.Set("Origin", "https://attacker.invalid") }},
		{"query", "/v1/tasks/" + auditTask + "/audits?secret=x", valid, 400, nil},
		{"invalid-task", "/v1/tasks/bad:task/audits", valid, 400, nil},
		{"missing-key", "/v1/tasks/" + auditTask + "/audits", valid, 400, func(r *http.Request) { r.Header.Del("Idempotency-Key") }},
		{"duplicate-key", "/v1/tasks/" + auditTask + "/audits", valid, 400, func(r *http.Request) { r.Header["Idempotency-Key"] = []string{auditKey, auditKey} }},
		{"short-key", "/v1/tasks/" + auditTask + "/audits", valid, 400, func(r *http.Request) { r.Header.Set("Idempotency-Key", "short") }},
		{"resume", "/v1/tasks/" + auditTask + "/audits", valid, 400, func(r *http.Request) { r.Header.Set("Last-Event-ID", auditOperation+":1") }},
		{"media", "/v1/tasks/" + auditTask + "/audits", valid, 415, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }},
		{"unknown", "/v1/tasks/" + auditTask + "/audits", `{"reviewer_model_id":"reviewer","max_cost":1,"prompt":"secret"}`, 400, nil},
		{"duplicate", "/v1/tasks/" + auditTask + "/audits", `{"reviewer_model_id":"reviewer","reviewer_model_id":"other","max_cost":1}`, 400, nil},
		{"null", "/v1/tasks/" + auditTask + "/audits", `{"reviewer_model_id":null,"max_cost":1}`, 400, nil},
		{"missing", "/v1/tasks/" + auditTask + "/audits", `{"reviewer_model_id":"reviewer"}`, 400, nil},
		{"bad-reviewer", "/v1/tasks/" + auditTask + "/audits", `{"reviewer_model_id":"provider/model","max_cost":1}`, 400, nil},
		{"negative", "/v1/tasks/" + auditTask + "/audits", `{"reviewer_model_id":"reviewer","max_cost":-1}`, 400, nil},
		{"trailing", "/v1/tasks/" + auditTask + "/audits", valid + `{}`, 400, nil},
		{"oversize", "/v1/tasks/" + auditTask + "/audits", `{"reviewer_model_id":"` + strings.Repeat("a", auditRequestLimit) + `","max_cost":1}`, 413, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			s := services()
			s.RunAudit = func(context.Context, evaluation.AuditRequest, func(evaluation.AuditEvent) error) (evaluation.AuditStatus, error) {
				calls++
				return auditPending(), nil
			}
			h, _ := New(token, 1, s)
			r := auditRequest(http.MethodPost, tc.path, tc.body)
			if tc.mutate != nil {
				tc.mutate(r)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status || calls != 0 {
				t.Fatal(w.Code, calls, w.Body.String())
			}
		})
	}
}

func TestAuditInspectionCancellationAndReplay(t *testing.T) {
	terminal := auditCanceled()
	for _, tc := range []struct {
		name, method, suffix, body, cursor string
		wantSSE                            bool
	}{
		{"inspect", http.MethodGet, "", "", "", false},
		{"cancel", http.MethodPost, "/cancel", `{}`, "", false},
		{"events-all", http.MethodGet, "/events", "", "", true},
		{"events-after-one", http.MethodGet, "/events", "", auditOperation + ":1", true},
		{"events-after-two", http.MethodGet, "/events", "", auditOperation + ":2", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := services()
			s.InspectAudit = func(_ context.Context, task, operation string) (evaluation.AuditStatus, error) {
				if task != auditTask || operation != auditOperation {
					t.Fatal(task, operation)
				}
				return terminal, nil
			}
			s.CancelAudit = func(_ context.Context, task, operation string) (evaluation.AuditStatus, error) {
				if task != auditTask || operation != auditOperation {
					t.Fatal(task, operation)
				}
				return terminal, nil
			}
			s.AuditEvents = func(_ context.Context, task, operation string, after int64) (evaluation.AuditEventPage, error) {
				if task != auditTask || operation != auditOperation {
					t.Fatal(task, operation)
				}
				all := []evaluation.AuditEvent{auditEvent(auditPending(), 1), auditEvent(terminal, 2)}
				return evaluation.AuditEventPage{Version: 1, AuditID: operation, FromSequence: after, NextSequence: 2, HeadSequence: 2, Events: append([]evaluation.AuditEvent(nil), all[after:]...)}, nil
			}
			h, _ := New(token, 1, s)
			path := "/v1/tasks/" + auditTask + "/audits/" + auditOperation + tc.suffix
			r := request(tc.method, path, tc.body)
			if tc.cursor != "" {
				r.Header.Set("Last-Event-ID", tc.cursor)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.wantSSE {
				body := w.Body.String()
				if w.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(body, "event: checkpoint") || strings.Contains(body, "id: "+tc.cursor+"\n") {
					t.Fatal(w.Header(), body)
				}
				wantEvents := 2
				if strings.HasSuffix(tc.cursor, ":1") {
					wantEvents = 1
				} else if strings.HasSuffix(tc.cursor, ":2") {
					wantEvents = 0
				}
				if strings.Count(body, "event: audit") != wantEvents {
					t.Fatal(wantEvents, body)
				}
			} else if !strings.Contains(w.Body.String(), `"status":"canceled"`) {
				t.Fatal(w.Body.String())
			}
		})
	}
}

func TestAuditControlValidationErrorsAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		name, method, suffix, body string
		status                     int
		configure                  func(*Services)
	}{
		{"bad-id", http.MethodGet, "/bad:id", "", 400, nil},
		{"uppercase-id", http.MethodGet, "/" + strings.ToUpper(auditOperation), "", 400, nil},
		{"wrong-method", http.MethodDelete, "/" + auditOperation, "", 404, nil},
		{"inspect-body", http.MethodGet, "/" + auditOperation, `{}`, 400, nil},
		{"cancel-media", http.MethodPost, "/" + auditOperation + "/cancel", `{}`, 415, nil},
		{"cancel-field", http.MethodPost, "/" + auditOperation + "/cancel", `{"reason":"secret"}`, 400, nil},
		{"cancel-oversize", http.MethodPost, "/" + auditOperation + "/cancel", strings.Repeat(" ", 1025), 413, nil},
		{"not-found", http.MethodGet, "/" + auditOperation, "", 404, func(s *Services) {
			s.InspectAudit = func(context.Context, string, string) (evaluation.AuditStatus, error) {
				return evaluation.AuditStatus{}, sql.ErrNoRows
			}
		}},
		{"conflict", http.MethodGet, "/" + auditOperation, "", 409, func(s *Services) {
			s.InspectAudit = func(context.Context, string, string) (evaluation.AuditStatus, error) {
				return evaluation.AuditStatus{}, telemetry.ErrConflict
			}
		}},
		{"deadline", http.MethodGet, "/" + auditOperation, "", 504, func(s *Services) {
			s.InspectAudit = func(context.Context, string, string) (evaluation.AuditStatus, error) {
				return evaluation.AuditStatus{}, context.DeadlineExceeded
			}
		}},
		{"private-error", http.MethodGet, "/" + auditOperation, "", 500, func(s *Services) {
			s.InspectAudit = func(context.Context, string, string) (evaluation.AuditStatus, error) {
				return evaluation.AuditStatus{}, errors.New("private super-secret")
			}
		}},
		{"corrupt", http.MethodGet, "/" + auditOperation, "", 500, func(s *Services) {
			s.InspectAudit = func(context.Context, string, string) (evaluation.AuditStatus, error) {
				out := auditCanceled()
				out.TaskID = "other"
				return out, nil
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := services()
			s.InspectAudit = func(context.Context, string, string) (evaluation.AuditStatus, error) { return auditCanceled(), nil }
			s.CancelAudit = func(context.Context, string, string) (evaluation.AuditStatus, error) { return auditCanceled(), nil }
			if tc.configure != nil {
				tc.configure(&s)
			}
			h, _ := New(token, 1, s)
			r := request(tc.method, "/v1/tasks/"+auditTask+"/audits"+tc.suffix, tc.body)
			if tc.name == "cancel-media" {
				r.Header.Set("Content-Type", "text/plain")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "super-secret") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestAuditReadRejectsTransferEncodingAndBodyBeforeDispatch(t *testing.T) {
	for _, suffix := range []string{"/" + auditOperation, "/" + auditOperation + "/events"} {
		t.Run(suffix, func(t *testing.T) {
			calls := 0
			s := services()
			s.InspectAudit = func(context.Context, string, string) (evaluation.AuditStatus, error) {
				calls++
				return auditCanceled(), nil
			}
			s.AuditEvents = func(context.Context, string, string, int64) (evaluation.AuditEventPage, error) {
				calls++
				return evaluation.AuditEventPage{}, nil
			}
			h, _ := New(token, 1, s)
			for _, mutate := range []func(*http.Request){
				func(r *http.Request) { r.TransferEncoding = []string{"chunked"} },
				func(r *http.Request) { r.ContentLength = -1 },
			} {
				r := request(http.MethodGet, "/v1/tasks/"+auditTask+"/audits"+suffix, "")
				mutate(r)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 400 || calls != 0 {
					t.Fatal(w.Code, calls, w.Body.String())
				}
			}
		})
	}
}

func TestAuditRunServiceCorruptionAndPanicAreSafeSSE(t *testing.T) {
	for _, name := range []string{"corrupt", "short-operation", "panic"} {
		t.Run(name, func(t *testing.T) {
			s := services()
			s.RunAudit = func(_ context.Context, _ evaluation.AuditRequest, emit func(evaluation.AuditEvent) error) (evaluation.AuditStatus, error) {
				if name == "panic" {
					panic("private super-secret")
				}
				bad := auditPending()
				if name == "short-operation" {
					bad.ID = "short"
				} else {
					bad.TaskID = "other"
				}
				_ = emit(auditEvent(bad, 1))
				return bad, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, auditRequest(http.MethodPost, "/v1/tasks/"+auditTask+"/audits", `{"reviewer_model_id":"reviewer","max_cost":1}`))
			body := w.Body.String()
			wantStatus := 500
			wantErrorEvent := false
			if w.Code != wantStatus || strings.Contains(body, "private") || strings.Contains(body, "super-secret") || strings.Contains(body, "event: error") != wantErrorEvent {
				t.Fatal(w.Code, body)
			}
		})
	}
}

func TestAuditRunPreAdmissionErrorsPreserveHTTPStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
		code string
	}{
		{"changed-intent-conflict", telemetry.ErrConflict, 409, "audit_conflict"},
		{"unknown-task", sql.ErrNoRows, 404, "audit_unavailable"},
		{"admission", app.ErrAdmission, 422, "admission_denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := services()
			s.RunAudit = func(context.Context, evaluation.AuditRequest, func(evaluation.AuditEvent) error) (evaluation.AuditStatus, error) {
				return evaluation.AuditStatus{}, tc.err
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, auditRequest(http.MethodPost, "/v1/tasks/"+auditTask+"/audits", `{"reviewer_model_id":"reviewer","max_cost":1}`))
			if w.Code != tc.want || w.Header().Get("Content-Type") != "application/json" || !strings.Contains(w.Body.String(), `"error":"`+tc.code+`"`) || strings.Contains(w.Body.String(), "event:") {
				t.Fatal(w.Code, w.Header(), w.Body.String())
			}
		})
	}
}

func TestAuditResponsesRedactHTTPCredentials(t *testing.T) {
	s := services()
	terminal := auditCompleted("found " + auditKey + " and " + token)
	s.RunAudit = func(_ context.Context, _ evaluation.AuditRequest, emit func(evaluation.AuditEvent) error) (evaluation.AuditStatus, error) {
		if err := emit(auditEvent(auditPending(), 1)); err != nil {
			return evaluation.AuditStatus{}, err
		}
		if err := emit(auditEvent(terminal, 2)); err != nil {
			return evaluation.AuditStatus{}, err
		}
		return terminal, nil
	}
	s.InspectAudit = func(context.Context, string, string) (evaluation.AuditStatus, error) { return terminal, nil }
	h, _ := New(token, 1, s)
	requests := []*http.Request{
		auditRequest(http.MethodPost, "/v1/tasks/"+auditTask+"/audits", `{"reviewer_model_id":"reviewer","max_cost":1}`),
		request(http.MethodGet, "/v1/tasks/"+auditTask+"/audits/"+auditOperation, ""),
	}
	for i, r := range requests {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		body := w.Body.String()
		if w.Code != 200 || strings.Contains(body, token) || (i == 0 && strings.Contains(body, auditKey)) || !strings.Contains(body, "[REDACTED]") {
			t.Fatal(w.Code, body)
		}
	}
}

func TestAuditEventCursorAndCapacityFailClosed(t *testing.T) {
	s := services()
	calls := 0
	s.RunAudit = func(context.Context, evaluation.AuditRequest, func(evaluation.AuditEvent) error) (evaluation.AuditStatus, error) {
		return auditPending(), nil
	}
	s.AuditEvents = func(context.Context, string, string, int64) (evaluation.AuditEventPage, error) {
		calls++
		return evaluation.AuditEventPage{}, app.ErrAdmission
	}
	h, _ := New(token, 1, s)
	path := "/v1/tasks/" + auditTask + "/audits/" + auditOperation + "/events"
	for _, cursor := range []string{"other:1", auditOperation + ":01", auditOperation + ":-1"} {
		r := request(http.MethodGet, path, "")
		r.Header.Set("Last-Event-ID", cursor)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 || calls != 0 {
			t.Fatal(cursor, w.Code, calls, w.Body.String())
		}
	}
	r := request(http.MethodGet, path, "")
	r.Header.Set("Last-Event-ID", auditOperation+":3")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 409 || calls != 1 {
		t.Fatal(w.Code, calls, w.Body.String())
	}
	r = request(http.MethodGet, path, "")
	r.Header.Set("Last-Event-ID", auditOperation+":2")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 409 || calls != 2 {
		t.Fatal(w.Code, calls, w.Body.String())
	}

	// Audit triggers use execution capacity; inspection, cancellation, and
	// replay use independent control capacity.
	h.slots <- struct{}{}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, auditRequest(http.MethodPost, "/v1/tasks/"+auditTask+"/audits", `{"reviewer_model_id":"reviewer","max_cost":1}`))
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	<-h.slots
	h.controls <- struct{}{}
	h.controls <- struct{}{}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request(http.MethodGet, path, ""))
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	<-h.controls
	<-h.controls
}
