package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/accounting"
	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
)

func TestAuditTaskPersistsIndependentRedactedReview(t *testing.T) {
	svc, cfg := autoFixture(t)
	ctx := context.Background()
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "hello", Domain: "creative"})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	selfGuidance := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Model    string `json:"model"`
			Tools    []any  `json:"tools"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("bad request")
		}
		if len(body.Tools) > 0 {
			t.Error("review tools exposed")
		}
		if body.Model == "a" {
			for _, message := range body.Messages {
				selfGuidance = selfGuidance || strings.Contains(message.Content, "same-model review invocation")
			}
		}
		a := evaluation.Audit{Version: 1, EvaluatorID: body.Model, RubricVersion: "darwin-review-v2", Domain: "creative", Verdict: "reject", Confidence: .4, Findings: []evaluation.AuditFinding{{Summary: "private-token advisory", EvidenceRefs: []string{"candidate"}}}}
		encoded, _ := json.Marshal(a)
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", string(encoded))
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	svc.secret = func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return "private-token"
		}
		return ""
	}
	record, err := svc.AuditTask(ctx, source.TaskID, "z", 0)
	if err != nil || calls != 1 || record.Audit.Verdict != "reject" || strings.Contains(record.Audit.Findings[0].Summary, "private-token") {
		t.Fatalf("%+v %v calls=%d", record, err, calls)
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	saved, err := db.Audit(ctx, record.ID)
	if err != nil || saved.TaskID != source.TaskID || saved.EvaluatorModel != "z" {
		t.Fatalf("%+v %v", saved, err)
	}
	attempts, err := db.ReviewAttempts(ctx, source.TaskID, "", 100)
	if err != nil || len(attempts) != 1 || attempts[0].Status != "completed" || attempts[0].AuditID != record.ID {
		t.Fatalf("review lifecycle: %+v %v", attempts, err)
	}
	self, err := svc.AuditTask(ctx, source.TaskID, "a", 0)
	if err != nil || self.EvaluatorModel != "a" || self.Audit.Verdict != "reject" || !selfGuidance {
		t.Fatal("same-model advisory review failed", self, err)
	}
	svc.settings.Mode = "hybrid"
	svc.settings.Models[1].Locality = "cloud"
	if _, err := svc.AuditTask(ctx, source.TaskID, "z", 0); err == nil {
		t.Fatal("local history escaped to cloud")
	}
	if calls != 2 {
		t.Fatal("denied review dispatched")
	}
}

func TestFailedReviewLifecycleSurvivesCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			svc, cfg := autoFixture(t)
			ctx := context.Background()
			source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "hello"})
			if err != nil {
				t.Fatal(err)
			}
			reviewCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// The attempt must be durable before any reviewer request arrives.
				db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
				if err != nil {
					t.Error(err)
					return
				}
				attempts, err := db.ReviewAttempts(ctx, source.TaskID, "", 100)
				db.Close()
				if err != nil || len(attempts) != 1 || attempts[0].Status != "started" {
					t.Errorf("dispatch preceded persistence: %+v %v", attempts, err)
				}
				if canceled {
					cancel()
					return
				}
				fmt.Fprintln(w, `{"message":{"content":"sensitive-invalid-review"},"done":true,"done_reason":"stop","prompt_eval_count":23,"eval_count":11}`)
			}))
			defer server.Close()
			svc.settings.Providers[0].Endpoint = server.URL
			if _, err := svc.AuditTask(reviewCtx, source.TaskID, "z", 0); err == nil {
				t.Fatal("failed review accepted")
			}
			db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			attempts, err := db.ReviewAttempts(ctx, source.TaskID, "", 100)
			code := "review_failed"
			if canceled {
				code = "canceled"
			}
			if err != nil || len(attempts) != 1 || attempts[0].Status != "failed" || attempts[0].Code != code || attempts[0].FinishedAt.Before(attempts[0].StartedAt) || time.Since(attempts[0].FinishedAt) > time.Minute {
				t.Fatalf("failure lifecycle: %+v %v", attempts, err)
			}
			if canceled {
				if attempts[0].Usage != nil || attempts[0].Elapsed != 0 {
					t.Fatal("canceled review retained unverified usage", attempts[0])
				}
			} else if attempts[0].Usage == nil || attempts[0].Usage.InputTokens != 23 || attempts[0].Usage.OutputTokens != 11 || attempts[0].Elapsed <= 0 {
				t.Fatal("verified failed-review usage was lost", attempts[0])
			}
			encoded, _ := json.Marshal(attempts)
			if strings.Contains(string(encoded), "sensitive-invalid-review") {
				t.Fatal("review payload persisted in lifecycle")
			}
			audits, err := db.Audits(ctx, source.TaskID, "", 100)
			if err != nil || len(audits) != 0 {
				t.Fatalf("failure created advisory evidence: %+v %v", audits, err)
			}
			totals, err := db.UsageTotals(ctx, accounting.Scope{TaskID: source.TaskID})
			known := int64(1)
			if canceled {
				known = 0
			}
			if err != nil || totals.Judge.Records != 1 || totals.Judge.KnownUsageRecords != known || totals.Judge.UnknownUsageRecords != 1-known || totals.Judge.KnownInputTokens != known*23 || totals.Judge.KnownOutputTokens != known*11 {
				t.Fatalf("failed-review accounting mismatch: %#v %v", totals, err)
			}
		})
	}
}

func TestAutomaticReviewDoesNotReplaceCandidateOutcome(t *testing.T) {
	svc, _ := autoFixture(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		calls++
		text := "candidate answer"
		review := body.Model == "z"
		for _, message := range body.Messages {
			review = review || strings.Contains(message.Content, "bounded output auditor")
		}
		if review {
			encoded, _ := json.Marshal(evaluation.Audit{Version: 1, EvaluatorID: body.Model, RubricVersion: "darwin-review-v2", Domain: "creative", Verdict: "abstain", Confidence: 0, Findings: []evaluation.AuditFinding{}})
			text = string(encoded)
		}
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", text)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	svc.settings.Evaluation.AutoReviewModel = "z"
	out, err := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "hello", Domain: "creative"})
	if err != nil || out.Text != "candidate answer" || out.AuditID == "" || out.AuditStatus != "recorded" {
		t.Fatalf("%+v %v", out, err)
	}
	svc.settings.Evaluation.AutoReviewModel = "a"
	before := calls
	out, err = svc.Run(context.Background(), Request{ModelID: "a", Prompt: "hello", Domain: "creative"})
	if err != nil || out.Text != "candidate answer" || out.AuditID == "" || out.AuditStatus != "recorded" || calls-before != 2 {
		t.Fatalf("same-model review changed task or recursed: %+v %v calls=%d", out, err, calls-before)
	}
	svc.settings.Evaluation.Judge = false
	out, err = svc.Run(context.Background(), Request{ModelID: "a", Prompt: "hello"})
	if err != nil || out.AuditStatus != "" {
		t.Fatalf("kill switch ignored: %+v %v", out, err)
	}
}
