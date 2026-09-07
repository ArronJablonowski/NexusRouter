package runtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestSteeringCannotExpandContextBudget(t *testing.T) {
	for _, mode := range []string{"tokens", "serialized_bytes"} {
		t.Run(mode, func(t *testing.T) {
			journal := &steeringFixture{}
			r := runRequest()
			if mode == "tokens" {
				estimate, err := providers.EstimateContext(r.Inference)
				if err != nil {
					t.Fatal(err)
				}
				r.MaxContextTokens = estimate + 10
				journal.queue(strings.Repeat("x", runtime.MaxSteeringBytes))
			} else {
				for range runtime.MaxSteeringMessages {
					journal.queue(strings.Repeat("\x00", runtime.MaxSteeringBytes))
				}
			}
			calls := 0
			loop := runtime.Loop{Journal: journal, Steering: journal, Provider: model(func(context.Context, providers.Request, func(providers.Chunk) error) error { calls++; return nil })}
			_, err := loop.Run(context.Background(), r)
			want := runtime.ErrLimit
			if mode == "tokens" {
				want = runtime.ErrContextOverflow
			}
			if !errors.Is(err, want) || calls != 0 || len(journal.pending) == 0 {
				t.Fatal(err, calls, len(journal.pending))
			}
			if journal.events[len(journal.events)-1].Kind != runtime.TaskFailed {
				t.Fatal("missing budget terminal")
			}
		})
	}
}
