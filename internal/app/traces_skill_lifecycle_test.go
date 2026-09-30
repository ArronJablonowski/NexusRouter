package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestTraceSnapshotMergesContentFreeSkillCatalogLifecycle(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	directory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Telemetry.Database = filepath.Join(directory, "state.db")
	cfg.Skills.Root, cfg.Skills.Scope = filepath.Join(directory, "skills"), "project"
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := skills.Open(cfg.Skills.Root, []string{cfg.Skills.Scope})
	if err != nil {
		t.Fatal(err)
	}
	draft := skills.Draft{Key: skills.Key{Scope: cfg.Skills.Scope, Name: "private-skill"}, Description: "private description", SourceSessions: []string{"private-session"}, Steps: []string{"private step"}, ValidationCases: []string{"private fixture"}}
	validator := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		return skills.Evidence{ID: "private-proof", Passed: true, Deterministic: true}, nil
	})
	first, err := store.Draft(ctx, draft, false)
	if err != nil || store.Activate(ctx, draft.Key, first.ID, "", validator, false) != nil {
		t.Fatal(first, err)
	}
	draft.Steps = []string{"private newer step"}
	second, err := store.Draft(ctx, draft, false)
	if err != nil || store.Activate(ctx, draft.Key, second.ID, first.ID, validator, false) != nil || store.Rollback(ctx, draft.Key, second.ID, false) != nil {
		t.Fatal(second, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.TraceSnapshot(ctx, 10)
	if err != nil || snapshot.Validate() != nil || len(snapshot.Traces) != 3 {
		t.Fatal(snapshot, err)
	}
	want := map[string]int{"skill_activation/activated": 2, "skill_rollback/rolled_back": 1}
	for _, trace := range snapshot.Traces {
		if len(trace.Spans) != 1 {
			t.Fatal(trace)
		}
		root := trace.Spans[0]
		want[root.Name+"/"+root.Outcome]--
	}
	for key, count := range want {
		if count != 0 {
			t.Fatal("wrong lifecycle count", key, count, snapshot)
		}
	}
	body, marshalErr := json.Marshal(snapshot)
	for _, private := range []string{"private-skill", "private description", "private-session", "private-proof", first.ID, second.ID} {
		if strings.Contains(string(body), private) {
			t.Fatal("private catalog data escaped", private)
		}
	}
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	tooEarly := snapshot
	tooEarly.ObservedAt = snapshot.Traces[len(snapshot.Traces)-1].Spans[0].StartedAt.Add(-1)
	tooEarly.Traces = nil
	if service.appendSkillCatalogTraces(ctx, &tooEarly, 10) == nil {
		t.Fatal("cross-store future transition accepted")
	}
}
