package app

import (
	"context"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestAutomaticRoutingConsumesAuditQuality(t *testing.T) {
	svc, cfg := autoFixture(t)
	ctx := context.Background()
	source, err := svc.Run(ctx, Request{ModelID: "z", Prompt: "seed", Domain: "creative"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.Read(ctx, source.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	attempt := ""
	for _, e := range events {
		if e.Kind == runtime.TurnStarted {
			attempt = e.AttemptID
		}
	}
	audit := evaluation.AuditRecord{Version: 1, ID: "advisory", TaskID: source.TaskID, AttemptID: attempt, EvaluatorModel: "a", EvaluatorProvider: "local", Time: time.Now(), EvidenceRefs: []string{"candidate"}, Audit: evaluation.Audit{Version: 1, EvaluatorID: "a", RubricVersion: "v1", Domain: "creative", Verdict: "accept", Confidence: 1, Findings: []evaluation.AuditFinding{{Summary: "advisory", EvidenceRefs: []string{"candidate"}}}}}
	if err := db.RecordAudit(ctx, audit); err != nil {
		t.Fatal(err)
	}
	out, err := svc.Run(ctx, Request{Prompt: "next", Domain: "creative"})
	if err != nil || out.Text != "z" {
		t.Fatalf("%+v %v", out, err)
	}
	events, err = db.Read(ctx, out.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, e := range events {
		if e.Kind == runtime.RouteSelected {
			seen = true
			if e.Data.Route.Primary.AdvisorySamples != 1 || e.Data.Route.Primary.Samples != 0 {
				t.Fatal("audit treated as execution sample")
			}
		}
	}
	if !seen {
		t.Fatal("missing route explanation")
	}
	svc.settings.Evaluation.Judge = false
	out, err = svc.Run(ctx, Request{Prompt: "no judge", Domain: "creative"})
	if err != nil || out.Text != "a" {
		t.Fatalf("kill switch ignored: %+v %v", out, err)
	}
}
