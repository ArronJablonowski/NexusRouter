package evaluation

import (
	"math"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

func publicReviewAttempt(status, code string) ReviewAttempt {
	start := time.Unix(100, 0).UTC()
	r := ReviewAttempt{Version: 1, ID: "operation", TaskID: "task", AttemptID: "attempt", EvaluatorModel: "family/reviewer:model", EvaluatorProvider: "provider", ReviewerID: "reviewer", RequestDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: status, Code: code, StartedAt: start}
	if status != "started" {
		r.FinishedAt = start.Add(time.Second)
	}
	return r
}

func publicAudit(verdict string) AuditRecord {
	findings := []AuditFinding{}
	if verdict != "abstain" {
		findings = []AuditFinding{{Summary: "bounded finding", EvidenceRefs: []string{"candidate"}}}
	}
	return AuditRecord{Version: 1, ID: "audit", TaskID: "task", AttemptID: "attempt", EvaluatorModel: "family/reviewer:model", EvaluatorProvider: "provider", Audit: Audit{Version: 1, EvaluatorID: "reviewer", RubricVersion: "rubric", Domain: "code", Verdict: verdict, Confidence: .5, Findings: findings}, EvidenceRefs: []string{"candidate"}, Usage: &providers.Usage{InputTokens: 10, OutputTokens: 2}, Elapsed: time.Second, Time: time.Unix(101, 0).UTC()}
}

func TestAuditRequestValidation(t *testing.T) {
	valid := AuditRequest{Version: 1, IdempotencyKey: "0123456789abcdef", TaskID: "task", ReviewerModelID: "reviewer", MaxCost: 1}
	if valid.Validate() != nil {
		t.Fatal("valid request rejected")
	}
	for _, mutate := range []func(*AuditRequest){
		func(r *AuditRequest) { r.Version = 2 },
		func(r *AuditRequest) { r.IdempotencyKey = "short" },
		func(r *AuditRequest) { r.IdempotencyKey = "0123456789abcde " },
		func(r *AuditRequest) { r.TaskID = "" },
		func(r *AuditRequest) { r.TaskID = "task/other" },
		func(r *AuditRequest) { r.TaskID = "task?other" },
		func(r *AuditRequest) { r.TaskID = "task\\other" },
		func(r *AuditRequest) { r.TaskID = "täsk" },
		func(r *AuditRequest) { r.ReviewerModelID = "provider:model" },
		func(r *AuditRequest) { r.ReviewerModelID = "" },
		func(r *AuditRequest) { r.MaxCost = -1 },
		func(r *AuditRequest) { r.MaxCost = math.NaN() },
	} {
		r := valid
		mutate(&r)
		if r.Validate() == nil {
			t.Fatal("invalid request accepted", r)
		}
	}
}

func TestAuditStatusExactLifecycleMapping(t *testing.T) {
	for _, tc := range []struct {
		attempt ReviewAttempt
		audit   *AuditRecord
		want    string
	}{
		{publicReviewAttempt("started", ""), nil, "pending"},
		{publicReviewAttempt("failed", "canceled"), nil, "canceled"},
		{publicReviewAttempt("failed", "review_failed"), nil, "failed"},
		{publicReviewAttempt("failed", "persistence_failed"), nil, "failed"},
	} {
		got, err := NewAuditStatus(tc.attempt, tc.audit)
		if err != nil || got.Status != tc.want || (got.Status == "pending") != (got.TerminalDisposition == "") {
			t.Fatal(tc.want, got, err)
		}
	}
	for verdict, want := range map[string]string{"accept": "completed", "reject": "rejected", "abstain": "abstained"} {
		a := publicAudit(verdict)
		r := publicReviewAttempt("completed", "")
		r.AuditID = a.ID
		got, err := NewAuditStatus(r, &a)
		if err != nil || got.Status != want || got.TerminalDisposition != want || got.AuditID != a.ID || got.Usage == a.Usage {
			t.Fatal(verdict, got, err)
		}
	}
}

func TestAuditStatusAndEventValidationFailClosed(t *testing.T) {
	pending, err := NewAuditStatus(publicReviewAttempt("started", ""), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := AuditEvidencePrecedence(); len(got) != 4 {
		t.Fatal(got)
	} else {
		got[0] = LLMJudge
		if AuditEvidencePrecedence()[0] != Deterministic {
			t.Fatal("global precedence mutated")
		}
	}
	for _, mutate := range []func(*AuditStatus){
		func(s *AuditStatus) { s.EvidencePrecedence[0] = LLMJudge },
		func(s *AuditStatus) { s.ElapsedMillis = 1 },
		func(s *AuditStatus) { s.Status = "completed" },
		func(s *AuditStatus) { s.ReviewerID = "" },
		func(s *AuditStatus) { s.TaskID = "task/other" },
	} {
		s := pending
		s.EvidencePrecedence = AuditEvidencePrecedence()
		mutate(&s)
		if s.Validate() == nil {
			t.Fatal("invalid status accepted", s)
		}
	}
	e := AuditEvent{Version: 1, AuditID: pending.ID, Sequence: 1, Status: pending}
	p := AuditEventPage{Version: 1, AuditID: pending.ID, FromSequence: 0, NextSequence: 1, HeadSequence: 1, Events: []AuditEvent{e}}
	if e.Validate() != nil || p.Validate() != nil {
		t.Fatal(e.Validate(), p.Validate())
	}
	p.Events[0].Sequence = 2
	if p.Validate() == nil {
		t.Fatal("noncontiguous event accepted")
	}
	p.Events[0].Sequence = 3
	if p.Events[0].Validate() == nil {
		t.Fatal("third lifecycle event accepted")
	}
	r := publicReviewAttempt("completed", "")
	a := publicAudit("accept")
	r.AuditID = a.ID
	a.Time = r.FinishedAt.Add(time.Nanosecond)
	if _, err := NewAuditStatus(r, &a); err == nil {
		t.Fatal("audit outside durable lifecycle accepted")
	}
}
