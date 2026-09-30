package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func appComparisonFixture(t *testing.T, count int) (*Service, *telemetry.Store, skills.ComparisonRequest) {
	t.Helper()
	svc, cfg := autoFixture(t)
	svc.settings.Skills.Scope = "project"
	svc.settings.Skills.Enabled = false
	svc.settings.Skills.Root = filepath.Join(t.TempDir(), "absent-catalog")
	svc.settings.Models[0].Model = "actual:tag"
	db, err := telemetry.Open(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	r := skills.ComparisonRequest{Version: 1, ModelID: "a", Domain: "creative", Profile: "default", Name: "workflow", BaselineVersion: strings.Repeat("a", 32), CandidateVersion: strings.Repeat("b", 32), Source: evaluation.UserFeedback, MinSamples: 20, MinDrop: .1}
	for i := 0; i < count; i++ {
		task := fmt.Sprintf("task-%03d", i)
		r.Tasks = append(r.Tasks, task)
		version, digest := r.BaselineVersion, strings.Repeat("c", 64)
		passed := i < count/2
		if !passed {
			version, digest = r.CandidateVersion, strings.Repeat("d", 64)
		}
		for n, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted} {
			e := runtime.Event{Version: 1, ID: fmt.Sprintf("%s-%d", task, n), TaskID: task, SessionID: task, CorrelationID: task, Sequence: int64(n + 1), Time: time.Unix(100, 0).UTC(), Kind: kind}
			if n > 0 {
				e.AttemptID = task + "-attempt"
				e.TurnID = task + "-turn"
			}
			if n == 0 {
				e.Data = runtime.Data{Domain: r.Domain, Profile: r.Profile, Privacy: "local_only", SkillContext: &runtime.SkillContextUse{Version: 1, Complete: true, References: []runtime.SkillReference{{Scope: "project", Name: r.Name, Version: version, Digest: digest}}}}
			}
			if n == 1 {
				e.Data.ModelID = "actual:tag"
				e.Data.ProviderID = "local"
			}
			if n == 2 {
				e.Data.Text = "private-result"
				e.Data.FinishReason = "stop"
			}
			if err = db.Append(context.Background(), int64(n), e); err != nil {
				t.Fatal(err)
			}
		}
		record := evaluation.Record{Version: 1, ID: task + "-evaluation", TaskID: task, AttemptID: task + "-attempt", Key: routing.Key{Model: "actual:tag", Provider: "local", Domain: r.Domain, Profile: r.Profile}, Checks: []evaluation.Check{{Source: evaluation.UserFeedback, Reference: task + "-feedback", Passed: passed}}, ExecutionSucceeded: true, Time: time.Unix(100, 0).UTC()}
		if err = db.RecordEvaluation(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	return svc, db, r
}

func TestCompareSkillOutcomesRealStorageAndNoMutation(t *testing.T) {
	svc, db, r := appComparisonFixture(t, 40)
	ctx := context.Background()
	before, err := db.SkillTaskOutcomes(ctx, r.Tasks)
	if err != nil {
		t.Fatal(err)
	}
	svc.settings.Skills.Enabled = false
	svc.settings.Skills.Root = filepath.Join(t.TempDir(), "absent-catalog")
	out, err := svc.CompareSkillOutcomes(ctx, r)
	if out.ConfiguredModelID != r.ModelID {
		t.Fatal("configured model binding lost", out, err)
	}
	if err != nil || out.Validate() != nil || out.Baseline.Samples != 20 || out.Baseline.Accepted != 20 || out.Candidate.Samples != 20 || out.Candidate.Accepted != 0 || out.Policy.Execution.Model != "actual:tag" || !out.AdvisoryOnly {
		t.Fatal(out, err)
	}
	after, err := db.SkillTaskOutcomes(ctx, r.Tasks)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("comparison mutated evidence", err)
	}
	if _, err = os.Stat(svc.settings.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("catalog created", err)
	}
	// A later user correction changes this observation, not stored sample count.
	last := r.Tasks[len(r.Tasks)-1]
	if err = ReviseFeedback(ctx, svc.settings.Telemetry.Database, last, last+"-evaluation", true); err != nil {
		t.Fatal(err)
	}
	next, err := svc.CompareSkillOutcomes(ctx, r)
	if err != nil || next.Candidate.Accepted != 1 || next.Candidate.Samples != 20 || next.EvidenceDigest == out.EvidenceDigest {
		t.Fatal(next, err)
	}
}

func TestCompareSkillOutcomesGuardsAndRotation(t *testing.T) {
	svc, _, r := appComparisonFixture(t, 2)
	ctx := context.Background()
	for _, mutate := range []func(*skills.ComparisonRequest){func(r *skills.ComparisonRequest) { r.ModelID = "missing" }, func(r *skills.ComparisonRequest) { r.Name = "bad/name" }, func(r *skills.ComparisonRequest) { r.Tasks = []string{"missing"} }, func(r *skills.ComparisonRequest) { r.Tasks = []string{r.Tasks[0], r.Tasks[0]} }} {
		next := r
		mutate(&next)
		if out, err := svc.CompareSkillOutcomes(ctx, next); err == nil || out.Version != 0 {
			t.Fatal(out, err)
		}
	}
	svc.settings.Skills.Scope = "other"
	if out, err := svc.CompareSkillOutcomes(ctx, r); err == nil || out.Version != 0 {
		t.Fatal("crossscope", out, err)
	}
	svc.settings.Skills.Scope = "project"
	if err := r.Validate(); err != nil {
		t.Fatal("fixture request", err)
	}
	if err := svc.settings.Validate(); err != nil {
		t.Fatal("fixture config", err)
	}
	for _, rotateAt := range []int32{1, 2, 3} {
		var calls atomic.Int32
		svc.secret = func(string) string {
			if calls.Add(1) >= rotateAt {
				return "workflow"
			}
			return ""
		}
		if out, err := svc.CompareSkillOutcomes(ctx, r); err == nil || out.Version != 0 {
			t.Fatal("rotation escaped", rotateAt, out, err)
		}
	}
	svc.secret = func(string) string { panic("private-error") }
	if out, err := svc.CompareSkillOutcomes(ctx, r); err != ErrInspection || out.Version != 0 {
		t.Fatal(out, err)
	}
	svc.secret = nil
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := svc.CompareSkillOutcomes(canceled, r); err == nil {
		t.Fatal("canceled")
	}
	if _, err := svc.CompareSkillOutcomes(nil, r); err == nil {
		t.Fatal("nil context")
	}
	if _, err := (*Service)(nil).CompareSkillOutcomes(ctx, r); err == nil {
		t.Fatal("nil service")
	}
	svc.settings.Telemetry.Database = filepath.Join(t.TempDir(), "missing.db")
	if _, err := svc.CompareSkillOutcomes(ctx, r); err == nil {
		t.Fatal("missing accepted")
	}
	if _, err := os.Stat(svc.settings.Telemetry.Database); !os.IsNotExist(err) {
		t.Fatal("database created", err)
	}
}
