package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

type rotationGenerationProvider func(context.Context, providers.Request, func(providers.Chunk) error) error

func (p rotationGenerationProvider) Models(context.Context) ([]string, error) {
	return []string{"a"}, nil
}
func (p rotationGenerationProvider) Stream(ctx context.Context, r providers.Request, emit func(providers.Chunk) error) error {
	return p(ctx, r, emit)
}

func TestSkillGenerationCredentialRotationRedactsActualAndNewSecrets(t *testing.T) {
	for _, mode := range []string{"actual-api-key", "after-inference", "after-inference-tag"} {
		t.Run(mode, func(t *testing.T) {
			svc, tasks := skillGenerationAppFixture(t)
			svc.settings.Providers[0].APIKeyEnv = "ROTATING_KEY"
			lookups := 0
			runtimeSecret := "original-runtime-key"
			svc.secret = func(name string) string {
				if name == "DARWIN_API_TOKEN" {
					return runtimeSecret
				}
				if name == "ROTATING_KEY" {
					lookups++
					if lookups == 2 {
						return "transient-actual-key"
					}
					return "later-key"
				}
				return ""
			}
			var actual string
			svc.providerFactory = applicationProviderFactory(func(_ context.Context, c providers.Connection) (providers.Provider, error) {
				actual = c.APIKey
				return rotationGenerationProvider(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
					secret := actual
					if mode != "actual-api-key" {
						runtimeSecret = "fresh-runtime-key"
						secret = runtimeSecret
					}
					tags := `[]`
					if mode == "after-inference-tag" {
						tags = fmt.Sprintf(`[%q]`, secret)
					}
					content := fmt.Sprintf(`{"version":1,"description":%q,"tags":%s,"steps":["Inspect input"],"required_tools":[],"configuration":"","risks":[],"validation_cases":["Check output"]}`, "Workflow mentioning "+secret, tags)
					return emit(providers.Chunk{Text: content, Done: true, FinishReason: "stop"})
				}), nil
			})
			a, err := svc.GenerateSkillDraft(context.Background(), "rotation", "a", skills.Key{Scope: "project", Name: "workflow"}, tasks, 0)
			if actual != "transient-actual-key" {
				t.Fatal("fixture did not exercise transient actual key", actual)
			}
			if mode == "after-inference-tag" {
				if err == nil || a.Status != "failed" || a.Result != nil {
					t.Fatal("invalid redacted tag accepted", a, err)
				}
			} else if err != nil || a.Status != "drafted" {
				t.Fatal(a, err)
			}
			db, readErr := telemetry.OpenReadOnly(context.Background(), svc.settings.Telemetry.Database)
			if readErr != nil {
				t.Fatal(readErr)
			}
			defer db.Close()
			saved, readErr := db.SkillGenerationAttempt(context.Background(), a.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			body, _ := json.Marshal(saved)
			for _, secret := range []string{"transient-actual-key", "fresh-runtime-key", "original-runtime-key"} {
				if strings.Contains(string(body), secret) || (err != nil && strings.Contains(err.Error(), secret)) {
					t.Fatal("rotated credential persisted")
				}
			}
			if mode != "after-inference-tag" && !strings.Contains(string(body), "[REDACTED]") {
				t.Fatal("rotated credential not redacted")
			}
		})
	}
}

func TestWorkflowSelectionHelperOnlyCredentialCannotEnterMetadata(t *testing.T) {
	for _, field := range []string{"group", "task"} {
		t.Run(field, func(t *testing.T) {
			svc, tasks := skillGenerationAppFixture(t)
			svc.settings.Providers[0].APIKeyEnv = "ROTATING_KEY"
			group := "unique-group-credential"
			transient := group
			if field == "task" {
				transient = tasks[0]
			}
			lookups := 0
			svc.secret = func(name string) string {
				if name == "ROTATING_KEY" {
					lookups++
					if lookups == 3 {
						return transient
					}
					return "stable-key"
				}
				return ""
			}
			_, err := svc.PlanWorkflowSelection(context.Background(), "a", skills.Key{Scope: "project", Name: "workflow"}, group, "fixture_v1", tasks, 0)
			if err == nil || lookups < 3 {
				t.Fatal("helper-only credential persisted as metadata", err, lookups)
			}
			db, err := telemetry.OpenReadOnly(context.Background(), svc.settings.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			selections, err := db.ListWorkflowSelections(context.Background(), "project", "", 10)
			if err != nil || len(selections) != 0 {
				t.Fatal("denied plan saved selection", selections, err)
			}
		})
	}
}

func TestSkillSelectionHelperOnlyCredentialRedactsSourcePrompt(t *testing.T) {
	svc, tasks := skillGenerationAppFixture(t)
	svc.settings.Providers[0].APIKeyEnv = "ROTATING_KEY"
	svc.secret = func(name string) string {
		if name == "ROTATING_KEY" {
			return "stable-key"
		}
		return ""
	}
	selection, err := svc.PlanWorkflowSelection(context.Background(), "a", skills.Key{Scope: "project", Name: "workflow"}, "writing", "fixture_v1", tasks, 0)
	if err != nil {
		t.Fatal(err)
	}
	lookups := 0
	svc.secret = func(name string) string {
		if name == "ROTATING_KEY" {
			lookups++
			if lookups == 5 {
				return "runtime-token"
			}
			return "stable-key"
		}
		return ""
	}
	calls := 0
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return rotationGenerationProvider(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
			calls++
			body, _ := json.Marshal(r)
			if strings.Contains(string(body), "runtime-token") || !strings.Contains(string(body), "[REDACTED]") {
				t.Error("helper-only credential entered prompt")
			}
			return emit(providers.Chunk{Text: `{"version":1,"description":"Workflow runtime-token","tags":[],"steps":["Inspect input"],"required_tools":[],"configuration":"","risks":[],"validation_cases":["Check output"]}`, Done: true, FinishReason: "stop"})
		}), nil
	})
	a, err := svc.GenerateSkillSelection(context.Background(), selection.ID, 0)
	if err != nil || a.Status != "drafted" || calls != 1 {
		t.Fatal(a, err, calls)
	}
	body, _ := json.Marshal(a)
	if strings.Contains(string(body), "runtime-token") || !strings.Contains(string(body), "[REDACTED]") {
		t.Fatal("helper-only credential persisted in draft")
	}
}
