package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

type reviewContextEstimator func(context.Context, providers.Request) (int, error)

func TestReviewerNilContextRejects(t *testing.T) {
	v := reviewer(reviewProvider(func(context.Context, providers.Request, func(providers.Chunk) error) error {
		t.Fatal("nil context reached provider")
		return nil
	}))
	if _, err := v.Review(nil, ReviewRequest{Domain: "creative", Requirements: "write", Candidate: "answer"}); err != ErrAudit {
		t.Fatal(err)
	}
}

func (f reviewContextEstimator) Estimate(ctx context.Context, r providers.Request) (int, error) {
	return f(ctx, r)
}

func TestReviewerContextEstimatorBoundsAndSanitizes(t *testing.T) {
	for _, mode := range []string{"large", "low_floor", "error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			calls, estimates := 0, 0
			v := reviewer(reviewProvider(func(context.Context, providers.Request, func(providers.Chunk) error) error { calls++; return nil }))
			if mode == "low_floor" {
				v.ContextTokens = 1
			}
			v.ContextEstimator = reviewContextEstimator(func(context.Context, providers.Request) (int, error) {
				estimates++
				switch mode {
				case "large":
					return v.ContextTokens + 1, nil
				case "low_floor":
					return 1, nil
				case "error":
					return 0, errors.New("private-estimator-payload")
				default:
					panic("private-estimator-payload")
				}
			})
			out, err := v.Review(context.Background(), ReviewRequest{Domain: "creative", Requirements: "Write a poem", Candidate: "candidate"})
			if !errors.Is(err, ErrAudit) || strings.Contains(err.Error(), "private") || out.Audit.Verdict != "" || calls != 0 || estimates != 1 {
				t.Fatal(out, err, calls, estimates)
			}
		})
	}
}

func TestReviewerContextEstimatorSeesAssembledPromptWithoutMutation(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "nil_compatibility", true: "custom"}[custom], func(t *testing.T) {
			calls, estimates := 0, 0
			var estimated providers.Request
			v := reviewer(reviewProvider(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
				calls++
				if r.Model != "reviewer" || len(r.Tools) != 0 || len(r.Messages) != 2 || r.Messages[0].Content != reviewInstructions || r.Messages[0].Role != "system" || r.Messages[1].Role != "user" || !strings.Contains(r.Messages[1].Content, "original candidate") {
					t.Fatal("mutated reviewer request", r)
				}
				if custom {
					before, _ := json.Marshal(estimated)
					after, _ := json.Marshal(r)
					if string(before) != string(after) {
						t.Fatal("estimation altered inference")
					}
				}
				return emit(providers.Chunk{Text: reviewOutput("abstain"), Done: true, FinishReason: "stop"})
			}))
			if custom {
				v.ContextEstimator = reviewContextEstimator(func(ctx context.Context, r providers.Request) (int, error) {
					estimates++
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) > v.Timeout {
						t.Fatal("estimation not bounded by reviewer timeout")
					}
					if r.Model != "reviewer" || len(r.Messages) != 2 || r.Messages[0].Content != reviewInstructions || !strings.Contains(r.Messages[1].Content, "original candidate") || !strings.Contains(r.Messages[1].Content, "test-log") {
						t.Fatal("incomplete audit prompt estimated", r)
					}
					body, _ := json.Marshal(r)
					if json.Unmarshal(body, &estimated) != nil {
						t.Fatal("snapshot failed")
					}
					r.Model = "wrong"
					r.Messages[0].Content = "injected instructions"
					r.Messages[1].Content = "injected evidence"
					return 1, nil
				})
			}
			out, err := v.Review(context.Background(), ReviewRequest{Domain: "creative", Requirements: "Write a poem", Candidate: "original candidate", Evidence: []ReviewEvidence{{ID: "test-log", Content: "recorded evidence"}}})
			if err != nil || out.Audit.Verdict != "abstain" || calls != 1 || (custom && estimates != 1) {
				t.Fatal(out, err, calls, estimates)
			}
		})
	}
}

func TestReviewerTimeoutIncludesContextEstimation(t *testing.T) {
	calls := 0
	v := reviewer(reviewProvider(func(context.Context, providers.Request, func(providers.Chunk) error) error { calls++; return nil }))
	v.Timeout = 20 * time.Millisecond
	v.ContextEstimator = reviewContextEstimator(func(ctx context.Context, _ providers.Request) (int, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > v.Timeout {
			t.Error("estimator received extended deadline")
		}
		<-ctx.Done()
		return 1, nil
	})
	if _, err := v.Review(context.Background(), ReviewRequest{Domain: "creative", Requirements: "Write"}); !errors.Is(err, context.DeadlineExceeded) || calls != 0 {
		t.Fatal(err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	v.ContextEstimator = reviewContextEstimator(func(context.Context, providers.Request) (int, error) {
		t.Error("canceled review estimated")
		return 1, nil
	})
	if _, err := v.Review(ctx, ReviewRequest{Domain: "creative", Requirements: "Write"}); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal(err, calls)
	}
}
