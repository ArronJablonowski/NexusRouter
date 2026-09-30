package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/routing"
)

// This fixture qualifies the production audit path, not the quality of a live
// model. Candidate and reviewer traffic remains on loopback.
func TestMVPOrchestratorAuditEvidence(t *testing.T) {
	ctx := context.Background()
	svc, cfg := autoFixture(t)
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "Write the requested two-stanza poem.", Domain: "creative"})
	if err != nil || source.Text != "a" {
		t.Fatal("candidate fixture failed", source, err)
	}

	var calls atomic.Int32
	reviewer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Model    string              `json:"model"`
			Messages []providers.Message `json:"messages"`
			Tools    []json.RawMessage   `json:"tools"`
		}
		if r.URL.Path != "/api/chat" || json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Tools) != 0 {
			t.Error("review request bypassed the bounded tool-free adapter")
			return
		}
		var supplied map[string]bool
		for _, message := range request.Messages {
			if message.Role != "user" {
				continue
			}
			var envelope struct {
				Evidence []evaluation.ReviewEvidence `json:"evidence"`
			}
			if json.Unmarshal([]byte(message.Content), &envelope) != nil {
				continue
			}
			supplied = map[string]bool{}
			for _, item := range envelope.Evidence {
				supplied[item.ID] = true
			}
		}
		if !supplied["candidate_execution"] || !supplied["session_history"] {
			t.Error("reviewer lacked citable execution evidence")
		}
		verdict, confidence, summary := "reject", .9, "The candidate omitted the requested poem."
		if request.Model == "a" {
			verdict, confidence, summary = "accept", 1, "The same model asserts its response is acceptable."
		}
		audit := evaluation.Audit{Version: 1, EvaluatorID: request.Model, RubricVersion: "darwin-review-v2", Domain: "creative", Verdict: verdict, Confidence: confidence, Findings: []evaluation.AuditFinding{{Summary: summary, EvidenceRefs: []string{"candidate", "candidate_execution"}}}}
		body, _ := json.Marshal(audit)
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", body)
	}))
	defer reviewer.Close()
	svc.settings.Providers[0].Endpoint = reviewer.URL

	independent, err := svc.AuditTask(ctx, source.TaskID, "z", 0)
	if err != nil || independent.Audit.Verdict != "reject" || !reflect.DeepEqual(independent.Audit.Findings[0].EvidenceRefs, []string{"candidate", "candidate_execution"}) {
		t.Fatal("meaningless output was not durably rejected", independent, err)
	}
	self, err := svc.AuditTask(ctx, source.TaskID, "a", 0)
	if err != nil || self.Audit.Verdict != "accept" {
		t.Fatal("same-model fixture failed", self, err)
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	key := routing.Key{Model: "a", Provider: "local", Domain: "creative", Profile: "default"}
	advisory, err := db.AuditQuality(ctx, key)
	if err != nil || advisory.Samples != 1 || advisory.Quality != 0 || advisory.Confidence != .9 {
		t.Fatal("same-model acceptance created positive routing evidence", advisory, err)
	}
	saved, err := db.Audit(ctx, independent.ID)
	attempts, attemptsErr := db.ReviewAttempts(ctx, source.TaskID, "", 100)
	db.Close()
	if err != nil || attemptsErr != nil || saved.TaskID != source.TaskID || len(attempts) != 2 || attempts[0].Status != "completed" || attempts[1].Status != "completed" {
		t.Fatal("audit lifecycle was not durable", saved, attempts, err, attemptsErr)
	}

	if err = RecordFeedback(ctx, cfg.Telemetry.Database, source.TaskID, true, 0); err != nil {
		t.Fatal("creative user feedback failed", err)
	}
	db, err = telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	advisory, err = db.AuditQuality(ctx, key)
	fitness, fitnessErr := db.Fitness(ctx, key)
	db.Close()
	if err != nil || advisory != (routing.Advisory{}) || fitnessErr != nil || fitness.Samples != 1 || fitness.Quality != 1 {
		t.Fatal("explicit user feedback did not supersede creative audit evidence", advisory, fitness, err, fitnessErr)
	}

	// The source was created under local-only policy. Reclassifying the reviewer
	// as cloud in hybrid mode must deny before any third request is sent.
	svc.settings.Mode = "hybrid"
	svc.settings.Providers = append(svc.settings.Providers, config.Provider{ID: "cloud", Kind: "ollama", Endpoint: reviewer.URL})
	svc.settings.Models[1].Provider, svc.settings.Models[1].Locality = "cloud", "cloud"
	if _, err = svc.AuditTask(ctx, source.TaskID, "z", 0); err == nil || calls.Load() != 2 {
		t.Fatal("local-only audit evidence escaped to a cloud reviewer", err, calls.Load())
	}
}
