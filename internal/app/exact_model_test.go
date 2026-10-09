package app

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"testing"
)

type exactFailureProvider struct{ calls *int }

func (p exactFailureProvider) Models(context.Context) ([]string, error) {
	return []string{"a", "z"}, nil
}
func (p exactFailureProvider) Stream(context.Context, providers.Request, func(providers.Chunk) error) error {
	*p.calls++
	return &providers.Failure{Code: "unavailable", Retryable: true}
}

type exactFailureFactory struct{ calls *int }

func (f exactFailureFactory) Build(context.Context, providers.Connection) (providers.Provider, error) {
	return exactFailureProvider{f.calls}, nil
}
func TestExactModelNeverActivatesCommanderFallback(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(map[bool]string{false: "local pin", true: "remote explicit"}[remote], func(t *testing.T) {
			s, _ := autoFixture(t)
			s.settings.WebUI.DefaultModel = "a"
			s.settings.WebUI.CommanderFallbackModel = "z"
			calls := 0
			s.providerFactory = exactFailureFactory{&calls}
			r := Request{ModelID: "a", Prompt: "exact task", DisableFallback: !remote}
			if remote {
				r.RemoteExecution = &runtime.RemoteExecution{Mode: "direct", Depth: 1}
			}
			result, e := s.Run(context.Background(), r)
			if e == nil || calls != 1 || len(result.PreviousTaskIDs) != 0 {
				t.Fatal("exact assignment changed models", result, e, calls)
			}
		})
	}
}
