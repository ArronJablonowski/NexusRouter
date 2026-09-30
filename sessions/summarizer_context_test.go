package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

type summaryEstimator func(context.Context, providers.Request) (int, error)

func TestSummarizerNilContextRejects(t *testing.T) {
	s := summarySettings(summaryProvider(func(context.Context, providers.Request, func(providers.Chunk) error) error {
		t.Fatal("nil context reached provider")
		return nil
	}))
	if _, err := s.Draft(nil, summarySource(), 2); err != ErrHistory {
		t.Fatal(err)
	}
}

func (f summaryEstimator) Estimate(ctx context.Context, request providers.Request) (int, error) {
	return f(ctx, request)
}

func TestSummarizerContextEstimatorGatesDispatch(t *testing.T) {
	for _, mode := range []string{"high", "low_builtin_floor", "error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			s := summarySettings(summaryProvider(func(context.Context, providers.Request, func(providers.Chunk) error) error {
				t.Error("rejected summary reached provider")
				return nil
			}))
			if mode == "low_builtin_floor" {
				s.ContextTokens = 1
			}
			s.ContextEstimator = summaryEstimator(func(context.Context, providers.Request) (int, error) {
				calls++
				switch mode {
				case "high":
					return s.ContextTokens + 1, nil
				case "low_builtin_floor":
					return 0, nil
				case "error":
					return 0, errors.New("private estimator detail")
				default:
					panic("private estimator panic")
				}
			})
			draft, err := s.Draft(context.Background(), summarySource(), 2)
			if err != ErrHistory || calls != 1 || !reflect.DeepEqual(draft, SummaryDraft{}) {
				t.Fatalf("unsafe draft result: %+v %v calls=%d", draft, err, calls)
			}
		})
	}
}

func TestSummarizerEstimatorSeesExactPromptWithoutMutation(t *testing.T) {
	source := summarySource()
	original, _ := json.Marshal(source)
	var expected providers.Request
	baseline := summarySettings(summaryProvider(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
		expected = r
		return emit(providers.Chunk{Text: summaryResponse, Done: true, FinishReason: "stop"})
	}))
	want, err := baseline.Draft(context.Background(), source, 2)
	if err != nil {
		t.Fatal(err)
	}
	if expected.Model != baseline.Model || len(expected.Messages) != 2 || expected.Messages[0].Content != summaryInstructions {
		t.Fatal("unexpected baseline summary request")
	}
	var envelope struct {
		FirstRetained int                 `json:"first_retained_message"`
		Messages      []providers.Message `json:"messages"`
	}
	if err = json.Unmarshal([]byte(expected.Messages[1].Content), &envelope); err != nil || envelope.FirstRetained != 2 || !reflect.DeepEqual(envelope.Messages, source.Messages) {
		t.Fatalf("incorrect summary envelope: %+v %v", envelope, err)
	}
	estimatorCalls, providerCalls := 0, 0
	s := summarySettings(summaryProvider(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
		providerCalls++
		if !reflect.DeepEqual(r, expected) {
			t.Error("estimator changed provider input")
		}
		return emit(providers.Chunk{Text: summaryResponse, Done: true, FinishReason: "stop"})
	}))
	s.ContextEstimator = summaryEstimator(func(_ context.Context, r providers.Request) (int, error) {
		estimatorCalls++
		encoded, _ := json.Marshal(r)
		expectedJSON, _ := json.Marshal(expected)
		if string(encoded) != string(expectedJSON) {
			t.Error("estimator did not receive exact assembled summary prompt")
		}
		r.Model = "altered-model"
		r.Messages[0].Content = "altered instructions"
		r.Messages[1].Content = "altered source"
		return 0, nil
	})
	got, err := s.Draft(context.Background(), source, 2)
	after, _ := json.Marshal(source)
	if err != nil || estimatorCalls != 1 || providerCalls != 1 || string(original) != string(after) {
		t.Fatalf("summary call/isolation failed: %v %d %d", err, estimatorCalls, providerCalls)
	}
	if got.SourceDigest != want.SourceDigest || got.SourceTaskID != want.SourceTaskID || got.SourceSequence != want.SourceSequence || !reflect.DeepEqual(got.Checkpoint, want.Checkpoint) || !reflect.DeepEqual(got.Request, want.Request) {
		t.Fatal("estimator changed source provenance or proposal")
	}
}

func TestSummarizerTimeoutIncludesEstimation(t *testing.T) {
	s := summarySettings(summaryProvider(func(context.Context, providers.Request, func(providers.Chunk) error) error {
		t.Error("expired estimation reached provider")
		return nil
	}))
	s.Timeout = 20 * time.Millisecond
	returned := false
	s.ContextEstimator = summaryEstimator(func(ctx context.Context, _ providers.Request) (int, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > s.Timeout {
			t.Error("estimator lacks summary deadline")
		}
		<-ctx.Done()
		returned = true
		return 0, nil
	})
	draft, err := s.Draft(context.Background(), summarySource(), 2)
	if !errors.Is(err, context.DeadlineExceeded) || !returned || !reflect.DeepEqual(draft, SummaryDraft{}) {
		t.Fatalf("summary estimation ignored timeout: %+v %v", draft, err)
	}
}
