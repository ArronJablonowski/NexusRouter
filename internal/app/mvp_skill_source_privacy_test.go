package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestMVPGeneratedSkillSourcePrivacySurvivesRestartAndCloudAdmission(t *testing.T) {
	for _, test := range []struct {
		name, sourceModel, wantPrivacy string
		cloudAllowed                   bool
	}{
		{name: "local-only source", sourceModel: "a", wantPrivacy: skills.PrivacyLocalOnly},
		{name: "public-only sources", sourceModel: "z", wantPrivacy: skills.PrivacyPublic, cloudAllowed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, cfg := autoFixture(t)
			privateRoot, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			cfg.Mode = "hybrid"
			cfg.Models[1].Locality = "cloud"
			cfg.Skills.Enabled = false
			cfg.Skills.AutoDraft = true
			cfg.Skills.AutoActivate = true
			cfg.Skills.LocalOnly = false
			cfg.Skills.Scope = "project"
			cfg.Skills.Root = filepath.Join(privateRoot, "skills")
			const skillBody = "SOURCE_PRIVACY_SKILL_BODY"
			factory := applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
				return delegateEstimatorProvider(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
					for _, message := range request.Messages {
						if strings.Contains(message.Content, "Generalize") {
							draft := `{"version":1,"description":"` + skillBody + `","tags":["creative"],"steps":["Apply the privacy-bound workflow"],"required_tools":[],"configuration":"","risks":[],"validation_cases":["Check the result"]}`
							return emit(providers.Chunk{Text: draft, Done: true, FinishReason: "stop"})
						}
					}
					return emit(providers.Chunk{Text: "accepted source result", Done: true, FinishReason: "stop"})
				}), nil
			})
			svc, err := NewServiceWithProviderFactory(cfg, nil, nil, nil, nil, factory)
			if err != nil {
				t.Fatal(err)
			}
			svc.profile = fixture.profile
			ctx := context.Background()
			var taskIDs []string
			for range 2 {
				result, err := svc.Run(ctx, Request{ModelID: test.sourceModel, Prompt: "source workflow", Domain: "creative"})
				if err != nil {
					t.Fatal(err)
				}
				if err = RecordFeedback(ctx, cfg.Telemetry.Database, result.TaskID, true, 0); err != nil {
					t.Fatal(err)
				}
				taskIDs = append(taskIDs, result.TaskID)
			}
			svc.settings.Skills.Enabled = true
			read, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			sources, err := read.SkillWorkflowSources(ctx, taskIDs)
			read.Close()
			if err != nil || len(sources) != 2 {
				t.Fatal("durable sources unavailable", sources, err)
			}
			for _, source := range sources {
				want := "cloud_allowed"
				if test.wantPrivacy == skills.PrivacyLocalOnly {
					want = "local_only"
				}
				if source.Privacy != want {
					t.Fatal("source privacy changed", source.Privacy, want)
				}
			}

			key := skills.Key{Scope: "project", Name: "source-privacy"}
			attempt, err := svc.GenerateSkillDraft(ctx, "source-privacy-generation", "a", key, taskIDs, 0)
			if err != nil || attempt.Validate() != nil || attempt.Result == nil || attempt.Result.Draft.Privacy != test.wantPrivacy {
				t.Fatal("generation lost source privacy", attempt, err)
			}
			persisted, inspectErr := InspectSkillGeneration(ctx, cfg.Telemetry.Database, key.Scope, attempt.ID)
			if inspectErr != nil || persisted.Validate() != nil || persisted.Result == nil || persisted.Result.Draft.Privacy != test.wantPrivacy {
				t.Fatal("persisted generation lost source privacy", persisted, inspectErr)
			}
			if err = svc.settings.Validate(); err != nil {
				t.Fatal("generated service configuration became invalid", err)
			}
			if !svc.settings.Skills.Enabled || !svc.settings.Skills.AutoDraft || svc.skillStore != nil {
				t.Fatal("publication preconditions changed", svc.settings.Skills, svc.skillStore)
			}
			version, err := svc.PublishSkillGeneration(ctx, attempt.ID)
			if err != nil || version.Draft.Privacy != test.wantPrivacy {
				t.Fatal("publication lost source privacy", version, err)
			}
			state, err := svc.SkillActivationState(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			validator := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
				return skills.Evidence{ID: "source-privacy-check", Passed: true, Deterministic: true}, nil
			})
			if err = svc.ActivateSkillVersion(ctx, state, version.ID, validator); err != nil {
				t.Fatal(err)
			}

			restarted, err := NewService(svc.settings, svc.secret)
			if err != nil {
				t.Fatal(err)
			}
			restarted.profile = fixture.profile
			store, err := skills.OpenReadOnly(cfg.Skills.Root, []string{cfg.Skills.Scope})
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := store.Load(ctx, key, "")
			metadata, discoverErr := store.Discover(ctx, key.Scope, []string{"creative"}, 1)
			store.Close()
			if err != nil || discoverErr != nil || loaded.Draft.Privacy != test.wantPrivacy || len(metadata) != 1 || metadata[0].Privacy != test.wantPrivacy {
				t.Fatal("restart lost durable privacy", loaded, metadata, err, discoverErr)
			}

			beforeDB, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			before, err := beforeDB.ListTasks(ctx, sessions.TaskListOptions{Limit: 100})
			beforeDB.Close()
			if err != nil {
				t.Fatal(err)
			}
			var builds atomic.Int32
			var received atomic.Bool
			restarted.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
				builds.Add(1)
				return delegateEstimatorProvider(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
					body, _ := json.Marshal(request.Messages)
					received.Store(strings.Contains(string(body), skillBody))
					return emit(providers.Chunk{Text: "cloud result", Done: true, FinishReason: "stop"})
				}), nil
			})
			result, runErr := restarted.Run(ctx, Request{ModelID: "z", Prompt: "use the workflow", Domain: "creative"})
			if test.cloudAllowed {
				if runErr != nil || result.Text != "cloud result" || builds.Load() != 1 || !received.Load() {
					t.Fatal("public skill was not usable by cloud model", result, runErr, builds.Load(), received.Load())
				}
				return
			}
			if !errors.Is(runErr, ErrAdmission) || result.TaskID != "" || builds.Load() != 0 || received.Load() {
				t.Fatal("local-only skill crossed cloud admission", result, runErr, builds.Load(), received.Load())
			}
			afterDB, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			after, err := afterDB.ListTasks(ctx, sessions.TaskListOptions{Limit: 100})
			if err != nil || len(after.Items) != len(before.Items) {
				afterDB.Close()
				t.Fatal("privacy rejection persisted a task/error", len(before.Items), len(after.Items), err)
			}
			for _, item := range after.Items {
				events, readErr := afterDB.Read(ctx, item.TaskID, 0, 100)
				if readErr != nil {
					afterDB.Close()
					t.Fatal(readErr)
				}
				body, _ := json.Marshal(events)
				if strings.Contains(string(body), skillBody) {
					afterDB.Close()
					t.Fatal("rejected private skill body persisted in task events")
				}
			}
			afterDB.Close()
		})
	}
}
