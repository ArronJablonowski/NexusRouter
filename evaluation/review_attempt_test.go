package evaluation

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestReviewAttemptValidation(t *testing.T) {
	base := ReviewAttempt{Version: 1, ID: "review", TaskID: "task", AttemptID: "attempt", EvaluatorModel: "model", EvaluatorProvider: "provider", Status: "started", StartedAt: time.Unix(100, 0)}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(base)
	if err != nil || strings.Contains(string(body), "ReviewerID") || strings.Contains(string(body), "RequestDigest") {
		t.Fatal("legacy encoding changed", string(body), err)
	}
	public := base
	public.ReviewerID, public.RequestDigest = "reviewer", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := public.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"review_failed", "canceled", "persistence_failed"} {
		r := base
		r.Status, r.Code, r.FinishedAt = "failed", code, base.StartedAt.Add(time.Second)
		if err := r.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, mutate := range []func(*ReviewAttempt){
		func(r *ReviewAttempt) { r.Version = 2 }, func(r *ReviewAttempt) { r.ID = "" }, func(r *ReviewAttempt) { r.Status = "unknown" },
		func(r *ReviewAttempt) { r.Code = "raw secret" }, func(r *ReviewAttempt) { r.AuditID = "audit" }, func(r *ReviewAttempt) { r.FinishedAt = r.StartedAt },
		func(r *ReviewAttempt) { r.Status = "completed"; r.FinishedAt = r.StartedAt },
		func(r *ReviewAttempt) { r.Status = "failed"; r.FinishedAt = r.StartedAt; r.Code = "raw error" },
		func(r *ReviewAttempt) {
			r.Status = "failed"
			r.FinishedAt = r.StartedAt.Add(-time.Second)
			r.Code = "canceled"
		},
		func(r *ReviewAttempt) { r.ReviewerID = "reviewer" },
		func(r *ReviewAttempt) {
			r.RequestDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		},
		func(r *ReviewAttempt) {
			r.ReviewerID, r.RequestDigest = "reviewer", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		},
	} {
		r := base
		mutate(&r)
		if err := r.Validate(); err == nil {
			t.Fatal("invalid record accepted", r)
		}
	}
}
