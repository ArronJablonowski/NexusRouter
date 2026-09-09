package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

type testFactory func(context.Context, Connection) (Provider, error)

func TestFactoryConnectionSerializationExcludesAuthority(t *testing.T) {
	body, err := json.Marshal(Connection{Version: 1, ID: "fixture", APIKey: "private-key", Transport: http.DefaultTransport})
	if err != nil || strings.Contains(string(body), "private-key") || strings.Contains(string(body), "APIKey") || strings.Contains(string(body), "Transport") {
		t.Fatal("connection authority serialized", err)
	}
}

func (f testFactory) Build(ctx context.Context, c Connection) (Provider, error) { return f(ctx, c) }

type factoryTestProvider struct {
	stream func(context.Context, Request, func(Chunk) error) error
	models func(context.Context) ([]string, error)
}

func (p *factoryTestProvider) Stream(ctx context.Context, r Request, emit func(Chunk) error) error {
	if p.stream != nil {
		return p.stream(ctx, r, emit)
	}
	return emit(Chunk{Done: true, FinishReason: "stop"})
}
func (p *factoryTestProvider) Models(ctx context.Context) ([]string, error) {
	if p.models != nil {
		return p.models(ctx)
	}
	return []string{"test-model"}, nil
}

type factoryTransport func(*http.Request) (*http.Response, error)

func (f factoryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func factoryConnection() Connection {
	return Connection{Version: 1, ID: "test-provider", Endpoint: "http://localhost:11434", Kind: "ollama", Purpose: PurposeExecution, APIKey: "credential-secret", Transport: &http.Transport{}}
}

func TestFactoryReceivesConnectionAndBoundedContext(t *testing.T) {
	c := factoryConnection()
	called := false
	p, err := Build(context.Background(), testFactory(func(ctx context.Context, got Connection) (Provider, error) {
		called = true
		if !reflect.DeepEqual(got, c) || got.Transport != c.Transport {
			t.Errorf("factory connection changed: %#v", got)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 3*time.Second {
			t.Error("factory needs a positive deadline of at most three seconds")
		}
		return &factoryTestProvider{}, nil
	}), c)
	if err != nil || p == nil || !called {
		t.Fatalf("build: provider=%v called=%v err=%v", p, called, err)
	}
}

func TestFactoryFailuresAreSanitized(t *testing.T) {
	for name, f := range map[string]testFactory{
		"error":     func(context.Context, Connection) (Provider, error) { return nil, errors.New("credential-secret") },
		"panic":     func(context.Context, Connection) (Provider, error) { panic("credential-secret") },
		"nil":       func(context.Context, Connection) (Provider, error) { return nil, nil },
		"typed nil": func(context.Context, Connection) (Provider, error) { return (*factoryTestProvider)(nil), nil },
	} {
		t.Run(name, func(t *testing.T) {
			p, err := Build(context.Background(), f, factoryConnection())
			if p != nil || err == nil || strings.Contains(err.Error(), "credential-secret") {
				t.Fatalf("unsafe build failure: provider=%v err=%v", p, err)
			}
		})
	}
}

func TestFactoryCancellationSkipsCallbacks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Build(ctx, testFactory(func(context.Context, Connection) (Provider, error) {
		t.Fatal("called factory after cancellation")
		return nil, nil
	}), factoryConnection())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("build cancellation: %v", err)
	}
	p, err := Build(context.Background(), testFactory(func(context.Context, Connection) (Provider, error) {
		return &factoryTestProvider{
			stream: func(context.Context, Request, func(Chunk) error) error {
				t.Fatal("stream called after cancellation")
				return nil
			},
			models: func(context.Context) ([]string, error) { t.Fatal("models called after cancellation"); return nil, nil },
		}, nil
	}), factoryConnection())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Stream(ctx, Request{}, func(Chunk) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("stream: %v", err)
	}
	if _, err := p.Models(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("models: %v", err)
	}
}

func TestFactoryStreamFailureNormalization(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failure    error
		panicValue bool
		code       string
	}{
		{"ordinary error", errors.New("credential-secret"), false, "adapter_failure"},
		{"panic", nil, true, "adapter_failure"},
		{"untrusted code", &Failure{Code: "credential-secret", Retryable: true}, false, "adapter_failure"},
		{"known code", &Failure{Code: "rate_limit", Retryable: true}, false, "rate_limit"},
		{"wrapped known code", fmt.Errorf("credential-secret: %w", &Failure{Code: "invalid_response"}), false, "invalid_response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Build(context.Background(), testFactory(func(context.Context, Connection) (Provider, error) {
				return &factoryTestProvider{stream: func(context.Context, Request, func(Chunk) error) error {
					if tc.panicValue {
						panic("credential-secret")
					}
					return tc.failure
				}}, nil
			}), factoryConnection())
			if err != nil {
				t.Fatal(err)
			}
			err = p.Stream(context.Background(), Request{Model: "test-model", Messages: []Message{{Role: "user", Content: "hello"}}}, func(Chunk) error { return nil })
			var failure *Failure
			if !errors.As(err, &failure) || failure.Code != tc.code || strings.Contains(err.Error(), "credential-secret") {
				t.Fatalf("unsafe stream error: %v", err)
			}
		})
	}
}

func TestFactoryModelMetadataValidation(t *testing.T) {
	for name, ids := range map[string][]string{
		"blank": {" "}, "empty ID": {""}, "duplicate": {"same", "same"},
		"invalid UTF8": {string([]byte{0xff})}, "too long": {strings.Repeat("x", 257)},
		"too many": make([]string, 4097),
	} {
		t.Run(name, func(t *testing.T) {
			p, err := Build(context.Background(), testFactory(func(context.Context, Connection) (Provider, error) {
				return &factoryTestProvider{models: func(context.Context) ([]string, error) { return ids, nil }}, nil
			}), factoryConnection())
			if err != nil {
				t.Fatal(err)
			}
			got, err := p.Models(context.Background())
			if err == nil || len(got) != 0 {
				t.Fatalf("accepted malformed metadata: %v %v", got, err)
			}
		})
	}
	for _, panics := range []bool{false, true} {
		p, err := Build(context.Background(), testFactory(func(context.Context, Connection) (Provider, error) {
			return &factoryTestProvider{models: func(context.Context) ([]string, error) {
				if panics {
					panic("credential-secret")
				}
				return []string{"partial"}, errors.New("credential-secret")
			}}, nil
		}), factoryConnection())
		if err != nil {
			t.Fatal(err)
		}
		got, err := p.Models(context.Background())
		if err == nil || len(got) != 0 || strings.Contains(err.Error(), "credential-secret") {
			t.Fatalf("unsafe metadata failure: %v %v", got, err)
		}
	}
}

func TestFactoryNilUsesBuiltinAndProvidedTransport(t *testing.T) {
	c := factoryConnection()
	called := false
	c.Transport = factoryTransport(func(r *http.Request) (*http.Response, error) {
		called = true
		if r.URL.String() != c.Endpoint+"/api/tags" || r.Header.Get("Authorization") != "Bearer "+c.APIKey {
			t.Error("builtin did not use configured endpoint and credential")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"models":[{"name":"local-model"}]}`)), Header: make(http.Header)}, nil
	})
	p, err := Build(context.Background(), nil, c)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := p.Models(context.Background())
	if err != nil || !called || !reflect.DeepEqual(ids, []string{"local-model"}) {
		t.Fatalf("builtin models: %v called=%v err=%v", ids, called, err)
	}
}

func TestFactoryCooperativeConstructionCancellationDiscardsResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	p, err := Build(ctx, testFactory(func(ctx context.Context, _ Connection) (Provider, error) {
		<-ctx.Done()
		return &factoryTestProvider{}, nil
	}), factoryConnection())
	if p != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("late provider accepted: %v %v", p, err)
	}
}

func TestFactoryAppliesConfiguredOperationTimeout(t *testing.T) {
	for _, operation := range []string{"stream", "models"} {
		t.Run(operation, func(t *testing.T) {
			connection := factoryConnection()
			connection.Timeout = 100 * time.Millisecond
			provider, err := Build(context.Background(), testFactory(func(context.Context, Connection) (Provider, error) {
				wait := func(ctx context.Context) error {
					deadline, ok := ctx.Deadline()
					remaining := time.Until(deadline)
					if !ok || remaining <= 0 || remaining > connection.Timeout {
						t.Errorf("operation received invalid deadline")
					}
					<-ctx.Done()
					return ctx.Err()
				}
				return &factoryTestProvider{
					stream: func(ctx context.Context, _ Request, _ func(Chunk) error) error { return wait(ctx) },
					models: func(ctx context.Context) ([]string, error) { return nil, wait(ctx) },
				}, nil
			}), connection)
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			if operation == "stream" {
				err = provider.Stream(context.Background(), request(), func(Chunk) error { return nil })
			} else {
				_, err = provider.Models(context.Background())
			}
			elapsed := time.Since(started)
			if !errors.Is(err, context.DeadlineExceeded) || elapsed < 50*time.Millisecond || elapsed > time.Second {
				t.Fatalf("timeout result err=%v elapsed=%v", err, elapsed)
			}
		})
	}
}

func TestFactoryRejectsInvalidOperationTimeout(t *testing.T) {
	for _, timeout := range []time.Duration{-1, time.Nanosecond, 99 * time.Millisecond, 5*time.Minute + time.Nanosecond} {
		connection := factoryConnection()
		connection.Timeout = timeout
		if provider, err := Build(context.Background(), nil, connection); err == nil || provider != nil {
			t.Fatalf("accepted timeout %v", timeout)
		}
	}
}

func TestFactoryModelsBoundaryAndOwnership(t *testing.T) {
	names := make([]string, 4096)
	for i := range names {
		names[i] = fmt.Sprintf("model-%04d", i)
	}
	names[0] = strings.Repeat("x", 256)
	p, err := Build(context.Background(), testFactory(func(context.Context, Connection) (Provider, error) {
		return &factoryTestProvider{models: func(context.Context) ([]string, error) { return names, nil }}, nil
	}), factoryConnection())
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Models(context.Background())
	if err != nil || !reflect.DeepEqual(got, names) {
		t.Fatalf("valid boundary: %d names, %v", len(got), err)
	}
	got[1] = "mutated"
	if names[1] == "mutated" {
		t.Fatal("caller can mutate adapter-owned metadata")
	}
}

func TestFactoryStreamCompletionAndCallbackFailure(t *testing.T) {
	request := Request{Model: "test-model", Messages: []Message{{Role: "user", Content: "hello"}}}
	for _, tc := range []struct {
		name        string
		chunks      []Chunk
		adapterErr  error
		callbackErr error
		wantPartial bool
	}{
		{name: "missing completion", chunks: []Chunk{{Text: "partial"}}, wantPartial: true},
		{name: "late output", chunks: []Chunk{{Done: true, FinishReason: "stop"}, {Text: "late"}}},
		{name: "invalid completion", chunks: []Chunk{{Done: true, FinishReason: "credential-secret"}}},
		{name: "partial retryable", chunks: []Chunk{{Text: "partial"}}, adapterErr: &Failure{Code: "transport", Retryable: true}, wantPartial: true},
		{name: "ignored callback error", chunks: []Chunk{{Text: "first"}, {Text: "second"}, {Done: true}}, callbackErr: errors.New("sink failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Build(context.Background(), testFactory(func(context.Context, Connection) (Provider, error) {
				return &factoryTestProvider{stream: func(_ context.Context, got Request, emit func(Chunk) error) error {
					got.Messages[0].Content = "changed by adapter"
					for _, chunk := range tc.chunks {
						_ = emit(chunk)
					}
					return tc.adapterErr
				}}, nil
			}), factoryConnection())
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			err = p.Stream(context.Background(), request, func(Chunk) error { calls++; return tc.callbackErr })
			if request.Messages[0].Content != "hello" {
				t.Fatal("adapter mutated caller request")
			}
			if tc.callbackErr != nil {
				if err != tc.callbackErr || calls != 1 {
					t.Fatalf("callback failure lost: calls=%d err=%v", calls, err)
				}
				return
			}
			var failure *Failure
			if !errors.As(err, &failure) || failure.Partial != tc.wantPartial || failure.Retryable {
				t.Fatalf("unsafe stream failure: %#v", err)
			}
		})
	}
}

func TestFactoryStreamClonesMetadataAndRetainsPartialOnPanic(t *testing.T) {
	request := Request{Model: "test-model", Messages: []Message{{Role: "user", Content: "hello"}}}
	for _, panics := range []bool{false, true} {
		call := &ToolCall{ID: "call-1", Name: "read_file", Arguments: []byte(`{"path":"a"}`)}
		usage := &Usage{InputTokens: 1, OutputTokens: 2}
		p, err := Build(context.Background(), testFactory(func(context.Context, Connection) (Provider, error) {
			return &factoryTestProvider{stream: func(_ context.Context, _ Request, emit func(Chunk) error) error {
				if err := emit(Chunk{ToolCall: call, Usage: usage}); err != nil {
					return err
				}
				call.Name = "mutated"
				call.Arguments[2] = 'X'
				usage.InputTokens = 99
				if panics {
					panic("credential-secret")
				}
				return emit(Chunk{Done: true, FinishReason: "tool_calls"})
			}}, nil
		}), factoryConnection())
		if err != nil {
			t.Fatal(err)
		}
		var received Chunk
		err = p.Stream(context.Background(), request, func(chunk Chunk) error {
			if chunk.ToolCall != nil {
				received = chunk
			}
			return nil
		})
		if received.ToolCall == nil || received.ToolCall.Name != "read_file" || string(received.ToolCall.Arguments) != `{"path":"a"}` || received.Usage.InputTokens != 1 {
			t.Fatal("adapter mutated previously emitted chunk")
		}
		if panics {
			var failure *Failure
			if !errors.As(err, &failure) || !failure.Partial || failure.Retryable || failure.Code != "adapter_failure" {
				t.Fatalf("unsafe partial panic: %#v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
}
