package skills

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

type skillGenerationProvider func(context.Context, providers.Request, func(providers.Chunk) error) error

func (p skillGenerationProvider) Stream(ctx context.Context, r providers.Request, emit func(providers.Chunk) error) error {
	return p(ctx, r, emit)
}
func (skillGenerationProvider) Models(context.Context) ([]string, error) {
	return []string{"skill-generator"}, nil
}

type skillGenerationEstimator func(context.Context, providers.Request) (int, error)

func (f skillGenerationEstimator) Estimate(ctx context.Context, r providers.Request) (int, error) {
	return f(ctx, r)
}

const generatedSkillJSON = `{"version":1,"description":"Run relevant checks","tags":[],"steps":["Run focused tests"],"required_tools":[],"configuration":"","risks":[],"validation_cases":["Fixture repository"]}`

func modelGeneratorFixture(p providers.Provider) ModelGenerator {
	return ModelGenerator{Provider: p, Model: "skill-generator", ContextTokens: 16384, Timeout: time.Second}
}

func TestModelGeneratorSingleToolFreeCallAndInactivePersistence(t *testing.T) {
	key := sample().Key
	calls := 0
	g := modelGeneratorFixture(skillGenerationProvider(func(ctx context.Context, r providers.Request, emit func(providers.Chunk) error) error {
		calls++
		if r.Model != "skill-generator" || len(r.Tools) != 0 || len(r.Messages) != 2 || r.Messages[0].Role != "system" || r.Messages[1].Role != "user" || !strings.Contains(r.Messages[1].Content, "Run focused checks") || !strings.Contains(r.Messages[1].Content, "evidence-a") {
			t.Fatal("incomplete or expanded generation request", r)
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Second {
			t.Fatal("unbounded model generation")
		}
		if err := emit(providers.Chunk{Text: generatedSkillJSON[:len(generatedSkillJSON)/2]}); err != nil {
			return err
		}
		return emit(providers.Chunk{Text: generatedSkillJSON[len(generatedSkillJSON)/2:], Done: true, FinishReason: "stop", Usage: &providers.Usage{InputTokens: 20, OutputTokens: 10}})
	}))
	path := testPath(t)
	s := openTest(t, path)
	s.SetAutomatic(true)
	v, err := s.DraftFromWorkflows(context.Background(), key, learningExamples(), g)
	if err != nil || calls != 1 || v.Draft.Key != key || v.Draft.Description != "Run relevant checks" || !reflect.DeepEqual(v.Draft.SourceSessions, []string{"session-a", "session-b"}) || !reflect.DeepEqual(v.Draft.SourceEvidence, []string{"evidence-a", "evidence-b"}) {
		t.Fatal(v, err, calls)
	}
	items, err := s.Discover(context.Background(), key.Scope, nil, 10)
	if err != nil || len(items) != 0 {
		t.Fatal("model draft activated", items, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, path)
	loaded, err := s.Load(context.Background(), key, v.ID)
	if err != nil || !reflect.DeepEqual(loaded.Draft, v.Draft) {
		t.Fatal("model provenance lost after restart", loaded, err)
	}
}

func TestModelGeneratorRejectsMalformedAndFailedStreams(t *testing.T) {
	for _, kind := range []string{"tool", "missing_done", "length", "post_done", "sticky_emit", "provider_error", "provider_panic", "oversize", "invalid_json", "unknown_field", "invalid_utf8", "negative_usage", "duplicate_usage", "overflow_usage", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			g := modelGeneratorFixture(skillGenerationProvider(func(ctx context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				calls++
				switch kind {
				case "provider_error":
					return errors.New("private-provider-payload")
				case "provider_panic":
					panic("private-provider-payload")
				case "cancel":
					<-ctx.Done()
					return ctx.Err()
				case "tool":
					_ = emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "call", Name: "shell", Arguments: []byte(`{}`)}})
				case "sticky_emit":
					if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "call", Name: "shell", Arguments: []byte(`{}`)}}); err == nil {
						t.Error("tool chunk not rejected")
					}
				case "negative_usage":
					_ = emit(providers.Chunk{Usage: &providers.Usage{InputTokens: -1, OutputTokens: 2}})
				case "duplicate_usage":
					_ = emit(providers.Chunk{Usage: &providers.Usage{InputTokens: 1}})
					_ = emit(providers.Chunk{Usage: &providers.Usage{OutputTokens: 1}})
				case "overflow_usage":
					_ = emit(providers.Chunk{Usage: &providers.Usage{InputTokens: math.MaxInt64, OutputTokens: 1}})
				}
				text := generatedSkillJSON
				switch kind {
				case "oversize":
					text = strings.Repeat("x", (64<<10)+1)
				case "invalid_json":
					text = "not JSON"
				case "unknown_field":
					text = strings.TrimSuffix(text, "}") + `,"key":{"scope":"project","name":"forged"}}`
				case "invalid_utf8":
					text = string([]byte{255})
				}
				_ = emit(providers.Chunk{Text: text})
				if kind == "missing_done" {
					return nil
				}
				reason := "stop"
				if kind == "length" {
					reason = "length"
				}
				_ = emit(providers.Chunk{Done: true, FinishReason: reason})
				if kind == "post_done" {
					_ = emit(providers.Chunk{Text: "trailing"})
				}
				return nil
			}))
			if kind == "cancel" {
				g.Timeout = 10 * time.Millisecond
			}
			d, err := g.Generate(context.Background(), sample().Key, learningExamples())
			if err == nil || d.Key != (Key{}) || strings.Contains(err.Error(), "private") || calls != 1 {
				t.Fatal(d, err, calls)
			}
		})
	}
}

func TestModelGeneratorContextEstimatorCannotRelaxBudget(t *testing.T) {
	for _, kind := range []string{"high", "low", "error", "panic"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			g := modelGeneratorFixture(skillGenerationProvider(func(context.Context, providers.Request, func(providers.Chunk) error) error { calls++; return nil }))
			if kind == "low" {
				g.ContextTokens = 1
			}
			g.ContextEstimator = skillGenerationEstimator(func(_ context.Context, r providers.Request) (int, error) {
				if r.Model != "skill-generator" || len(r.Messages) != 2 || !strings.Contains(r.Messages[1].Content, "evidence-a") {
					t.Error("wrong generation request estimated", r)
				}
				switch kind {
				case "high":
					return 16385, nil
				case "low":
					return 1, nil
				case "error":
					return 0, errors.New("private-estimator-payload")
				default:
					panic("private-estimator-payload")
				}
			})
			d, err := g.Generate(context.Background(), sample().Key, learningExamples())
			if err == nil || d.Key != (Key{}) || calls != 0 || strings.Contains(err.Error(), "private") {
				t.Fatal(d, err, calls)
			}
		})
	}
}

func TestModelGeneratorInvalidConfigurationRejectsBeforeStream(t *testing.T) {
	for _, kind := range []string{"nil_provider", "bad_model", "unknown_context", "zero_timeout", "long_timeout", "negative_cost", "nan_cost", "inf_cost", "negative_budget", "nan_budget", "over_budget", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			g := modelGeneratorFixture(skillGenerationProvider(func(context.Context, providers.Request, func(providers.Chunk) error) error {
				t.Error("invalid generator dispatched")
				return nil
			}))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "nil_provider":
				g.Provider = nil
			case "bad_model":
				g.Model = ""
			case "unknown_context":
				g.ContextTokens = 0
			case "zero_timeout":
				g.Timeout = 0
			case "long_timeout":
				g.Timeout = time.Hour
			case "negative_cost":
				g.EstimatedCost = -1
			case "nan_cost":
				g.EstimatedCost = math.NaN()
			case "inf_cost":
				g.EstimatedCost = math.Inf(1)
			case "negative_budget":
				g.MaxCost = -1
			case "nan_budget":
				g.MaxCost = math.NaN()
			case "over_budget":
				g.EstimatedCost = 1
				g.MaxCost = 0
			case "canceled":
				cancel()
			}
			d, err := g.Generate(ctx, sample().Key, learningExamples())
			if err == nil || d.Key != (Key{}) {
				t.Fatal(d, err)
			}
		})
	}
}

func TestModelGeneratorDetailedPreservesKnownUnknownAndZeroUsage(t *testing.T) {
	for _, kind := range []string{"known", "unknown", "zero"} {
		t.Run(kind, func(t *testing.T) {
			var usage *providers.Usage
			want := providers.Usage{}
			if kind != "unknown" {
				usage = &providers.Usage{}
				if kind == "known" {
					usage.InputTokens, usage.OutputTokens = 13, 7
				}
				want = *usage
			}
			g := modelGeneratorFixture(skillGenerationProvider(func(ctx context.Context, r providers.Request, emit func(providers.Chunk) error) error {
				if r.Model != "skill-generator" {
					t.Error("wrong inference model", r.Model)
				}
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > time.Second {
					t.Error("missing detailed deadline")
				}
				if err := emit(providers.Chunk{Text: generatedSkillJSON, Done: true, FinishReason: "stop", Usage: usage}); err != nil {
					return err
				}
				if usage != nil {
					usage.InputTokens, usage.OutputTokens = 999, 999
				}
				return nil
			}))
			r, err := g.GenerateDetailed(context.Background(), sample().Key, learningExamples())
			if err != nil || r.Model != g.Model || r.Draft.Key != sample().Key || r.Elapsed < 0 {
				t.Fatal(r, err)
			}
			if kind == "unknown" {
				if r.Usage != nil {
					t.Fatal("unknown usage became known", r.Usage)
				}
			} else if r.Usage == nil || *r.Usage != want || r.Usage == usage {
				t.Fatal("usage was not an isolated exact snapshot", r.Usage, want)
			}
		})
	}
}

func TestModelGeneratorDetailedFailureReturnsNoPartialMetadata(t *testing.T) {
	for _, kind := range []string{"provider_error", "panic", "malformed_output", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			g := modelGeneratorFixture(skillGenerationProvider(func(ctx context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				if kind == "canceled" {
					<-ctx.Done()
					return ctx.Err()
				}
				text := generatedSkillJSON
				if kind == "malformed_output" {
					text = "invalid"
				}
				if err := emit(providers.Chunk{Text: text, Done: true, FinishReason: "stop", Usage: &providers.Usage{InputTokens: 3, OutputTokens: 4}}); err != nil {
					return err
				}
				if kind == "provider_error" {
					return errors.New("private-generator-error")
				}
				if kind == "panic" {
					panic("private-generator-error")
				}
				return nil
			}))
			if kind == "canceled" {
				g.Timeout = 10 * time.Millisecond
			}
			r, err := g.GenerateDetailed(context.Background(), sample().Key, learningExamples())
			if err == nil || strings.Contains(err.Error(), "private") || !reflect.DeepEqual(r, ModelDraftResult{}) {
				t.Fatal("failed generation leaked partial metadata", r, err)
			}
		})
	}
}
