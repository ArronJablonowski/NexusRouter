package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func summaryValidationRegistry(t *testing.T, validator sessions.SummaryValidator) *sessions.SummaryValidatorRegistry {
	t.Helper()
	registry, err := sessions.NewSummaryValidatorRegistry(map[string]sessions.SummaryValidator{"project-tests-v1": validator})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestValidateSummaryPersistsBoundEvidenceAndRetryDoesNotReinvoke(t *testing.T) {
	svc, db, task := codexCompactionFixture(t)
	attempt := codexCompactionDraft(t, svc, task)
	var calls atomic.Int32
	registry := summaryValidationRegistry(t, sessions.SummaryValidatorFunc(func(_ context.Context, input sessions.SummaryValidationInput) (sessions.SummaryValidationDecision, error) {
		calls.Add(1)
		if input.AttemptID != attempt.ID || input.TaskID != task || input.SourceDigest != attempt.SourceDigest || input.DraftDigest == "" || input.Source.Sequence != attempt.SourceSequence {
			t.Errorf("incorrect immutable input: %+v", input)
		}
		// Trusted callbacks still cannot mutate the durable binding used later.
		input.Draft.Request.Summary.Requirements[0] = "forged"
		input.Source.Messages[0].Content = "forged"
		return sessions.SummaryValidationDecision{Decision: "approved", Note: "project checks passed"}, nil
	}))
	review, err := svc.ValidateSummary(context.Background(), attempt.ID, "", "validate-operation", "project-tests-v1", registry)
	if err != nil || review.Version != 2 || review.Decision != "approved" || review.SourceDigest != attempt.SourceDigest || review.DraftDigest == "" || calls.Load() != 1 {
		t.Fatalf("review=%+v calls=%d err=%v", review, calls.Load(), err)
	}
	retry, err := svc.ValidateSummary(context.Background(), attempt.ID, "", "validate-operation", "project-tests-v1", registry)
	if err != nil || retry != review || calls.Load() != 1 {
		t.Fatalf("lost-ack retry=%+v calls=%d err=%v", retry, calls.Load(), err)
	}
	saved, err := db.CurrentSummaryReview(context.Background(), attempt.ID)
	if err != nil || saved != review {
		t.Fatal("bound validation evidence unavailable", saved, err)
	}
	current, err := db.SummaryAttempt(context.Background(), attempt.ID)
	if err != nil || current.Draft.Request.Summary.Requirements[0] == "forged" {
		t.Fatal("validator mutated durable draft", err)
	}
}

func TestValidateSummaryAbstentionAndCASRemainInactive(t *testing.T) {
	svc, db, task := codexCompactionFixture(t)
	attempt := codexCompactionDraft(t, svc, task)
	registry := summaryValidationRegistry(t, sessions.SummaryValidatorFunc(func(context.Context, sessions.SummaryValidationInput) (sessions.SummaryValidationDecision, error) {
		return sessions.SummaryValidationDecision{Decision: "abstained", Note: "domain unsupported"}, nil
	}))
	review, err := svc.ValidateSummary(context.Background(), attempt.ID, "", "abstain-operation", "project-tests-v1", registry)
	if err != nil || review.Decision != "abstained" {
		t.Fatal(review, err)
	}
	if _, _, err := db.LatestApprovedSummary(context.Background(), task); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("abstention activated summary", err)
	}
	secondRegistry := summaryValidationRegistry(t, sessions.SummaryValidatorFunc(func(context.Context, sessions.SummaryValidationInput) (sessions.SummaryValidationDecision, error) {
		return sessions.SummaryValidationDecision{Decision: "approved", Note: "checks passed"}, nil
	}))
	if _, err := svc.ValidateSummary(context.Background(), attempt.ID, "", "stale-operation", "project-tests-v1", secondRegistry); !errors.Is(err, telemetry.ErrConflict) {
		t.Fatal("stale compare-and-swap accepted", err)
	}
}

func TestStockSummaryIntegrityEvidenceNeverSelfApproves(t *testing.T) {
	svc, db, task := codexCompactionFixture(t)
	attempt := codexCompactionDraft(t, svc, task)
	registry, err := sessions.NewSummaryValidatorRegistry(map[string]sessions.SummaryValidator{
		sessions.SummaryIntegrityValidatorID: sessions.NewSummaryIntegrityValidator(),
	})
	if err != nil {
		t.Fatal(err)
	}
	review, err := svc.ValidateSummary(context.Background(), attempt.ID, "", "stock-integrity-operation", sessions.SummaryIntegrityValidatorID, registry)
	if err != nil || review.Decision != "abstained" || !strings.Contains(review.Note, "approval=false") {
		t.Fatalf("review=%+v err=%v", review, err)
	}
	if _, _, err := db.LatestApprovedSummary(context.Background(), task); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("stock linter authorized continuation", err)
	}
	approved, err := svc.ReviewSummary(context.Background(), attempt.ID, review.ID, "approved", "Operator compared the draft with its source")
	if err != nil || approved.PreviousID != review.ID || approved.Decision != "approved" {
		t.Fatal("operator could not supersede advisory evidence", approved, err)
	}
	if _, current, err := db.LatestApprovedSummary(context.Background(), task); err != nil || current.ID != approved.ID {
		t.Fatal("explicit operator approval did not become current", current, err)
	}
}

func TestValidateSummaryContainsValidatorFailureAndPanic(t *testing.T) {
	for _, mode := range []string{"error", "panic", "invalid", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			svc, db, task := codexCompactionFixture(t)
			attempt := codexCompactionDraft(t, svc, task)
			registry := summaryValidationRegistry(t, sessions.SummaryValidatorFunc(func(callbackCtx context.Context, _ sessions.SummaryValidationInput) (sessions.SummaryValidationDecision, error) {
				switch mode {
				case "error":
					return sessions.SummaryValidationDecision{}, errors.New("private validator failure")
				case "panic":
					panic("private validator panic")
				case "timeout":
					<-callbackCtx.Done()
					return sessions.SummaryValidationDecision{}, callbackCtx.Err()
				default:
					return sessions.SummaryValidationDecision{Decision: "approved", Note: ""}, nil
				}
			}))
			ctx := context.Background()
			if mode == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 10*time.Millisecond)
				defer cancel()
			}
			if _, err := svc.ValidateSummary(ctx, attempt.ID, "", "failed-operation", "project-tests-v1", registry); err != ErrAdmission {
				t.Fatal("validator failure escaped", err)
			}
			if _, err := db.CurrentSummaryReview(context.Background(), attempt.ID); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("validator failure persisted approval", err)
			}
		})
	}
}
