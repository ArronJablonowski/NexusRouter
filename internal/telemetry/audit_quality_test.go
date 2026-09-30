package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func qualityTask(t *testing.T, s *Store, task, model, profile, routeProfile string) evaluation.AuditRecord {
	t.Helper()
	kinds := []runtime.Kind{runtime.TaskStarted}
	if routeProfile != "" {
		kinds = append(kinds, runtime.RouteSelected)
	}
	kinds = append(kinds, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted)
	for i, kind := range kinds {
		e := event(fmt.Sprintf("%s-%d", task, i), int64(i+1), kind)
		e.TaskID = task
		e.TurnID, e.AttemptID = "turn", "attempt"
		e.Data = runtime.Data{ModelID: model, ProviderID: "candidate-provider"}
		if kind == runtime.TaskStarted {
			e.Data.Domain = "code"
			e.Data.Profile = profile
		}
		if kind == runtime.RouteSelected {
			e.RouteID = "route"
			e.Data.Domain = "code"
			e.Data.Profile = routeProfile
		}
		if err := s.Append(context.Background(), int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	r := storedAudit()
	r.ID, r.TaskID = task+"-audit", task
	r.Audit.Verdict, r.Audit.Confidence = "accept", .8
	r.Audit.Findings = []evaluation.AuditFinding{{Summary: "review", EvidenceRefs: []string{"evidence-1"}}}
	return r
}

func TestAuditQualityLatestAndFeedbackDominance(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "quality.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key := routing.Key{Model: "candidate", Provider: "candidate-provider", Domain: "code", Profile: "default"}
	a := qualityTask(t, s, "a", "candidate", "", " ")
	// A nonempty route profile is deliberately separate from default.
	a.Audit.Domain = "other"
	if err := s.RecordAudit(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := qualityTask(t, s, "b", "candidate", "", " ")
	b.Audit.Domain = "other"
	if err := s.RecordAudit(ctx, b); err != nil {
		t.Fatal(err)
	}
	c := qualityTask(t, s, "c", "candidate", "", "")
	if err := s.RecordAudit(ctx, c); err != nil {
		t.Fatal(err)
	}
	d := qualityTask(t, s, "d", "candidate", "", "")
	d.Audit.Verdict, d.Audit.Confidence = "reject", .2
	d.Usage = &providers.Usage{InputTokens: 9000, OutputTokens: 1000}
	d.Time = time.Unix(200, 0).UTC()
	if err := s.RecordAudit(ctx, d); err != nil {
		t.Fatal(err)
	}
	out, err := s.AuditQuality(ctx, key)
	if err != nil || out.Samples != 2 || math.Abs(out.Quality-.8) > 1e-12 || out.Confidence != .5 || !out.Updated.Equal(d.Time) {
		t.Fatal(out, err)
	}
	if _, err := s.Fitness(ctx, key); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("review usage created candidate fitness", err)
	}
	c.ID = "c-new"
	c.Audit.Verdict = "abstain"
	if err := s.RecordAudit(ctx, c); err != nil {
		t.Fatal(err)
	}
	out, err = s.AuditQuality(ctx, key)
	if err != nil || out.Samples != 1 || out.Quality != 0 {
		t.Fatal("abstention resurrected old accept", out, err)
	}
	r := evaluation.Record{Version: 1, ID: "feedback", TaskID: "d", AttemptID: "attempt", Key: key, Checks: []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "user", Passed: true}}, Cost: .25, Time: time.Unix(300, 0)}
	if err := s.RecordEvaluation(ctx, r); err != nil {
		t.Fatal(err)
	}
	out, err = s.AuditQuality(ctx, key)
	if err != nil || out != (routing.Advisory{}) {
		t.Fatal("feedback did not dominate", out, err)
	}
	f, err := s.Fitness(ctx, key)
	if err != nil || f.Samples != 1 || f.Quality != 1 || f.Cost != .25 {
		t.Fatal("advisory mutated fitness", f, err)
	}
}

func TestAuditQualityAttributionAndBound(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "quality.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key := routing.Key{Model: "candidate", Provider: "candidate-provider", Domain: "code", Profile: "routed"}
	r := qualityTask(t, s, "route", "candidate", "initial", "routed")
	if err := s.RecordAudit(ctx, r); err != nil {
		t.Fatal(err)
	}
	for _, k := range []routing.Key{
		{Model: "independent-reviewer", Provider: key.Provider, Domain: key.Domain, Profile: key.Profile},
		{Model: key.Model, Provider: "review-provider", Domain: key.Domain, Profile: key.Profile},
		{Model: key.Model, Provider: key.Provider, Domain: "other", Profile: key.Profile},
		{Model: key.Model, Provider: key.Provider, Domain: key.Domain, Profile: "initial"},
	} {
		if out, err := s.AuditQuality(ctx, k); err != nil || out.Samples != 0 {
			t.Fatal(k, out, err)
		}
	}
	out, err := s.AuditQuality(ctx, key)
	if err != nil || out.Samples != 1 {
		t.Fatal(out, err)
	}
	r.ID = "route-new"
	r.Audit.Verdict = "reject"
	if err := s.RecordAudit(ctx, r); err != nil {
		t.Fatal(err)
	}
	out, err = s.AuditQuality(ctx, key)
	if err != nil || out.Samples != 1 || out.Quality != 0 {
		t.Fatal("repeated attempt counted", out, err)
	}
	r.ID = "route-zero"
	r.Audit.Confidence = 0
	if err := s.RecordAudit(ctx, r); err != nil {
		t.Fatal(err)
	}
	out, err = s.AuditQuality(ctx, key)
	if err != nil || out != (routing.Advisory{}) {
		t.Fatal("zero confidence counted", out, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE audit_records SET body=json_set(body,'$.ID','forged') WHERE id='route-zero'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuditQuality(ctx, key); err == nil {
		t.Fatal("invalid stored record accepted")
	}
	for i := 0; i < 101; i++ {
		r := qualityTask(t, s, fmt.Sprintf("t%d", i), "candidate", "bounded", "")
		if i == 0 {
			r.Audit.Verdict = "reject"
		}
		if err := s.RecordAudit(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	key.Profile = "bounded"
	out, err = s.AuditQuality(ctx, key)
	if err != nil || out.Samples != 100 || out.Quality != 1 {
		t.Fatal("not bounded to latest 100", out, err)
	}
}

func TestAuditQualitySameModelCanWarnButNeverCreatePositiveSignal(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "self-review.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key := routing.Key{Model: "candidate", Provider: "candidate-provider", Domain: "code", Profile: "default"}
	independent := qualityTask(t, s, "self-reviewed", key.Model, "", "")
	independent.Audit.Verdict, independent.Audit.Confidence = "reject", .8
	if err := s.RecordAudit(ctx, independent); err != nil {
		t.Fatal(err)
	}
	self := independent
	self.ID, self.EvaluatorModel, self.EvaluatorProvider = "self-accept", key.Model, key.Provider
	self.Audit.Verdict, self.Audit.Confidence = "accept", 1
	if err := s.RecordAudit(ctx, self); err != nil {
		t.Fatal(err)
	}
	out, err := s.AuditQuality(ctx, key)
	if err != nil || out.Samples != 1 || out.Quality != 0 || out.Confidence != .8 {
		t.Fatal("self acceptance displaced independent warning", out, err)
	}
	self.ID, self.Audit.Verdict = "self-reject", "reject"
	if err := s.RecordAudit(ctx, self); err != nil {
		t.Fatal(err)
	}
	out, err = s.AuditQuality(ctx, key)
	if err != nil || out.Samples != 1 || out.Quality != 0 || out.Confidence != .25 {
		t.Fatal("same-model warning not capped", out, err)
	}
	feedback := evaluation.Record{Version: 1, ID: "user-feedback", TaskID: self.TaskID, AttemptID: self.AttemptID, Key: key, Checks: []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "user", Passed: true}}, Time: time.Now().UTC()}
	if err := s.RecordEvaluation(ctx, feedback); err != nil {
		t.Fatal(err)
	}
	if out, err = s.AuditQuality(ctx, key); err != nil || out != (routing.Advisory{}) {
		t.Fatal("user feedback did not supersede same-model advice", out, err)
	}
}
