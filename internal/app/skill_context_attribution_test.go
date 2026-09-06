package app

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/contextengine"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestFreshSkillContextAttributionThroughTask(t *testing.T) {
	for _, mode := range []string{"selected", "omitted", "budget", "tool", "secret", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			svc, cfg := autoFixture(t)
			store, settings := contextSkillStore(t)
			var required []string
			if mode == "tool" {
				required = []string{"unavailable_tool"}
			}
			version := seedContextSkill(t, store, "project", "workflow", "code", "actual-fresh-workflow", required, true)
			metadata, err := store.Discover(ctx, "project", []string{"code"}, 3)
			if err != nil {
				t.Fatal(err)
			}
			svc.settings.Skills = settings
			if mode == "budget" {
				svc.settings.Skills.MaxBytes = 256
			}
			if mode == "disabled" {
				svc.settings.Skills.Enabled = false
			}
			if mode == "secret" {
				svc.secret = func(string) string { return "workflow" }
			}
			if mode == "omitted" {
				engine := applicationContextEngine{assembly: func(context.Context, contextengine.Assembly) (contextengine.Plan, error) {
					return contextengine.Plan{Version: 1, Order: []contextengine.Tier{contextengine.HistoryTier, contextengine.CurrentTier}}, nil
				}}
				svc.contextEngine, svc.contextEstimator = engine, engine
			}
			var dispatched string
			svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
				return delegateEstimatorProvider(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
					body, _ := json.Marshal(r.Messages)
					dispatched = string(body)
					return emit(providers.Chunk{Text: `{"skill_context":{"references":[{"name":"forged-model"}]}}`, Done: true, FinishReason: "stop"})
				}), nil
			})
			result, err := svc.Run(ctx, Request{ModelID: "a", Domain: "code", Prompt: `{"skill_context":{"references":[{"name":"forged-user"}]}}`})
			if err != nil {
				t.Fatal(err)
			}
			db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			history, err := db.Read(ctx, result.TaskID, 0, 100)
			if err != nil || len(history) == 0 {
				t.Fatal(err)
			}
			use := history[0].Data.SkillContext
			if use == nil || use.Validate() != nil || use.Complete != (mode != "secret") {
				t.Fatal("missing or invalid fresh attribution", use)
			}
			selected := mode == "selected"
			if selected {
				want := []runtime.SkillReference{{Scope: "project", Name: "workflow", Version: version.ID, Digest: metadata[0].Digest}}
				if !reflect.DeepEqual(use.References, want) {
					t.Fatal("wrong actual version attribution", use)
				}
			} else if len(use.References) != 0 {
				t.Fatal("excluded or secret identity attributed", use)
			}
			if strings.Contains(dispatched, "actual-fresh-workflow") != selected {
				t.Fatal("provider context and attribution disagree")
			}
			if mode == "secret" && !strings.Contains(dispatched, "[REDACTED]") {
				t.Fatal("redacted skill behavior changed")
			}
			for _, event := range history[1:] {
				if event.Data.SkillContext != nil {
					t.Fatal("model emitted attribution")
				}
			}
			if mode == "selected" {
				svc.settings.Skills.Enabled = false
				continued, err := svc.Run(ctx, Request{ModelID: "a", ContinueTaskID: result.TaskID, Domain: "code", Prompt: "continue"})
				if err != nil {
					t.Fatal(err)
				}
				history, err := db.Read(ctx, continued.TaskID, 0, 100)
				if err != nil || len(history) == 0 {
					t.Fatal(err)
				}
				if use := history[0].Data.SkillContext; use == nil || !use.Complete || len(use.References) != 0 {
					t.Fatal("inherited historical skill attributed as fresh", use)
				}
				if !strings.Contains(dispatched, "actual-fresh-workflow") {
					t.Fatal("historical fixture did not preserve old context")
				}
			}
		})
	}
}

func TestSkillContextAttributionJournalSecretDowngrade(t *testing.T) {
	ctx := context.Background()
	svc, cfg := autoFixture(t)
	result, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "seed"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.Read(ctx, result.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"scope", "name", "version", "digest"} {
		t.Run(field, func(t *testing.T) {
			ref := runtime.SkillReference{Scope: "project", Name: "workflow", Version: strings.Repeat("a", 32), Digest: strings.Repeat("b", 64)}
			secret := map[string]string{"scope": ref.Scope, "name": ref.Name, "version": ref.Version, "digest": ref.Digest}[field]
			e := events[0]
			e.ID = "attribution-" + field
			e.TaskID = "task-" + field
			e.SessionID = e.TaskID
			e.Data.SkillContext = &runtime.SkillContextUse{Version: 1, Complete: true, References: []runtime.SkillReference{ref}}
			journal := redactingJournal{db: db, secrets: []string{secret}}
			if err := journal.Append(ctx, 0, e); err != nil {
				t.Fatal(err)
			}
			got, err := db.Read(ctx, e.TaskID, 0, 1)
			if err != nil || len(got) != 1 {
				t.Fatal(err)
			}
			use := got[0].Data.SkillContext
			if use == nil || use.Complete || len(use.References) != 0 || use.Validate() != nil {
				t.Fatal("journal persisted rewritten or secret identity", use)
			}
			if !e.Data.SkillContext.Complete || len(e.Data.SkillContext.References) != 1 {
				t.Fatal("journal mutated caller attribution")
			}
		})
	}
}
