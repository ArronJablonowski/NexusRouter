package app

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"testing"
)

func TestDirectInferenceIdentityCapacityAndTextExecution(t *testing.T) {
	s, _ := autoFixture(t)
	ctx := context.Background()
	registration := harness.DirectRegistration("a")
	identity, err := s.NativeHarnessIdentity("a", registration, 8192)
	if err != nil || identity.Harness != "nexus-direct" {
		t.Fatal(identity, err)
	}
	ready, err := s.NativeHarnessReadiness(ctx, "a", registration, 8192)
	if err != nil || !ready.Compatible || ready.ModelState != "present" || !ready.ExecutableMatched {
		t.Fatal(ready, err)
	}
	actual, need, capacity, err := s.NativeHarnessCapacity(ctx, "a", registration, 8192)
	if err != nil || actual != identity || need.RAM == 0 || capacity.Action != resources.CapacityAdmit {
		t.Fatal(actual, need, capacity, err)
	}
	request := Request{ModelID: "a", HarnessID: registration, HarnessDifficulty: "unknown", ExpectedHarnessIdentity: &identity, RemoteExecution: &runtime.RemoteExecution{Mode: "direct", Depth: 1}, ContextTokens: 8192, Messages: []providers.Message{{Role: "user", Content: "prior"}, {Role: "assistant", Content: "answer"}, {Role: "user", Content: "current"}}}
	result, err := s.Run(ctx, request)
	if err != nil || result.Text != "a" {
		t.Fatal(result, err)
	}
	request.RemoteExecution = nil
	if _, err = s.Run(ctx, request); err == nil {
		t.Fatal("built-in registration granted ambient local authority")
	}
	request.RemoteExecution = &runtime.RemoteExecution{Mode: "direct", Depth: 1}
	changed := identity
	changed.ModelRevision = "different"
	request.ExpectedHarnessIdentity = &changed
	if _, err = s.Run(ctx, request); err == nil {
		t.Fatal("changed deployment identity dispatched")
	}
}
