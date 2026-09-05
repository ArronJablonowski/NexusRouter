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

	"darwinrouter/evaluation"
	"darwinrouter/internal/telemetry"
)

func TestAuditTaskPersistsIndependentRedactedReview(t *testing.T) {
	svc, cfg := autoFixture(t)
	ctx := context.Background()
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "hello", Domain: "creative"})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("bad request")
		}
		if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
			t.Error("review tools exposed")
		}
		a := evaluation.Audit{Version: 1, EvaluatorID: "z", RubricVersion: "darwin-review-v2", Domain: "creative", Verdict: "reject", Confidence: .4, Findings: []evaluation.AuditFinding{{Summary: "private-token advisory", EvidenceRefs: []string{"candidate"}}}}
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
	if _, err := svc.AuditTask(ctx, source.TaskID, "a", 0); err == nil {
		t.Fatal("self review admitted")
	}
	svc.settings.Mode = "hybrid"
	svc.settings.Models[1].Locality = "cloud"
	if _, err := svc.AuditTask(ctx, source.TaskID, "z", 0); err == nil {
		t.Fatal("local history escaped to cloud")
	}
	if calls != 1 {
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
				fmt.Fprintln(w, `{"message":{"content":"sensitive-invalid-review"},"done":true,"done_reason":"stop"}`)
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
			encoded, _ := json.Marshal(attempts)
			if strings.Contains(string(encoded), "sensitive-invalid-review") {
				t.Fatal("review payload persisted in lifecycle")
			}
			audits, err := db.Audits(ctx, source.TaskID, "", 100)
			if err != nil || len(audits) != 0 {
				t.Fatalf("failure created advisory evidence: %+v %v", audits, err)
			}
		})
	}
}

func TestAutomaticReviewDoesNotReplaceCandidateOutcome(t *testing.T) {
	svc, _ := autoFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		text := "candidate answer"
		if body.Model == "z" {
			encoded, _ := json.Marshal(evaluation.Audit{Version: 1, EvaluatorID: "z", RubricVersion: "darwin-review-v2", Domain: "creative", Verdict: "abstain", Confidence: 0, Findings: []evaluation.AuditFinding{}})
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
	out, err = svc.Run(context.Background(), Request{ModelID: "a", Prompt: "hello"})
	if err != nil || out.Text != "candidate answer" || out.AuditID != "" || out.AuditStatus != "failed" {
		t.Fatalf("failed review changed task: %+v %v", out, err)
	}
	svc.settings.Evaluation.Judge = false
	out, err = svc.Run(context.Background(), Request{ModelID: "a", Prompt: "hello"})
	if err != nil || out.AuditStatus != "" {
		t.Fatalf("kill switch ignored: %+v %v", out, err)
	}
}
