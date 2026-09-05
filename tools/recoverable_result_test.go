package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestRecoverableHandlerOutcomeValidation(t *testing.T) {
	for _, readOnly := range []bool{true, false} {
		for _, mode := range []string{"valid", "not_failed", "confirmed", "uncertain"} {
			t.Run(fmt.Sprint(readOnly, mode), func(t *testing.T) {
				calls := 0
				definition := definition(&calls)
				definition.ReadOnly = readOnly
				want := runtime.ToolResult{Content: "repairable", Failed: true, Recoverable: true, Effect: runtime.NoEffect}
				switch mode {
				case "not_failed":
					want.Failed = false
				case "confirmed":
					want.Effect = runtime.ConfirmedEffect
				case "uncertain":
					want.Effect = runtime.UncertainEffect
				}
				definition.Handler = func(context.Context, json.RawMessage) (runtime.ToolResult, error) { return want, nil }
				registry := &Registry{}
				if err := registry.Register(definition); err != nil {
					t.Fatal(err)
				}
				executor := Executor{Registry: registry, Policy: &Policy{Default: Allow}, Authority: authorityFunc(func(ctx context.Context, _ Authorization, invoke func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
					return invoke(ctx)
				})}
				got, err := executor.ExecuteScoped(context.Background(), authorizedExecution())
				if mode == "valid" {
					if err != nil || got != want {
						t.Fatal("valid recoverable result changed", got, err)
					}
				} else if !errors.Is(err, ErrExecution) || got.Effect != runtime.UncertainEffect || got.Recoverable || got.Content != "" {
					t.Fatal("malformed recoverable result accepted", got, err)
				}
			})
		}
	}
}
