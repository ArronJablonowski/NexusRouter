package skills

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

func generationAttemptFixture(status string) GenerationAttempt {
	a := GenerationAttempt{Version: 1, ID: "generation", Key: sample().Key, Model: "model", Provider: "provider", InputDigest: strings.Repeat("a", 64), SourceSessions: []string{"session-a", "session-b"}, SourceEvidence: []string{"proof-a", "proof-b"}, Status: status, StartedAt: time.Now().UTC()}
	if status != "started" {
		a.FinishedAt = a.StartedAt.Add(time.Second)
	}
	if status == "failed" {
		a.Code = "generation_failed"
	}
	if status == "drafted" {
		d := sample()
		d.SourceSessions = append([]string(nil), a.SourceSessions...)
		d.SourceEvidence = append([]string(nil), a.SourceEvidence...)
		a.Result = &ModelDraftResult{Draft: d, Model: a.Model, Elapsed: time.Millisecond, Usage: &providers.Usage{InputTokens: 1, OutputTokens: 2}}
	}
	return a
}

func TestGenerationAttemptValidStatuses(t *testing.T) {
	for _, status := range []string{"started", "failed", "drafted"} {
		a := generationAttemptFixture(status)
		before, _ := json.Marshal(a)
		if err := a.Validate(); err != nil {
			t.Fatal(status, err)
		}
		after, _ := json.Marshal(a)
		if string(before) != string(after) {
			t.Fatal("validation mutated attempt")
		}
	}
	for _, code := range []string{"generation_failed", "canceled", "persistence_failed"} {
		a := generationAttemptFixture("failed")
		a.Code = code
		if err := a.Validate(); err != nil {
			t.Fatal(code, err)
		}
	}
	for _, usage := range []*providers.Usage{nil, {}, {InputTokens: math.MaxInt64}} {
		a := generationAttemptFixture("drafted")
		a.Result.Usage = usage
		if err := a.Validate(); err != nil {
			t.Fatal(usage, err)
		}
	}
}

func TestGenerationAttemptRejectsMalformedBindings(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		edit         func(*GenerationAttempt)
	}{
		{"version", "started", func(a *GenerationAttempt) { a.Version = 2 }},
		{"id", "started", func(a *GenerationAttempt) { a.ID = "../bad" }},
		{"key", "started", func(a *GenerationAttempt) { a.Key.Scope = "bad scope" }},
		{"model", "started", func(a *GenerationAttempt) { a.Model = " model " }},
		{"provider", "started", func(a *GenerationAttempt) { a.Provider = "" }},
		{"digest", "started", func(a *GenerationAttempt) { a.InputDigest = "bad" }},
		{"digest_case", "started", func(a *GenerationAttempt) { a.InputDigest = strings.Repeat("A", 64) }},
		{"sessions_small", "started", func(a *GenerationAttempt) { a.SourceSessions = a.SourceSessions[:1] }},
		{"sessions_duplicate", "started", func(a *GenerationAttempt) { a.SourceSessions[1] = a.SourceSessions[0] }},
		{"sessions_order", "started", func(a *GenerationAttempt) { a.SourceSessions = []string{"z", "a"} }},
		{"evidence_missing", "started", func(a *GenerationAttempt) { a.SourceEvidence = nil }},
		{"evidence_invalid", "started", func(a *GenerationAttempt) { a.SourceEvidence[0] = "bad evidence" }},
		{"evidence_duplicate", "started", func(a *GenerationAttempt) { a.SourceEvidence[1] = a.SourceEvidence[0] }},
		{"status", "started", func(a *GenerationAttempt) { a.Status = "completed" }},
		{"started_terminal_time", "started", func(a *GenerationAttempt) { a.FinishedAt = a.StartedAt }},
		{"started_result", "started", func(a *GenerationAttempt) { a.Result = &ModelDraftResult{} }},
		{"started_code", "started", func(a *GenerationAttempt) { a.Code = "generation_failed" }},
		{"time_zero", "started", func(a *GenerationAttempt) { a.StartedAt = time.Time{} }},
		{"time_zone", "started", func(a *GenerationAttempt) { a.StartedAt = a.StartedAt.In(time.FixedZone("east", 3600)) }},
		{"cost_negative", "started", func(a *GenerationAttempt) { a.EstimatedCost = -1 }},
		{"cost_nan", "started", func(a *GenerationAttempt) { a.EstimatedCost = math.NaN() }},
		{"cost_inf", "started", func(a *GenerationAttempt) { a.EstimatedCost = math.Inf(1) }},
		{"failed_time", "failed", func(a *GenerationAttempt) { a.FinishedAt = a.StartedAt.Add(-time.Nanosecond) }},
		{"failed_code", "failed", func(a *GenerationAttempt) { a.Code = "private-error" }},
		{"failed_result", "failed", func(a *GenerationAttempt) { a.Result = &ModelDraftResult{} }},
		{"result_missing", "drafted", func(a *GenerationAttempt) { a.Result = nil }},
		{"result_model", "drafted", func(a *GenerationAttempt) { a.Result.Model = "other" }},
		{"result_key", "drafted", func(a *GenerationAttempt) { a.Result.Draft.Key.Name = "other" }},
		{"result_sessions", "drafted", func(a *GenerationAttempt) { a.Result.Draft.SourceSessions = []string{"different-a", "different-b"} }},
		{"result_evidence", "drafted", func(a *GenerationAttempt) { a.Result.Draft.SourceEvidence = []string{"different"} }},
		{"result_steps", "drafted", func(a *GenerationAttempt) { a.Result.Draft.Steps = nil }},
		{"result_elapsed_negative", "drafted", func(a *GenerationAttempt) { a.Result.Elapsed = -1 }},
		{"result_elapsed_large", "drafted", func(a *GenerationAttempt) { a.Result.Elapsed = 30*time.Second + time.Nanosecond }},
		{"result_usage_negative", "drafted", func(a *GenerationAttempt) { a.Result.Usage.InputTokens = -1 }},
		{"result_usage_overflow", "drafted", func(a *GenerationAttempt) {
			a.Result.Usage = &providers.Usage{InputTokens: math.MaxInt64, OutputTokens: 1}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := generationAttemptFixture(tc.status)
			tc.edit(&a)
			if err := a.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatal(tc.name, err)
			}
		})
	}
}

type fakeGenerationRecorder struct {
	begin, finish func(context.Context, GenerationAttempt) error
}

func (r fakeGenerationRecorder) BeginSkillGeneration(ctx context.Context, a GenerationAttempt) error {
	return r.begin(ctx, a)
}
func (r fakeGenerationRecorder) FinishSkillGeneration(ctx context.Context, a GenerationAttempt) error {
	return r.finish(ctx, a)
}

func TestRecordedGenerationPersistenceFailuresNeverGrantResult(t *testing.T) {
	for _, kind := range []string{"begin_error", "begin_panic", "finish_error", "finish_panic"} {
		t.Run(kind, func(t *testing.T) {
			calls, finishes := 0, 0
			g := modelGeneratorFixture(skillGenerationProvider(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				calls++
				return emit(providers.Chunk{Text: generatedSkillJSON, Done: true, FinishReason: "stop"})
			}))
			recorder := fakeGenerationRecorder{begin: func(_ context.Context, a GenerationAttempt) error {
				if a.Status != "started" || a.Validate() != nil {
					t.Error(a)
				}
				if kind == "begin_error" {
					return errors.New("private-recording-error")
				}
				if kind == "begin_panic" {
					panic("private-recording-error")
				}
				return nil
			}, finish: func(_ context.Context, a GenerationAttempt) error {
				finishes++
				if a.Status != "drafted" || a.Validate() != nil {
					t.Error(a)
				}
				if kind == "finish_panic" {
					panic("private-recording-error")
				}
				return errors.New("private-recording-error")
			}}
			a, err := g.GenerateRecorded(context.Background(), recorder, "generation", "provider", sample().Key, learningExamples())
			if !errors.Is(err, ErrGenerationPersistence) || strings.Contains(err.Error(), "private") {
				t.Fatal(a, err)
			}
			if strings.HasPrefix(kind, "begin_") {
				if calls != 0 || finishes != 0 || !reflect.DeepEqual(a, GenerationAttempt{}) {
					t.Fatal(a, calls, finishes)
				}
			} else if calls != 1 || finishes != 1 || a.Status != "started" || a.Result != nil || a.Code != "" || !a.FinishedAt.IsZero() || a.Validate() != nil {
				t.Fatal("failed acknowledgement returned proposal", a, calls, finishes)
			}
		})
	}
}

func TestRecordedGenerationRecorderMutationCannotChangeResult(t *testing.T) {
	g := modelGeneratorFixture(skillGenerationProvider(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		return emit(providers.Chunk{Text: generatedSkillJSON, Done: true, FinishReason: "stop", Usage: &providers.Usage{InputTokens: 3, OutputTokens: 4}})
	}))
	recorder := fakeGenerationRecorder{begin: func(_ context.Context, a GenerationAttempt) error {
		a.SourceSessions[0] = "mutated"
		a.SourceEvidence[0] = "mutated"
		return nil
	}, finish: func(_ context.Context, a GenerationAttempt) error {
		a.SourceSessions[0] = "mutated"
		a.SourceEvidence[0] = "mutated"
		a.Result.Draft.SourceSessions[0] = "mutated"
		a.Result.Draft.Steps[0] = "mutated"
		a.Result.Usage.InputTokens = 999
		return nil
	}}
	a, err := g.GenerateRecorded(context.Background(), recorder, "generation", "provider", sample().Key, learningExamples())
	if err != nil || a.Validate() != nil || a.Status != "drafted" || a.SourceSessions[0] != "session-a" || a.SourceEvidence[0] != "evidence-a" || a.Result.Draft.Steps[0] != "Run focused tests" || a.Result.Usage.InputTokens != 3 {
		t.Fatal(a, err)
	}
}

func TestRecordedGenerationCancellationWritesBoundedTerminal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finishes := 0
	g := modelGeneratorFixture(skillGenerationProvider(func(ctx context.Context, _ providers.Request, _ func(providers.Chunk) error) error {
		cancel()
		return ctx.Err()
	}))
	recorder := fakeGenerationRecorder{begin: func(context.Context, GenerationAttempt) error { return nil }, finish: func(ctx context.Context, a GenerationAttempt) error {
		finishes++
		deadline, ok := ctx.Deadline()
		if ctx.Err() != nil || !ok || time.Until(deadline) > 5*time.Second || a.Status != "failed" || a.Code != "canceled" || a.Result != nil || a.Validate() != nil {
			t.Error("invalid cancellation terminal", a, ctx.Err())
		}
		return nil
	}}
	a, err := g.GenerateRecorded(ctx, recorder, "generation", "provider", sample().Key, learningExamples())
	if !errors.Is(err, context.Canceled) || a.Status != "failed" || a.Code != "canceled" || a.Result != nil || finishes != 1 {
		t.Fatal(a, err, finishes)
	}
}
