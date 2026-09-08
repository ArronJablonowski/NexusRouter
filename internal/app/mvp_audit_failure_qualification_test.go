package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
)

// auditQualificationDeadline is manually expired only after the provider has
// observed dispatch. The fallback parent keeps a broken fixture bounded while
// Err still distinguishes the controlled deadline from ordinary cancellation.
type auditQualificationDeadline struct {
	parent context.Context
	done   chan struct{}
	once   sync.Once
	err    atomic.Int32
}

func (c *auditQualificationDeadline) Deadline() (time.Time, bool) { return c.parent.Deadline() }
func (c *auditQualificationDeadline) Done() <-chan struct{}       { return c.done }
func (c *auditQualificationDeadline) Value(key any) any           { return c.parent.Value(key) }
func (c *auditQualificationDeadline) Err() error {
	switch c.err.Load() {
	case 1:
		return context.Canceled
	case 2:
		return context.DeadlineExceeded
	default:
		return nil
	}
}
func (c *auditQualificationDeadline) expire(err error) {
	code := int32(1)
	if err == context.DeadlineExceeded {
		code = 2
	}
	c.once.Do(func() {
		c.err.Store(code)
		close(c.done)
	})
}

func newAuditQualificationDeadline(parent context.Context) (*auditQualificationDeadline, context.CancelFunc) {
	fallback, cancel := context.WithTimeout(parent, 10*time.Second)
	ctx := &auditQualificationDeadline{parent: fallback, done: make(chan struct{})}
	stop := context.AfterFunc(fallback, func() { ctx.expire(fallback.Err()) })
	return ctx, func() {
		stop()
		cancel()
	}
}

// TestMVPOrchestratorAuditFailureModes qualifies the production audit path's
// non-verdict and failure lifecycle behavior without a live model or network.
// Each case starts from a real completed task and inspects the SQLite records
// written by Service.AuditTask.
func TestMVPOrchestratorAuditFailureModes(t *testing.T) {
	t.Run("independent_accept_is_durable", func(t *testing.T) {
		ctx := context.Background()
		svc, cfg := autoFixture(t)
		source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "Return exactly a.", Domain: "general"})
		if err != nil || source.Text != "a" {
			t.Fatal("candidate fixture failed", source, err)
		}

		reviewer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				Model string            `json:"model"`
				Tools []json.RawMessage `json:"tools"`
			}
			if r.URL.Path != "/api/chat" || json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "z" || len(request.Tools) != 0 {
				t.Error("invalid bounded review request")
				return
			}
			audit := evaluation.Audit{
				Version:       1,
				EvaluatorID:   "z",
				RubricVersion: "darwin-review-v2",
				Domain:        "general",
				Verdict:       "accept",
				Confidence:    1,
				Findings: []evaluation.AuditFinding{{
					Summary:      "The candidate exactly matches the requested literal output.",
					EvidenceRefs: []string{"requirements", "candidate", "candidate_execution"},
				}},
			}
			body, _ := json.Marshal(audit)
			fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", body)
		}))
		defer reviewer.Close()
		svc.settings.Providers[0].Endpoint = reviewer.URL

		record, err := svc.AuditTask(ctx, source.TaskID, "z", 0)
		if err != nil || record.Audit.Verdict != "accept" || len(record.Audit.Findings) != 1 {
			t.Fatal("independent acceptance was not preserved", record, err)
		}
		db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		saved, savedErr := db.Audit(ctx, record.ID)
		attempts, attemptsErr := db.ReviewAttempts(ctx, source.TaskID, "", 100)
		if savedErr != nil || saved.Audit.Verdict != "accept" || attemptsErr != nil || len(attempts) != 1 || attempts[0].Status != "completed" || attempts[0].Code != "" || attempts[0].AuditID != record.ID {
			t.Fatal("acceptance lifecycle was not durable", saved, attempts, savedErr, attemptsErr)
		}
	})

	t.Run("abstention_is_durable_and_distinct", func(t *testing.T) {
		ctx := context.Background()
		svc, cfg := autoFixture(t)
		source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "Write a short story with an ambiguous ending.", Domain: "creative"})
		if err != nil {
			t.Fatal(err)
		}

		reviewer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				Model string            `json:"model"`
				Tools []json.RawMessage `json:"tools"`
			}
			if r.URL.Path != "/api/chat" || json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "z" || len(request.Tools) != 0 {
				t.Error("invalid bounded review request")
				return
			}
			audit := evaluation.Audit{
				Version:       1,
				EvaluatorID:   "z",
				RubricVersion: "darwin-review-v2",
				Domain:        "creative",
				Verdict:       "abstain",
				Confidence:    0,
				Findings:      []evaluation.AuditFinding{},
			}
			body, _ := json.Marshal(audit)
			fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", body)
		}))
		defer reviewer.Close()
		svc.settings.Providers[0].Endpoint = reviewer.URL

		record, err := svc.AuditTask(ctx, source.TaskID, "z", 0)
		if err != nil || record.Audit.Verdict != "abstain" || len(record.Audit.Findings) != 0 {
			t.Fatal("abstention was not preserved", record, err)
		}
		db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		saved, savedErr := db.Audit(ctx, record.ID)
		attempts, attemptsErr := db.ReviewAttempts(ctx, source.TaskID, "", 100)
		if savedErr != nil || saved.Audit.Verdict != "abstain" || attemptsErr != nil || len(attempts) != 1 || attempts[0].Status != "completed" || attempts[0].Code != "" || attempts[0].AuditID != record.ID {
			t.Fatal("abstention lifecycle was not durable", saved, attempts, savedErr, attemptsErr)
		}
	})

	for _, mode := range []string{"malformed", "caller_cancellation", "deadline_timeout"} {
		t.Run(mode, func(t *testing.T) {
			baseCtx := context.Background()
			svc, cfg := autoFixture(t)
			source, err := svc.Run(baseCtx, Request{ModelID: "a", Prompt: "Return a substantive answer.", Domain: "general"})
			if err != nil {
				t.Fatal(err)
			}

			reviewCtx := baseCtx
			cancel := func() {}
			var controlledDeadline *auditQualificationDeadline
			if mode == "caller_cancellation" {
				reviewCtx, cancel = context.WithCancel(baseCtx)
			} else if mode == "deadline_timeout" {
				controlledDeadline, cancel = newAuditQualificationDeadline(baseCtx)
				reviewCtx = controlledDeadline
			}
			defer cancel()
			releaseHandler := make(chan struct{})
			reviewer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "malformed" {
					fmt.Fprintln(w, `{"message":{"content":"private malformed review"},"done":true,"done_reason":"stop"}`)
					return
				}
				if mode == "caller_cancellation" {
					cancel()
				} else if mode == "deadline_timeout" {
					// AuditTask persists the started row before calling the
					// provider. Prove that ordering before expiring the controlled
					// deadline, so test speed cannot move expiry ahead of dispatch.
					db, err := telemetry.OpenReadOnly(baseCtx, cfg.Telemetry.Database)
					if err != nil {
						t.Error(err)
						return
					}
					attempts, err := db.ReviewAttempts(baseCtx, source.TaskID, "", 100)
					db.Close()
					if err != nil || len(attempts) != 1 || attempts[0].Status != "started" {
						t.Error("review dispatch preceded durable start", attempts, err)
						return
					}
					controlledDeadline.expire(context.DeadlineExceeded)
				}
				// Some test transports do not close the server-side request context
				// until after the handler returns. The caller context remains the
				// authoritative cancellation signal; release this handler once the
				// production audit call has observed it and returned.
				<-releaseHandler
			}))
			defer reviewer.Close()
			svc.settings.Providers[0].Endpoint = reviewer.URL

			_, auditErr := svc.AuditTask(reviewCtx, source.TaskID, "z", 0)
			if mode != "malformed" {
				close(releaseHandler)
			}
			if auditErr == nil {
				t.Fatal("failed audit was accepted")
			}
			db, err := telemetry.OpenReadOnly(baseCtx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			attempts, attemptsErr := db.ReviewAttempts(baseCtx, source.TaskID, "", 100)
			wantCode := "review_failed"
			if mode != "malformed" {
				wantCode = "canceled"
			}
			if attemptsErr != nil || len(attempts) != 1 || attempts[0].Status != "failed" || attempts[0].Code != wantCode || attempts[0].AuditID != "" || attempts[0].FinishedAt.IsZero() || attempts[0].FinishedAt.Before(attempts[0].StartedAt) {
				t.Fatal("failed audit lifecycle was not durable", attempts, attemptsErr)
			}
			encoded, _ := json.Marshal(attempts)
			if strings.Contains(string(encoded), "private malformed review") {
				t.Fatal("reviewer output leaked into lifecycle evidence")
			}
			audits, auditsErr := db.Audits(baseCtx, source.TaskID, "", 100)
			if auditsErr != nil || len(audits) != 0 {
				t.Fatal("failed audit created advisory evidence", audits, auditsErr)
			}
		})
	}
}
