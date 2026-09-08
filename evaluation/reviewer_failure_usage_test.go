package evaluation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

func TestReviewerPreservesOnlyVerifiedTerminalFailureUsage(t *testing.T) {
	tests := []struct {
		name, text, finish string
		providerErr        error
		lateProviderErr    error
		cancel             bool
		wantUsage          bool
	}{
		{name: "malformed output", text: "private malformed output", finish: "stop", wantUsage: true},
		{name: "unsupported finish", text: reviewOutput("accept"), finish: "length"},
		{name: "provider failure", text: "private partial output", providerErr: errors.New("private provider failure")},
		{name: "provider failure after done", text: "private malformed output", finish: "stop", lateProviderErr: errors.New("private late provider failure")},
		{name: "canceled", text: "private partial output", cancel: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			v := reviewer(reviewProvider(func(streamCtx context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				if err := emit(providers.Chunk{Text: tc.text, Usage: &providers.Usage{InputTokens: 17, OutputTokens: 5}}); err != nil {
					return err
				}
				if tc.cancel {
					cancel()
					return streamCtx.Err()
				}
				if tc.providerErr != nil {
					return tc.providerErr
				}
				if err := emit(providers.Chunk{Done: true, FinishReason: tc.finish}); err != nil {
					return err
				}
				return tc.lateProviderErr
			}))
			out, err := v.Review(ctx, ReviewRequest{Domain: "creative", Requirements: "Write", Candidate: "candidate"})
			if err == nil || out.Audit.Verdict != "" || strings.Contains(err.Error(), "private") {
				t.Fatalf("failure authority or redaction lost: %#v %v", out, err)
			}
			if tc.wantUsage {
				if out.Usage == nil || out.Usage.InputTokens != 17 || out.Usage.OutputTokens != 5 || out.Elapsed <= 0 {
					t.Fatalf("terminal usage lost: %#v", out)
				}
			} else if out.Usage != nil || out.Elapsed != 0 {
				t.Fatalf("interrupted usage treated as known: %#v", out)
			}
		})
	}
}

func TestReviewAttemptFailureMeasurementValidation(t *testing.T) {
	base := ReviewAttempt{Version: 1, ID: "review", TaskID: "task", AttemptID: "attempt", EvaluatorModel: "model", EvaluatorProvider: "provider", Status: "failed", Code: "review_failed", StartedAt: time.Unix(100, 0), FinishedAt: time.Unix(102, 0), Usage: &providers.Usage{InputTokens: 3, OutputTokens: 2}, Elapsed: time.Second}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ReviewAttempt){
		func(r *ReviewAttempt) { r.Code = "canceled" },
		func(r *ReviewAttempt) { r.Elapsed = 3 * time.Second },
		func(r *ReviewAttempt) { r.Usage = nil },
		func(r *ReviewAttempt) { r.Usage.InputTokens = -1 },
		func(r *ReviewAttempt) { r.Status, r.Code, r.FinishedAt = "started", "", time.Time{} },
	} {
		r := base
		u := *base.Usage
		r.Usage = &u
		mutate(&r)
		if err := r.Validate(); err == nil {
			t.Fatal("invalid failure measurement accepted", r)
		}
	}
}
