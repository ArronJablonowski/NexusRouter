package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func appComparisonSelectionRequest(r skills.ComparisonRequest) skills.ComparisonSelectionRequest {
	return skills.ComparisonSelectionRequest{Version: 1, ModelID: r.ModelID, Domain: r.Domain, Profile: r.Profile, Name: r.Name, BaselineVersion: r.BaselineVersion, CandidateVersion: r.CandidateVersion, Source: r.Source, MinSamples: r.MinSamples, MinDrop: r.MinDrop, Privacy: "local_only", TasksPerVersion: 20}
}

func TestSelectSkillComparisonConfiguredSnapshot(t *testing.T) {
	svc, db, explicit := appComparisonFixture(t, 40)
	ctx := context.Background()
	request := appComparisonSelectionRequest(explicit)
	before, err := db.SkillTaskOutcomes(ctx, explicit.Tasks)
	if err != nil {
		t.Fatal(err)
	}
	report, err := svc.SelectSkillComparison(ctx, request)
	if err != nil || report.Validate() != nil || report.ConfiguredModelID != request.ModelID || report.Comparison == nil || report.Comparison.Status != "regression_signal" || report.Baseline.Selected != 20 || report.Candidate.Selected != 20 || report.Policy.Comparison.Execution.Model != "actual:tag" || report.Policy.Comparison.Key.Scope != "project" {
		t.Fatal(report, err)
	}
	after, err := db.SkillTaskOutcomes(ctx, explicit.Tasks)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("selection mutated evidence", err)
	}
	if _, err := os.Stat(svc.settings.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("catalog opened", err)
	}
	request.Privacy = "cloud_allowed"
	empty, err := svc.SelectSkillComparison(ctx, request)
	if err != nil || empty.Validate() != nil || empty.Comparison != nil || empty.Baseline.Selected != 0 || empty.Candidate.Selected != 0 || empty.Watermark != report.Watermark {
		t.Fatal(empty, err)
	}
	request.Privacy = "local_only"
	last := explicit.Tasks[len(explicit.Tasks)-1]
	if err := ReviseFeedback(ctx, svc.settings.Telemetry.Database, last, last+"-evaluation", true); err != nil {
		t.Fatal(err)
	}
	corrected, err := svc.SelectSkillComparison(ctx, request)
	if err != nil || corrected.Comparison == nil || corrected.Comparison.Candidate.Accepted != 1 || corrected.Comparison.Candidate.Samples != 20 || corrected.Comparison.EvidenceDigest == report.Comparison.EvidenceDigest {
		t.Fatal(corrected, err)
	}
}

func TestSelectSkillComparisonAdmissionAndSecrets(t *testing.T) {
	svc, _, explicit := appComparisonFixture(t, 2)
	request := appComparisonSelectionRequest(explicit)
	ctx := context.Background()
	for _, change := range []func(*skills.ComparisonSelectionRequest){func(r *skills.ComparisonSelectionRequest) { r.ModelID = "unknown" }, func(r *skills.ComparisonSelectionRequest) { r.Privacy = "" }, func(r *skills.ComparisonSelectionRequest) { r.TasksPerVersion = 101 }} {
		r := request
		change(&r)
		if out, err := svc.SelectSkillComparison(ctx, r); err == nil || out.Version != 0 {
			t.Fatal(out, err)
		}
	}
	for _, at := range []int{1, 2, 3} {
		calls := 0
		svc.secret = func(string) string {
			calls++
			if calls >= at {
				return explicit.Name
			}
			return ""
		}
		if out, err := svc.SelectSkillComparison(ctx, request); err == nil || out.Version != 0 {
			t.Fatal("secret rotation", at, out, err)
		}
	}
	svc.secret = func(string) string { panic("private") }
	if out, err := svc.SelectSkillComparison(ctx, request); err != ErrInspection || out.Version != 0 {
		t.Fatal(out, err)
	}
	svc.secret = nil
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := svc.SelectSkillComparison(canceled, request); err == nil {
		t.Fatal("canceled admitted")
	}
	if _, err := svc.SelectSkillComparison(nil, request); err == nil {
		t.Fatal("nil context")
	}
	if _, err := (*Service)(nil).SelectSkillComparison(ctx, request); err == nil {
		t.Fatal("nil service")
	}
	svc.settings.Telemetry.Database = filepath.Join(t.TempDir(), "absent.db")
	if _, err := svc.SelectSkillComparison(ctx, request); err == nil {
		t.Fatal("missing database admitted")
	}
	if _, err := os.Stat(svc.settings.Telemetry.Database); !os.IsNotExist(err) {
		t.Fatal("created database", err)
	}
}
