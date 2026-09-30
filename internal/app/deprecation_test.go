package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
)

func TestModelDeprecationUsesCurrentFeedbackWithoutMutation(t *testing.T) {
	svc, cfg := autoFixture(t)
	ctx := context.Background()
	out, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "hello", Domain: "code"})
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordFeedback(ctx, cfg.Telemetry.Database, out.TaskID, false, 0); err != nil {
		t.Fatal(err)
	}
	p := evaluation.DeprecationPolicy{Window: 50, MinSamples: 1, FailureThreshold: .35}
	before, err := FeedbackHistory(ctx, cfg.Telemetry.Database, out.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	report, err := svc.ModelDeprecation(ctx, "a", "code", "default", p)
	if err != nil || !report.Candidate || report.Sampled != 1 || report.QualityFailures != 1 || report.ExecutionFailures != 0 || !report.ApprovalRequired || report.EstimatedRAMBytes != cfg.Models[0].RAMBytes {
		t.Fatal(report, err)
	}
	after, err := FeedbackHistory(ctx, cfg.Telemetry.Database, out.TaskID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("report changed evidence", err)
	}
	if err := ReviseFeedback(ctx, cfg.Telemetry.Database, out.TaskID, before[len(before)-1].ID, true); err != nil {
		t.Fatal(err)
	}
	revised, err := svc.ModelDeprecation(ctx, "a", "code", "default", p)
	if err != nil || revised.Candidate || revised.Sampled != 1 || revised.Failures != 0 || revised.EvidenceDigest == report.EvidenceDigest {
		t.Fatal(revised, err)
	}
	empty, err := svc.ModelDeprecation(ctx, "a", "creative", "default", p)
	if err != nil || empty.Candidate || empty.Sampled != 0 || empty.Reason != "insufficient_evidence" || empty.Key.Domain != "creative" {
		t.Fatal(empty, err)
	}
}

func TestModelDeprecationMissingStorageNeverCreated(t *testing.T) {
	svc, _ := autoFixture(t)
	path := filepath.Join(t.TempDir(), "absent.db")
	svc.settings.Telemetry.Database = path
	p := evaluation.DeprecationPolicy{Window: 50, MinSamples: 20, FailureThreshold: .35}
	if _, err := svc.ModelDeprecation(context.Background(), "a", "code", "default", p); err == nil {
		t.Fatal("missing store admitted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("inspection created storage")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.ModelDeprecation(ctx, "a", "code", "default", p); err == nil {
		t.Fatal("canceled admitted")
	}
}
