package sessions

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

func TestSummarizerPreservesOnlyVerifiedTerminalFailureUsage(t *testing.T) {
	tests := []struct {
		name, text, finish string
		providerErr        error
		lateProviderErr    error
		cancel             bool
		wantUsage          bool
	}{
		{name: "malformed output", text: "private malformed output", finish: "stop", wantUsage: true},
		{name: "unsupported finish", text: summaryResponse, finish: "length"},
		{name: "provider failure", text: "private partial output", providerErr: errors.New("private provider failure")},
		{name: "provider failure after done", text: "private malformed output", finish: "stop", lateProviderErr: errors.New("private late provider failure")},
		{name: "canceled", text: "private partial output", cancel: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := summarySettings(summaryProvider(func(streamCtx context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				if err := emit(providers.Chunk{Text: tc.text, Usage: &providers.Usage{InputTokens: 19, OutputTokens: 7}}); err != nil {
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
			draft, err := s.Draft(ctx, summarySource(), 2)
			if !errors.Is(err, ErrHistory) && !errors.Is(err, context.Canceled) {
				t.Fatalf("failure authority lost: %#v %v", draft, err)
			}
			if tc.wantUsage {
				if draft.Usage == nil || draft.Usage.InputTokens != 19 || draft.Usage.OutputTokens != 7 || draft.Elapsed <= 0 {
					t.Fatalf("terminal usage lost: %#v", draft)
				}
			} else if draft.Usage != nil || draft.Elapsed != 0 {
				t.Fatalf("interrupted usage treated as known: %#v", draft)
			}
		})
	}
}

func TestSummaryAttemptFailureMeasurementValidation(t *testing.T) {
	base := SummaryAttempt{Version: 1, ID: "summary", TaskID: "task", SourceDigest: strings.Repeat("a", 64), Model: "model", Provider: "provider", Status: "failed", Code: "summary_failed", SourceSequence: 1, Keep: 1, StartedAt: time.Unix(100, 0), FinishedAt: time.Unix(102, 0), Usage: &providers.Usage{InputTokens: 3, OutputTokens: 2}, Elapsed: time.Second}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*SummaryAttempt){
		func(a *SummaryAttempt) { a.Code = "canceled" },
		func(a *SummaryAttempt) { a.Elapsed = 3 * time.Second },
		func(a *SummaryAttempt) { a.Usage = nil },
		func(a *SummaryAttempt) { a.Usage.OutputTokens = -1 },
		func(a *SummaryAttempt) { a.Status, a.Code, a.FinishedAt = "started", "", time.Time{} },
	} {
		a := base
		u := *base.Usage
		a.Usage = &u
		mutate(&a)
		if err := a.Validate(); err == nil {
			t.Fatal("invalid failure measurement accepted", a)
		}
	}
}
