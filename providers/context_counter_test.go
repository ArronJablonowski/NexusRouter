package providers

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestBoundCounterOptInFallbackAndIsolation(t *testing.T) {
	r := Request{Model: "fixture", Messages: []Message{{Role: "user", Content: strings.Repeat("x", 8000)}}}
	base, _ := EstimateContext(r)
	for _, tc := range []struct {
		name      string
		model     string
		supported bool
		want      int
	}{{"exact", "fixture", true, 1124}, {"unsupported", "fixture", false, base}, {"other model", "other", true, base}} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			c, e := NewBoundTokenCounter(tc.model, strings.Repeat("a", 64), strings.Repeat("b", 64), func(_ context.Context, q Request) (int, bool, error) {
				called = true
				q.Messages[0].Content = "mutated"
				return 100, tc.supported, nil
			})
			if e != nil {
				t.Fatal(e)
			}
			got, e := EstimateWith(context.Background(), c, r)
			if e != nil || got != tc.want || r.Messages[0].Content == "mutated" {
				t.Fatal(got, e)
			}
			if tc.model != "fixture" && called {
				t.Fatal("wrong-model callback")
			}
		})
	}
}
func TestBoundCounterFailsClosed(t *testing.T) {
	for name, fn := range map[string]TokenCounter{"zero": func(context.Context, Request) (int, bool, error) { return 0, true, nil }, "negative": func(context.Context, Request) (int, bool, error) { return -1, true, nil }, "oversize": func(context.Context, Request) (int, bool, error) { return maxEstimateRequestBytes + 1, true, nil }, "error": func(context.Context, Request) (int, bool, error) { return 1, true, errors.New("private") }, "panic": func(context.Context, Request) (int, bool, error) { panic("private") }} {
		t.Run(name, func(t *testing.T) {
			c, _ := NewBoundTokenCounter("fixture", strings.Repeat("a", 64), strings.Repeat("b", 64), fn)
			if n, e := EstimateWith(context.Background(), c, Request{Model: "fixture"}); n != 0 || e != ErrContextEstimate {
				t.Fatal(n, e)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := NewBoundTokenCounter("fixture", strings.Repeat("a", 64), strings.Repeat("b", 64), func(context.Context, Request) (int, bool, error) {
		t.Fatal("canceled callback invoked")
		return 1, true, nil
	})
	if _, e := EstimateWith(ctx, c, Request{Model: "fixture"}); e != ErrContextEstimate {
		t.Fatal(e)
	}
	if _, e := NewBoundTokenCounter("fixture", "invalid", strings.Repeat("b", 64), c.count); e != ErrContextEstimate {
		t.Fatal(e)
	}
}
