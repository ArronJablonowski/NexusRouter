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
	"github.com/ArronJablonowski/NexusRouter/internal/app"
)

func decisionCommandFixture() approvals.Command {
	return approvals.Command{Expected: approvalRecordFixture().Request, ID: "decision", Allowed: true}
}
func decisionCommandBody(c approvals.Command) string { body, _ := json.Marshal(c); return string(body) }
func commandRecord(c approvals.Command) approvals.Record {
	return approvals.Record{Request: c.Expected, State: approvals.Approved, Decisions: []approvals.Decision{{ID: c.ID, Actor: "api_operator", Allowed: c.Allowed, Time: c.Expected.CreatedAt.Add(time.Second)}}}
}

func TestApprovalDecisionAPIRecordsAndReplaysLaterState(t *testing.T) {
	for _, state := range []string{"approved", "revoked", "consumed"} {
		t.Run(state, func(t *testing.T) {
			c := decisionCommandFixture()
			s := services()
			calls := 0
			s.DecideApproval = func(ctx context.Context, got approvals.Command) (approvals.Record, error) {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 5*time.Second || got.ID != c.ID || got.Allowed != c.Allowed || !got.Expected.Matches(c.Expected) {
					t.Error("unbound command", got)
				}
				r := commandRecord(got)
				r.State = state
				if state == "revoked" {
					r.Decisions = append(r.Decisions, approvals.Decision{ID: "later", Actor: "api_operator", Allowed: false, Time: c.Expected.CreatedAt.Add(2 * time.Second)})
				}
				if state == "consumed" {
					at := c.Expected.CreatedAt.Add(2 * time.Second)
					r.ConsumedAt = &at
				}
				return r, nil
			}
			h, _ := New(token, 1, s)
			h.slots <- struct{}{}
			h.controls <- struct{}{}
			h.controls <- struct{}{}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("POST", "/v1/tasks/task/approvals/approval/decision", decisionCommandBody(c)))
			if w.Code != 200 || calls != 1 || w.Header().Get("Cache-Control") != "no-store" || len(h.approvalSlots) != 0 {
				t.Fatal(w.Code, w.Body.String(), calls)
			}
		})
	}
}

func TestApprovalDecisionAPIRejectsUntrustedInputs(t *testing.T) {
	for _, kind := range []string{"auth", "origin", "query", "emptyquery", "method", "media", "nil_service", "capacity", "oversize", "malformed", "unknown", "actor", "duplicate", "nullallowed", "missingallowed", "task", "id", "expected_unknown", "invalid_utf8"} {
		t.Run(kind, func(t *testing.T) {
			c := decisionCommandFixture()
			body := decisionCommandBody(c)
			path := "/v1/tasks/task/approvals/approval/decision"
			method := "POST"
			want := 400
			s := services()
			s.DecideApproval = func(context.Context, approvals.Command) (approvals.Record, error) {
				t.Error("invalid input reached control")
				return approvals.Record{}, nil
			}
			h, _ := New(token, 1, s)
			switch kind {
			case "auth":
				want = 401
			case "origin":
				want = 403
			case "query":
				path += "?actor=secret"
			case "emptyquery":
				path += "?"
			case "method":
				method = "GET"
				body = ""
				want = 404
			case "media":
				want = 415
			case "nil_service":
				h.services.DecideApproval = nil
				want = 503
			case "capacity":
				h.approvalSlots <- struct{}{}
				h.approvalSlots <- struct{}{}
				want = 503
			case "oversize":
				body = strings.Repeat("x", (16<<10)+1)
				want = 413
			case "malformed":
				body = "{"
			case "unknown":
				body = strings.TrimSuffix(body, "}") + `,"unknown":"secret"}`
			case "actor":
				body = strings.TrimSuffix(body, "}") + `,"actor":"attacker"}`
			case "duplicate":
				body = strings.TrimSuffix(body, "}") + `,"allowed":false}`
			case "nullallowed":
				body = strings.Replace(body, `"allowed":true`, `"allowed":null`, 1)
			case "missingallowed":
				body = strings.Replace(body, `,"allowed":true`, "", 1)
			case "task":
				c.Expected.TaskID = "other"
				body = decisionCommandBody(c)
			case "id":
				c.Expected.ID = "other"
				body = decisionCommandBody(c)
			case "expected_unknown":
				body = strings.Replace(body, `"expected":{`, `"expected":{"actor":"attacker",`, 1)
			case "invalid_utf8":
				body = string([]byte{255})
			}
			r := request(method, path, body)
			if kind == "auth" {
				r.Header.Del("Authorization")
			}
			if kind == "origin" {
				r.Header.Set("Origin", "https://invalid.test")
			}
			if kind == "media" {
				r.Header.Set("Content-Type", "text/plain")
			}
			if kind == "capacity" {
				r.Body = metricsUnreadBody{t}
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want || strings.Contains(w.Body.String(), "secret") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestApprovalDecisionAPIRejectsUnboundResults(t *testing.T) {
	for _, kind := range []string{"expected", "decision_id", "allowed", "invalid", "missing"} {
		t.Run(kind, func(t *testing.T) {
			c := decisionCommandFixture()
			s := services()
			s.DecideApproval = func(context.Context, approvals.Command) (approvals.Record, error) {
				r := commandRecord(c)
				switch kind {
				case "expected":
					r.Request.Scope = "other"
				case "decision_id":
					r.Decisions[0].ID = "other"
				case "allowed":
					r.Decisions[0].Allowed = false
					r.State = approvals.Denied
				case "invalid":
					r.Decisions[0].Actor = ""
				case "missing":
					r.State = approvals.Pending
					r.Decisions = nil
				}
				return r, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request(http.MethodPost, "/v1/tasks/task/approvals/approval/decision", decisionCommandBody(c)))
			if w.Code != 500 || strings.Contains(w.Body.String(), "workspace") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestApprovalDecisionAPISanitizesServiceFailures(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{{approvals.ErrConflict, 409}, {approvals.ErrInvalid, 400}, {app.ErrAdmission, 400}, {errors.New("secret"), 503}, {context.Canceled, 503}} {
		t.Run(tc.err.Error(), func(t *testing.T) {
			s := services()
			s.DecideApproval = func(context.Context, approvals.Command) (approvals.Record, error) { return approvals.Record{}, tc.err }
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("POST", "/v1/tasks/task/approvals/approval/decision", decisionCommandBody(decisionCommandFixture())))
			if w.Code != tc.want || strings.Contains(w.Body.String(), "secret") || len(h.approvalSlots) != 0 {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}
