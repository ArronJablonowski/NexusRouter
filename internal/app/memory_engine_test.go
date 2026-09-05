package app

import (
	"context"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/memory"
)

func TestAutomaticRouteFreezesInjectedMemoryOnce(t *testing.T) {
	fixture, cfg := autoFixture(t)
	cfg.Memory = contextMemorySettings()
	cfg.Memory.LocalOnly = true
	fact := contextMemoryFact("injected")
	fact.Privacy = "local_only"
	store := &contextMemoryStore{facts: []memory.Fact{fact}}
	svc, err := NewServiceWithEngines(cfg, nil, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = fixture.profile
	result, err := svc.Run(context.Background(), Request{ModelID: "auto", Prompt: "useful fact"})
	if err != nil {
		t.Fatal(err)
	}
	if store.calls != 1 || !store.query.LocalOnly || store.query.Scope != "project" {
		t.Fatal("retrieval not frozen", store.calls, store.query)
	}
	history, err := InspectTask(context.Background(), cfg.Telemetry.Database, result.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range history.Messages {
		found = found || strings.Contains(message.Content, "A useful fact")
	}
	if !found {
		t.Fatal("injected fact absent from automatic execution")
	}
}
