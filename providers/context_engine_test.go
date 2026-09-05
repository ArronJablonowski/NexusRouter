package providers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type estimateFunc func(context.Context, Request) (int, error)

func (f estimateFunc) Estimate(ctx context.Context, r Request) (int, error) { return f(ctx, r) }

func estimateFixture() Request {
	return Request{Model: "fixture", Messages: []Message{{Role: "assistant", Content: "hello", ToolCalls: []ToolCall{{ID: "c1", Name: "lookup", Arguments: json.RawMessage(`{"key":"value"}`)}}}}, Tools: []Tool{{Name: "lookup", Description: "A lookup", Parameters: json.RawMessage(`{"type":"object"}`)}}, JSONSchema: json.RawMessage(`{"type":"string"}`)}
}

func TestEstimateWithBaselineAndSupplement(t *testing.T) {
	r := estimateFixture()
	baseline, err := EstimateContext(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, custom := range []int{0, baseline - 1, baseline, baseline + 500} {
		got, err := EstimateWith(context.Background(), estimateFunc(func(context.Context, Request) (int, error) { return custom, nil }), r)
		if err != nil || got != max(custom, baseline) {
			t.Fatalf("custom=%d got=%d err=%v", custom, got, err)
		}
	}
	got, err := EstimateWith(context.Background(), nil, r)
	if err != nil || got != baseline {
		t.Fatalf("nil estimator: %d %v", got, err)
	}
}

func TestEstimateWithIsolatesRequestMutation(t *testing.T) {
	r := estimateFixture()
	before, _ := json.Marshal(r)
	baseline, _ := EstimateContext(r)
	estimator := estimateFunc(func(ctx context.Context, copy Request) (int, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 3*time.Second {
			t.Error("missing bounded deadline")
		}
		copy.Model = "changed"
		copy.Messages[0].Content = "changed"
		copy.Messages[0].ToolCalls[0].Arguments[0] = 'x'
		copy.Tools[0].Parameters[0] = 'x'
		copy.Tools[0].Name = "changed"
		copy.JSONSchema[0] = 'x'
		return 0, nil
	})
	got, err := EstimateWith(context.Background(), estimator, r)
	after, _ := json.Marshal(r)
	if err != nil || got != baseline || string(before) != string(after) {
		t.Fatalf("caller request mutated or baseline changed: %d %v", got, err)
	}
}

func TestEstimateWithRejectsCallbackFailures(t *testing.T) {
	var typedNil estimateFunc
	for name, estimator := range map[string]ContextEstimator{
		"typed_nil": typedNil,
		"negative":  estimateFunc(func(context.Context, Request) (int, error) { return -1, nil }),
		"error":     estimateFunc(func(context.Context, Request) (int, error) { return 100, errors.New("private error") }),
		"panic":     estimateFunc(func(context.Context, Request) (int, error) { panic("private panic") }),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := EstimateWith(context.Background(), estimator, estimateFixture())
			if got != 0 || err != ErrContextEstimate {
				t.Fatalf("unsafe result: %d %v", got, err)
			}
		})
	}
	for _, estimator := range []ContextEstimator{nil, estimateFunc(func(context.Context, Request) (int, error) { t.Error("canceled callback executed"); return 0, nil })} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if got, err := EstimateWith(ctx, estimator, estimateFixture()); got != 0 || err != ErrContextEstimate {
			t.Fatal(got, err)
		}
		if got, err := EstimateWith(nil, estimator, estimateFixture()); got != 0 || err != ErrContextEstimate {
			t.Fatal(got, err)
		}
	}
}

func TestEstimateWithJoinsCanceledCallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	returned := false
	estimator := estimateFunc(func(ctx context.Context, _ Request) (int, error) {
		<-ctx.Done()
		returned = true
		return 100000, nil // A late optimistic answer must not escape cancellation.
	})
	got, err := EstimateWith(ctx, estimator, estimateFixture())
	if got != 0 || err != ErrContextEstimate || !returned {
		t.Fatalf("callback not joined/rejected: %d %v %v", got, err, returned)
	}
}

func TestEstimateWithSerializedBoundary(t *testing.T) {
	base := Request{Model: ""}
	body, _ := json.Marshal(base)
	for _, delta := range []int{-1, 0, 1} {
		r := base
		r.Model = strings.Repeat("x", maxEstimateRequestBytes-len(body)+delta)
		called := false
		got, err := EstimateWith(context.Background(), estimateFunc(func(context.Context, Request) (int, error) { called = true; return 0, nil }), r)
		if delta <= 0 {
			if err != nil || !called || got <= 0 {
				t.Fatalf("valid boundary %d rejected: %d %v", delta, got, err)
			}
		} else if err != ErrContextEstimate || got != 0 || called {
			t.Fatalf("oversize accepted: %d %v", got, err)
		}
	}
	// The nil compatibility path retains prior behavior beyond the custom cap.
	r := Request{Model: strings.Repeat("x", maxEstimateRequestBytes+1)}
	if _, err := EstimateWith(context.Background(), nil, r); err != nil {
		t.Fatal(err)
	}
}

func TestEstimateWithRejectsMalformedInputBeforeCallback(t *testing.T) {
	for name, mutate := range map[string]func(*Request){
		"model_utf8":       func(r *Request) { r.Model = string([]byte{255}) },
		"message_utf8":     func(r *Request) { r.Messages[0].Content = string([]byte{255}) },
		"tool_utf8":        func(r *Request) { r.Tools[0].Description = string([]byte{255}) },
		"arguments_json":   func(r *Request) { r.Messages[0].ToolCalls[0].Arguments = json.RawMessage(`{`) },
		"schema_json":      func(r *Request) { r.JSONSchema = json.RawMessage(`{`) },
		"schema_utf8":      func(r *Request) { r.Tools[0].Parameters = json.RawMessage{'"', 255, '"'} },
		"escaped_oversize": func(r *Request) { r.Model = strings.Repeat("\x00", maxEstimateRequestBytes/6+1) },
	} {
		t.Run(name, func(t *testing.T) {
			r := estimateFixture()
			mutate(&r)
			got, err := EstimateWith(context.Background(), estimateFunc(func(context.Context, Request) (int, error) { t.Error("invalid input reached callback"); return 0, nil }), r)
			if got != 0 || err != ErrContextEstimate {
				t.Fatalf("invalid input accepted: %d %v", got, err)
			}
		})
	}
}
