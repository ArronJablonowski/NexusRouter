package app

import (
	"context"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

type routeSkillStore struct {
	skills.Store
	discoveries, loads int
}

func (s *routeSkillStore) Discover(ctx context.Context, scope string, tags []string, limit int) ([]skills.Metadata, error) {
	s.discoveries++
	return s.Store.Discover(ctx, scope, tags, limit)
}

func (s *routeSkillStore) Load(ctx context.Context, key skills.Key, version string) (skills.Version, error) {
	s.loads++
	return s.Store.Load(ctx, key, version)
}

func TestAutomaticRouteFreezesInjectedSkillOnce(t *testing.T) {
	fixture, cfg := autoFixture(t)
	file, settings := contextSkillStore(t)
	seedContextSkill(t, file, "project", "workflow", "general", "Injected workflow evidence", nil, true)
	cfg.Skills = settings
	store := &routeSkillStore{Store: file}
	svc, err := NewServiceWithStores(cfg, nil, nil, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = fixture.profile
	result, err := svc.Run(context.Background(), Request{ModelID: "auto", Prompt: "question"})
	if err != nil {
		t.Fatal(err)
	}
	if store.discoveries != 1 || store.loads != 1 {
		t.Fatal("skill snapshot not frozen", store.discoveries, store.loads)
	}
	history, err := InspectTask(context.Background(), cfg.Telemetry.Database, result.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range history.Messages {
		if strings.Contains(message.Content, "Injected workflow evidence") {
			return
		}
	}
	t.Fatal("injected workflow absent from automatic execution")
}
