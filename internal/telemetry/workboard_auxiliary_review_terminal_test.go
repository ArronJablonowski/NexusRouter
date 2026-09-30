package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/workboard"
)

// DAR-91 requires every indeterminate auxiliary-review exit to retain the
// entire admitted reservation. These cases exercise the service boundary, not
// just the settlement constructor, so they also prove one durable admission,
// one terminal settlement, no candidate, and no hidden redispatch.
func TestAuxiliaryReviewIndeterminateTerminalsSettleConservatively(t *testing.T) {
	tests := []struct {
		name        string
		evaluator   func() *budgetedReviewFixture
		run         func(context.Context, context.CancelFunc, *workboard.EvaluationService, workboard.SubmitCandidateRequest, *budgetedReviewFixture) error
		disposition workboard.AuxiliaryReviewDisposition
		wantError   error
	}{
		{
			name: "canceled",
			evaluator: func() *budgetedReviewFixture {
				return &budgetedReviewFixture{entered: make(chan struct{}, 1), release: make(chan struct{})}
			},
			run: func(ctx context.Context, cancel context.CancelFunc, service *workboard.EvaluationService, request workboard.SubmitCandidateRequest, evaluator *budgetedReviewFixture) error {
				result := make(chan error, 1)
				go func() {
					_, err := service.SubmitCandidate(ctx, request)
					result <- err
				}()
				select {
				case <-evaluator.entered:
					cancel()
				case <-time.After(5 * time.Second):
					return errors.New("review did not start")
				}
				return <-result
			},
			disposition: workboard.AuxiliaryReviewCanceled,
			wantError:   context.Canceled,
		},
		{
			name: "timeout",
			evaluator: func() *budgetedReviewFixture {
				return &budgetedReviewFixture{timeLimitMS: 100, release: make(chan struct{})}
			},
			run: func(ctx context.Context, _ context.CancelFunc, service *workboard.EvaluationService, request workboard.SubmitCandidateRequest, _ *budgetedReviewFixture) error {
				_, err := service.SubmitCandidate(ctx, request)
				return err
			},
			disposition: workboard.AuxiliaryReviewFailed,
			wantError:   context.DeadlineExceeded,
		},
		{
			name:      "panic",
			evaluator: func() *budgetedReviewFixture { return &budgetedReviewFixture{panic: true} },
			run: func(ctx context.Context, _ context.CancelFunc, service *workboard.EvaluationService, request workboard.SubmitCandidateRequest, _ *budgetedReviewFixture) error {
				_, err := service.SubmitCandidate(ctx, request)
				return err
			},
			disposition: workboard.AuxiliaryReviewFailed,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := openExecutionAdmissionStore(t, base)
			defer store.Close()
			clock := time.Date(2026, 9, 13, 19, 0, 0, 0, time.UTC)
			evaluator := test.evaluator()
			service, request := prepareBudgetedReviewService(t, store, evaluator, &clock, "terminal-"+test.name)

			err := test.run(base, cancel, service, request, evaluator)
			if err == nil || test.wantError != nil && !errors.Is(err, test.wantError) {
				t.Fatalf("terminal error=%v want=%v", err, test.wantError)
			}
			if calls := evaluator.calls.Load(); calls != 1 {
				t.Fatalf("review dispatches=%d want=1", calls)
			}
			var admissions, settlements, candidates int
			if err = store.db.QueryRow(`SELECT count(*) FROM workboard_auxiliary_review_admissions`).Scan(&admissions); err != nil || admissions != 1 {
				t.Fatalf("admissions=%d err=%v", admissions, err)
			}
			if err = store.db.QueryRow(`SELECT count(*) FROM workboard_auxiliary_review_settlements`).Scan(&settlements); err != nil || settlements != 1 {
				t.Fatalf("settlements=%d err=%v", settlements, err)
			}
			if err = store.db.QueryRow(`SELECT count(*) FROM workboard_candidates`).Scan(&candidates); err != nil || candidates != 0 {
				t.Fatalf("candidates=%d err=%v", candidates, err)
			}
			var admissionID string
			if err = store.db.QueryRow(`SELECT admission_id FROM workboard_auxiliary_review_admissions`).Scan(&admissionID); err != nil {
				t.Fatal(err)
			}
			tx, err := store.db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			settlement, found, readErr := readAuxiliaryReviewSettlement(context.Background(), tx, admissionID)
			_ = tx.Rollback()
			if readErr != nil || !found || settlement.Disposition != test.disposition ||
				settlement.ChargedTimeMS != settlement.TimeLimitMS || settlement.ChargedTokens != settlement.TokenLimit ||
				settlement.ChargedCostMicros != settlement.CostMicros ||
				settlement.TimeChargeMode != workboard.AuxiliaryReviewConservative ||
				settlement.TokenChargeMode != workboard.AuxiliaryReviewConservative ||
				settlement.CostChargeMode != workboard.AuxiliaryReviewConservative {
				t.Fatalf("settlement=%+v found=%v err=%v", settlement, found, readErr)
			}
		})
	}
}
