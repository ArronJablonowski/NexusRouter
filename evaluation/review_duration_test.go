package evaluation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

func TestReviewDurationBoundaryIsSharedAcrossContracts(t *testing.T) {
	providerCalls := 0
	p := reviewProvider(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		providerCalls++
		if err := emit(providers.Chunk{Text: reviewOutput("abstain")}); err != nil {
			return err
		}
		return emit(providers.Chunk{Done: true, FinishReason: "stop", Usage: &providers.Usage{}})
	})
	reviewer := reviewer(p)
	reviewer.Timeout = MaxReviewDuration
	if _, err := reviewer.Review(context.Background(), ReviewRequest{Domain: "creative", Requirements: "Write"}); err != nil || providerCalls != 1 {
		t.Fatalf("maximum reviewer duration rejected: calls=%d err=%v", providerCalls, err)
	}
	reviewer.Timeout++
	if _, err := reviewer.Review(context.Background(), ReviewRequest{Domain: "creative", Requirements: "Write"}); !errors.Is(err, ErrAudit) || providerCalls != 1 {
		t.Fatalf("overlong reviewer duration dispatched: calls=%d err=%v", providerCalls, err)
	}

	evaluator, request := validEvaluatorFixture()
	if _, err := InvokeEvaluator(context.Background(), evaluator, request, MaxReviewDuration); err != nil {
		t.Fatalf("maximum evaluator duration rejected: %v", err)
	}
	if _, err := InvokeEvaluator(context.Background(), evaluator, request, MaxReviewDuration+time.Nanosecond); !errors.Is(err, ErrEvaluator) {
		t.Fatalf("overlong evaluator duration accepted: %v", err)
	}

	started := time.Unix(100, 0).UTC()
	audit := AuditRecord{Version: 1, ID: "audit", TaskID: "task", AttemptID: "attempt", EvaluatorModel: "model", EvaluatorProvider: "provider",
		Audit:        Audit{Version: 1, EvaluatorID: "reviewer", RubricVersion: "rubric", Domain: "creative", Verdict: "abstain", Findings: []AuditFinding{}},
		EvidenceRefs: []string{}, Elapsed: MaxReviewDuration, Time: started.Add(MaxReviewDuration)}
	if err := audit.Validate(); err != nil {
		t.Fatalf("maximum audit duration rejected: %v", err)
	}
	attempt := ReviewAttempt{Version: 1, ID: "review", TaskID: "task", AttemptID: "attempt", EvaluatorModel: "model", EvaluatorProvider: "provider",
		ReviewerID: "reviewer", RequestDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Status: "completed", AuditID: audit.ID, StartedAt: started, FinishedAt: started.Add(MaxReviewDuration)}
	if status, err := NewAuditStatus(attempt, &audit); err != nil || status.ElapsedMillis != MaxReviewDuration.Milliseconds() {
		t.Fatalf("maximum public audit duration rejected: %+v %v", status, err)
	}

	failed := attempt
	failed.Status, failed.Code, failed.AuditID = "failed", "review_failed", ""
	failed.Elapsed, failed.Usage = MaxReviewDuration, &providers.Usage{}
	if err := failed.Validate(); err != nil {
		t.Fatalf("maximum failed-attempt duration rejected: %v", err)
	}
	audit.Elapsed++
	if err := audit.Validate(); err == nil {
		t.Fatal("overlong audit duration accepted")
	}
}

func TestReviewCostIsBoundedByDurableMicroCostRange(t *testing.T) {
	request := AuditRequest{Version: 1, IdempotencyKey: "0123456789abcdef", TaskID: "task", ReviewerModelID: "reviewer", MaxCost: MaxReviewCost}
	if err := request.Validate(); err != nil {
		t.Fatalf("maximum representable audit cost rejected: %v", err)
	}
	attempt := ReviewAttempt{Version: 1, ID: "review", TaskID: "task", AttemptID: "attempt", EvaluatorModel: "model", EvaluatorProvider: "provider",
		EstimatedCost: MaxReviewCost, Status: "started", StartedAt: time.Unix(100, 0).UTC()}
	if err := attempt.Validate(); err != nil {
		t.Fatalf("maximum representable attempt cost rejected: %v", err)
	}
	request.MaxCost = MaxReviewCost + 1
	attempt.EstimatedCost = MaxReviewCost + 1
	if request.Validate() == nil || attempt.Validate() == nil {
		t.Fatal("unrepresentable review cost accepted")
	}
}
