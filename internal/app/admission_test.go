package app

import (
	"context"
	"errors"
	"math"
	"os"
	"testing"
)

func TestInvalidRoutingInputHasNoStorageEffects(t *testing.T) {
	for _, input := range []Request{
		{MaxCost: -1}, {MaxCost: math.NaN()}, {MaxCost: math.Inf(1)},
		{Capabilities: []string{""}}, {Capabilities: []string{"chat", "chat"}},
		{ContextTokens: -1},
	} {
		svc, cfg := autoFixture(t)
		input.Prompt = "hello"
		if _, err := svc.Run(context.Background(), input); !errors.Is(err, ErrAdmission) {
			t.Fatalf("invalid input accepted: %v", err)
		}
		if _, err := os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
			t.Fatalf("invalid admission touched storage: %v", err)
		}
	}
}

func TestExplicitRequestedConstraintsBeforeStorage(t *testing.T) {
	for _, input := range []Request{
		{ContextTokens: 9000}, {Capabilities: []string{"unavailable"}}, {MaxCost: .01},
	} {
		svc, cfg := autoFixture(t)
		cost := 1.0
		svc.settings.Models[0].EstimatedCost = &cost
		input.ModelID, input.Prompt = "a", "hello"
		if _, err := svc.Run(context.Background(), input); !errors.Is(err, ErrAdmission) {
			t.Fatalf("explicit constraint ignored: %v", err)
		}
		if _, err := os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
			t.Fatalf("denied explicit request touched storage: %v", err)
		}
	}
}

func TestNoEligibleRouteIsAdmissionFailure(t *testing.T) {
	svc, _ := autoFixture(t)
	_, err := svc.Run(context.Background(), Request{Prompt: "hello", Capabilities: []string{"unavailable"}})
	if !errors.Is(err, ErrAdmission) {
		t.Fatalf("no route misclassified: %v", err)
	}
}
