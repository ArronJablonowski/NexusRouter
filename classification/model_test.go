package classification

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

type fakeProvider struct {
	stream func(context.Context, providers.Request, func(providers.Chunk) error) error
	calls  int
	input  providers.Request
}

func (p *fakeProvider) Stream(ctx context.Context, request providers.Request, emit func(providers.Chunk) error) error {
	p.calls++
	p.input = request
	return p.stream(ctx, request, emit)
}

func (*fakeProvider) Models(context.Context) ([]string, error) { return []string{"classifier"}, nil }

func config() ModelConfig {
	return ModelConfig{
		Model: "classifier", Timeout: time.Second, MaxInputTokens: 4096, MaxOutputTokens: 64,
		MaxInputBytes: 1024, AllowedDomains: []string{"creative", "code"},
		AllowedCapabilities: []string{"review", "code", "review"},
	}
}

func classifier(t *testing.T, stream func(context.Context, providers.Request, func(providers.Chunk) error) error) (*ModelClassifier, *fakeProvider) {
	t.Helper()
	p := &fakeProvider{stream: stream}
	c, err := NewModelClassifier(p, config())
	if err != nil {
		t.Fatal(err)
	}
	return c, p
}

func emitJSON(value string) func(context.Context, providers.Request, func(providers.Chunk) error) error {
	return func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		return emit(providers.Chunk{Text: value, Done: true, FinishReason: "stop", Usage: &providers.Usage{InputTokens: 12, OutputTokens: 7}})
	}
}

func TestModelClassifierReturnsOnlyValidatedDecision(t *testing.T) {
	c, provider := classifier(t, func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		if err := emit(providers.Chunk{Text: `{"version":1,"domain":"code",`}); err != nil {
			return err
		}
		return emit(providers.Chunk{Text: `"capabilities":["review","code","review"]}`, Done: true, FinishReason: "stop", Usage: &providers.Usage{InputTokens: 12, OutputTokens: 7}})
	})
	result, err := c.Classify(context.Background(), Input{Task: "Review this change", Context: "Go repository"})
	if err != nil {
		t.Fatal(err)
	}
	want := Decision{Version: 1, Domain: "code", Capabilities: []string{"code", "review"}}
	if !reflect.DeepEqual(result.Decision, want) || result.Usage == nil || result.Usage.InputTokens != 12 || result.Elapsed < 0 || result.Elapsed > time.Second {
		t.Fatalf("result = %#v", result)
	}
	request := provider.input
	if request.Model != "classifier" || request.MaxOutputTokens != 64 || request.Tools != nil || len(request.Messages) != 2 || request.Messages[0].Role != "system" || request.Messages[1].Role != "user" {
		t.Fatalf("unsafe provider request: %#v", request)
	}
	var schema map[string]any
	if json.Unmarshal(request.JSONSchema, &schema) != nil || schema["additionalProperties"] != false {
		t.Fatalf("schema is not closed: %s", request.JSONSchema)
	}
	var input map[string]string
	if json.Unmarshal([]byte(request.Messages[1].Content), &input) != nil || input["task"] != "Review this change" || input["context"] != "Go repository" {
		t.Fatalf("input was not safely encoded: %q", request.Messages[1].Content)
	}
}

func TestModelClassifierAcceptsEmptyCapabilitiesAsNonNil(t *testing.T) {
	c, _ := classifier(t, emitJSON(`{"version":1,"domain":"creative","capabilities":[]}`))
	result, err := c.Classify(context.Background(), Input{Task: "Write a poem"})
	if err != nil || result.Decision.Capabilities == nil || len(result.Decision.Capabilities) != 0 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestNewModelClassifierRejectsUnsafeOrUnboundedConfig(t *testing.T) {
	var typedNil *fakeProvider
	tests := []struct {
		name     string
		provider providers.Provider
		mutate   func(*ModelConfig)
	}{
		{"nil provider", nil, func(*ModelConfig) {}},
		{"typed nil provider", typedNil, func(*ModelConfig) {}},
		{"model", &fakeProvider{}, func(c *ModelConfig) { c.Model = " bad" }},
		{"zero timeout", &fakeProvider{}, func(c *ModelConfig) { c.Timeout = 0 }},
		{"long timeout", &fakeProvider{}, func(c *ModelConfig) { c.Timeout = MaxClassifierTimeout + 1 }},
		{"zero tokens", &fakeProvider{}, func(c *ModelConfig) { c.MaxOutputTokens = 0 }},
		{"many tokens", &fakeProvider{}, func(c *ModelConfig) { c.MaxOutputTokens = MaxClassifierOutputTokens + 1 }},
		{"zero input tokens", &fakeProvider{}, func(c *ModelConfig) { c.MaxInputTokens = 0 }},
		{"many input tokens", &fakeProvider{}, func(c *ModelConfig) { c.MaxInputTokens = providers.MaxOutputTokens + 1 }},
		{"zero input", &fakeProvider{}, func(c *ModelConfig) { c.MaxInputBytes = 0 }},
		{"large input", &fakeProvider{}, func(c *ModelConfig) { c.MaxInputBytes = MaxClassifierInputBytes + 1 }},
		{"no domains", &fakeProvider{}, func(c *ModelConfig) { c.AllowedDomains = nil }},
		{"unsafe domain", &fakeProvider{}, func(c *ModelConfig) { c.AllowedDomains = []string{"bad\nlabel"} }},
		{"no capabilities", &fakeProvider{}, func(c *ModelConfig) { c.AllowedCapabilities = nil }},
		{"unsafe capability", &fakeProvider{}, func(c *ModelConfig) { c.AllowedCapabilities = []string{" bad"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := config()
			test.mutate(&cfg)
			if _, err := NewModelClassifier(test.provider, cfg); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

type fakeEstimator struct {
	estimate int
	err      error
	calls    int
	request  providers.Request
}

func (e *fakeEstimator) Estimate(_ context.Context, request providers.Request) (int, error) {
	e.calls++
	e.request = request
	return e.estimate, e.err
}

func TestModelClassifierPreflightsInputTokenAdmission(t *testing.T) {
	estimator := &fakeEstimator{estimate: 5000}
	cfg := config()
	cfg.ContextEstimator = estimator
	p := &fakeProvider{stream: emitJSON(`{"version":1,"domain":"code","capabilities":[]}`)}
	c, err := NewModelClassifier(p, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Classify(context.Background(), Input{Task: "task"}); !errors.Is(err, ErrInvalidInput) || p.calls != 0 || estimator.calls != 1 {
		t.Fatalf("error=%v provider calls=%d estimator calls=%d", err, p.calls, estimator.calls)
	}
	if estimator.request.MaxOutputTokens != cfg.MaxOutputTokens || estimator.request.Tools != nil {
		t.Fatalf("estimator received unsafe request: %#v", estimator.request)
	}
	estimator.estimate = 1
	if _, err := c.Classify(context.Background(), Input{Task: "task"}); err != nil || p.calls != 1 {
		t.Fatalf("admitted error=%v provider calls=%d", err, p.calls)
	}
}

func TestModelClassifierRejectsInputBeforeProviderCall(t *testing.T) {
	c, provider := classifier(t, emitJSON(`{"version":1,"domain":"code","capabilities":[]}`))
	invalidUTF8 := string([]byte{0xff})
	tests := []struct {
		name  string
		ctx   context.Context
		input Input
	}{
		{"nil context", nil, Input{Task: "task"}},
		{"empty task", context.Background(), Input{Task: " \n"}},
		{"invalid task utf8", context.Background(), Input{Task: invalidUTF8}},
		{"invalid context utf8", context.Background(), Input{Task: "task", Context: invalidUTF8}},
		{"nul", context.Background(), Input{Task: "task\x00"}},
		{"oversized", context.Background(), Input{Task: strings.Repeat("x", 1024)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := provider.calls
			_, err := c.Classify(test.ctx, test.input)
			if !errors.Is(err, ErrInvalidInput) || provider.calls != before {
				t.Fatalf("error=%v calls=%d", err, provider.calls)
			}
		})
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	before := provider.calls
	if _, err := c.Classify(canceled, Input{Task: "task"}); !errors.Is(err, context.Canceled) || provider.calls != before {
		t.Fatalf("canceled error=%v calls=%d", err, provider.calls)
	}
}

func TestModelClassifierRejectsInvalidDecisionJSON(t *testing.T) {
	tests := map[string]string{
		"malformed":          `{"version":`,
		"array":              `[]`,
		"unknown field":      `{"version":1,"domain":"code","capabilities":[],"reason":"x"}`,
		"duplicate field":    `{"version":1,"domain":"code","domain":"creative","capabilities":[]}`,
		"trailing prose":     `{"version":1,"domain":"code","capabilities":[]} ok`,
		"trailing object":    `{"version":1,"domain":"code","capabilities":[]}{}`,
		"missing version":    `{"domain":"code","capabilities":[]}`,
		"missing domain":     `{"version":1,"capabilities":[]}`,
		"missing capability": `{"version":1,"domain":"code"}`,
		"null capabilities":  `{"version":1,"domain":"code","capabilities":null}`,
		"wrong version":      `{"version":2,"domain":"code","capabilities":[]}`,
		"unknown domain":     `{"version":1,"domain":"math","capabilities":[]}`,
		"unknown capability": `{"version":1,"domain":"code","capabilities":["shell"]}`,
		"wrong capability":   `{"version":1,"domain":"code","capabilities":[1]}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			c, _ := classifier(t, emitJSON(body))
			result, err := c.Classify(context.Background(), Input{Task: "task"})
			if !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("error = %v", err)
			}
			if result.Decision.Version != 0 || result.Usage == nil || result.Usage.InputTokens != 12 || result.Elapsed < 0 {
				t.Fatalf("verified terminal accounting lost or decision leaked: %#v", result)
			}
		})
	}
}

func TestModelClassifierRejectsInvalidStream(t *testing.T) {
	valid := `{"version":1,"domain":"code","capabilities":[]}`
	tests := map[string]func(context.Context, providers.Request, func(providers.Chunk) error) error{
		"tool": func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			return emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "call", Name: "read", Arguments: json.RawMessage(`{}`)}, Done: true, FinishReason: "tool_calls"})
		},
		"non stop": func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			return emit(providers.Chunk{Text: valid, Done: true, FinishReason: "length"})
		},
		"empty reason": func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			return emit(providers.Chunk{Text: valid, Done: true})
		},
		"early reason": func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			return emit(providers.Chunk{Text: valid, FinishReason: "stop"})
		},
		"multiple usage": func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			if err := emit(providers.Chunk{Usage: &providers.Usage{}}); err != nil {
				return err
			}
			return emit(providers.Chunk{Text: valid, Done: true, FinishReason: "stop", Usage: &providers.Usage{}})
		},
		"negative usage": func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			return emit(providers.Chunk{Text: valid, Done: true, FinishReason: "stop", Usage: &providers.Usage{InputTokens: -1}})
		},
		"over budget usage": func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			return emit(providers.Chunk{Text: valid, Done: true, FinishReason: "stop", Usage: &providers.Usage{OutputTokens: 65}})
		},
		"missing done": func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			return emit(providers.Chunk{Text: valid})
		},
		"after done": func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			if err := emit(providers.Chunk{Text: valid, Done: true, FinishReason: "stop"}); err != nil {
				return err
			}
			return emit(providers.Chunk{Text: "late"})
		},
		"empty output": func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			return emit(providers.Chunk{Done: true, FinishReason: "stop"})
		},
		"oversized output": func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			return emit(providers.Chunk{Text: strings.Repeat("x", 513), Done: true, FinishReason: "stop"})
		},
		"partial error": func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			if err := emit(providers.Chunk{Text: "{"}); err != nil {
				return err
			}
			return &providers.Failure{Code: "transport", Partial: true}
		},
	}
	for name, stream := range tests {
		t.Run(name, func(t *testing.T) {
			c, _ := classifier(t, stream)
			if _, err := c.Classify(context.Background(), Input{Task: "task"}); !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func BenchmarkModelClassifierHostOverhead(b *testing.B) {
	for b.Loop() {
		provider := &fakeProvider{stream: emitJSON(`{"version":1,"domain":"code","capabilities":["code"]}`)}
		classifier, err := NewModelClassifier(provider, config())
		if err != nil {
			b.Fatal(err)
		}
		if _, err := classifier.Classify(context.Background(), Input{Task: "classify this bounded request"}); err != nil {
			b.Fatal(err)
		}
	}
}

func TestModelClassifierContainsProviderFailureAndPanic(t *testing.T) {
	secret := "secret provider detail"
	c, _ := classifier(t, func(context.Context, providers.Request, func(providers.Chunk) error) error { return errors.New(secret) })
	if _, err := c.Classify(context.Background(), Input{Task: "task"}); !errors.Is(err, ErrProvider) || strings.Contains(err.Error(), secret) {
		t.Fatalf("error = %v", err)
	}
	c, _ = classifier(t, func(context.Context, providers.Request, func(providers.Chunk) error) error { panic(secret) })
	if _, err := c.Classify(context.Background(), Input{Task: "task"}); !errors.Is(err, ErrProvider) || strings.Contains(err.Error(), secret) {
		t.Fatalf("panic error = %v", err)
	}
}

func TestModelClassifierHonorsCancellationAndDeadline(t *testing.T) {
	blocking := func(ctx context.Context, _ providers.Request, _ func(providers.Chunk) error) error {
		<-ctx.Done()
		return ctx.Err()
	}
	cfg := config()
	cfg.Timeout = 5 * time.Millisecond
	p := &fakeProvider{stream: blocking}
	c, err := NewModelClassifier(p, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Classify(context.Background(), Input{Task: "task"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Classify(ctx, Input{Task: "task"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
}
