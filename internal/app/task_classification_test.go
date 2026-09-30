package app

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

func TestClassifyRequestIntentUsesOnlyStructuredEvidence(t *testing.T) {
	tests := []struct {
		name       string
		request    Request
		domain     string
		profile    string
		capability []string
		domainSet  bool
		capsSet    bool
		ambiguous  bool
	}{
		{name: "automatic defaults", request: Request{ModelID: "auto", Prompt: "write a poem and solve code"}, domain: "general", profile: "default", capability: []string{"chat"}, ambiguous: true},
		{name: "implicit automatic defaults", request: Request{Prompt: "write Go"}, domain: "general", profile: "default", capability: []string{"chat"}, ambiguous: true},
		{name: "explicit preserved", request: Request{ModelID: "model", Domain: "project.special", Profile: "careful mode"}, domain: "project.special", profile: "careful mode", domainSet: true},
		{name: "explicit has no capability default", request: Request{ModelID: "model"}, domain: "general", profile: "default", ambiguous: true},
		{name: "validation wins", request: Request{ModelID: "model", Validation: "go_source", Capabilities: []string{"math"}}, domain: "code", profile: "default", capability: []string{"math"}, capsSet: true},
		{name: "code aliases agree", request: Request{Capabilities: []string{"coding", "debugging", "chat"}}, domain: "code", profile: "default", capability: []string{"coding", "debugging", "chat"}, capsSet: true},
		{name: "math", request: Request{Capabilities: []string{"mathematics"}}, domain: "math", profile: "default", capability: []string{"mathematics"}, capsSet: true},
		{name: "structured", request: Request{Capabilities: []string{"structured_output"}}, domain: "structured_json", profile: "default", capability: []string{"structured_output"}, capsSet: true},
		{name: "creative", request: Request{Capabilities: []string{"creative_writing"}}, domain: "creative", profile: "default", capability: []string{"creative_writing"}, capsSet: true},
		{name: "conflict", request: Request{Capabilities: []string{"code", "math"}}, domain: "general", profile: "default", capability: []string{"code", "math"}, capsSet: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			originalDomain, originalProfile := test.request.Domain, test.request.Profile
			before := append([]string(nil), test.request.Capabilities...)
			got, err := classifyRequestIntent(test.request)
			if err != nil || got.Domain != test.domain || got.Profile != test.profile || !reflect.DeepEqual(got.Capabilities, test.capability) {
				t.Fatal(got, err)
			}
			if !got.intentPrepared || got.domainExplicit != test.domainSet || got.capabilitiesExplicit != test.capsSet || got.intentAmbiguous != test.ambiguous {
				t.Fatal("incorrect intent origin", got.domainExplicit, got.capabilitiesExplicit, got.intentAmbiguous)
			}
			if !reflect.DeepEqual(test.request.Capabilities, before) || test.request.Domain != originalDomain || test.request.Profile != originalProfile {
				t.Fatal("caller request mutated")
			}
		})
	}
}

func TestClassifyRequestIntentRejectsUnsafeExplicitLabels(t *testing.T) {
	for _, request := range []Request{
		{Domain: " domain"}, {Domain: "domain\nvalue"}, {Domain: string([]byte{0xff})},
		{Profile: "profile "}, {Profile: "profile\tvalue"}, {Profile: string([]byte{0xff})},
	} {
		if _, err := classifyRequestIntent(request); !errors.Is(err, ErrAdmission) {
			t.Fatal("unsafe label admitted", request, err)
		}
	}
}

func TestClassifiedDomainPrecedesSkillDiscoveryAndPersistsEveryUse(t *testing.T) {
	ctx := context.Background()
	fixture, cfg := autoFixture(t)
	for i := range fixture.settings.Models {
		fixture.settings.Models[i].Capabilities = append(fixture.settings.Models[i].Capabilities, "code")
	}
	store, settings := contextSkillStore(t)
	seedContextSkill(t, store, "project", "classified", "code", "classified-code-procedure", nil, true)
	fixture.settings.Skills = settings
	tracked := &routeSkillStore{Store: store}
	fixture.skillStore = tracked
	result, err := fixture.Run(ctx, Request{ModelID: "auto", Prompt: "neutral prompt", Capabilities: []string{"code"}})
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.Read(ctx, result.TaskID, 0, 100)
	if err != nil || tracked.discoveries != 1 || tracked.loads != 1 {
		t.Fatal(events, err, tracked.discoveries, tracked.loads)
	}
	foundRoute, foundEvaluation := false, false
	for _, event := range events {
		switch event.Kind {
		case runtime.TaskStarted:
			if event.Data.Domain != "code" || event.Data.Profile != "default" || event.Data.SkillContext == nil || len(event.Data.SkillContext.References) != 1 {
				t.Fatal("classified intent did not bind task start", event)
			}
		case runtime.RouteSelected:
			foundRoute = event.Data.Domain == "code" && event.Data.Profile == "default"
		case runtime.EvaluationRecorded:
			foundEvaluation = event.Data.Domain == "code" && event.Data.Profile == "default"
		}
	}
	if !foundRoute || !foundEvaluation {
		t.Fatal("classified intent diverged across consumers", events)
	}
}

func TestExplicitAndDelegatedClassificationIsDurable(t *testing.T) {
	ctx := context.Background()
	for _, delegated := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit defaults", true: "delegated validation"}[delegated], func(t *testing.T) {
			svc, cfg := autoFixture(t)
			request := Request{ModelID: "a", Prompt: "neutral"}
			if delegated {
				svc.settings.Workers.DelegateModel = "a"
				svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
					return containmentProvider{}, nil
				})
				request.Validation = "go_source"
			}
			var result Result
			var err error
			if delegated {
				result, err = svc.runDelegate(ctx, "neutral", "go_source", "parent", true, "", "")
			} else {
				result, err = svc.Run(ctx, request)
			}
			if delegated && (!errors.Is(err, runtime.ErrInvalidOutput) || result.TaskID == "") {
				t.Fatal("delegated Go validation did not fail durably", result, err)
			} else if !delegated && err != nil {
				t.Fatal(err)
			}
			db, openErr := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
			if openErr != nil {
				t.Fatal(openErr)
			}
			defer db.Close()
			events, readErr := db.Read(ctx, result.TaskID, 0, 100)
			if readErr != nil || len(events) == 0 {
				t.Fatal(events, readErr, result, err)
			}
			want := "general"
			if delegated {
				want = "code"
			}
			if events[0].Data.Domain != want || events[0].Data.Profile != "default" {
				t.Fatal("incorrect durable classification", events[0])
			}
			for _, event := range events {
				if event.Kind == runtime.EvaluationRecorded && (event.Data.Domain != want || event.Data.Profile != "default") {
					t.Fatal("evaluation classification diverged", event)
				}
			}
		})
	}
}

func TestRunExplicitAdmittedDefensivelyClassifiesIntent(t *testing.T) {
	ctx := context.Background()
	_, cfg := autoFixture(t)
	result, err := runExplicitAdmitted(ctx, cfg, Request{ModelID: "a", Prompt: "neutral"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.Read(ctx, result.TaskID, 0, 100)
	if err != nil || len(events) == 0 || events[0].Data.Domain != "general" || events[0].Data.Profile != "default" {
		t.Fatal("final explicit boundary persisted unclassified intent", events, err)
	}
}

func TestSubmissionDigestPreservesIntentOriginBeforeStorage(t *testing.T) {
	ctx := context.Background()
	svc := submissionService(t)
	key := "classified-key-0001"
	omitted := Request{ModelID: "auto", Prompt: "neutral"}
	first, err := svc.Submit(ctx, key, omitted)
	if err != nil || first.State != "queued" {
		t.Fatal(first, err)
	}
	again, err := svc.Submit(ctx, key, omitted)
	if err != nil || again.ID != first.ID {
		t.Fatal("omitted intent was not idempotent", again, err)
	}
	explicit := omitted
	explicit.Domain, explicit.Profile, explicit.Capabilities = "general", "default", []string{"chat"}
	if _, err := svc.Submit(ctx, key, explicit); !errors.Is(err, submissions.ErrConflict) {
		t.Fatal("explicit intent origin did not conflict", err)
	}
}

func TestUnsafeIntentFailsBeforeStorageOrProviderConstruction(t *testing.T) {
	ctx := context.Background()
	for _, queued := range []bool{false, true} {
		svc, cfg := autoFixture(t)
		calls := 0
		svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
			calls++
			return containmentProvider{}, nil
		})
		request := Request{ModelID: "a", Prompt: "neutral", Domain: "bad\ndomain"}
		var err error
		if queued {
			_, err = svc.Submit(ctx, "unsafe-domain-001", request)
		} else {
			_, err = svc.Run(ctx, request)
		}
		if !errors.Is(err, ErrAdmission) || calls != 0 {
			t.Fatal(queued, err, calls)
		}
		if _, statErr := os.Stat(cfg.Telemetry.Database); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatal("invalid classification touched storage", queued, statErr)
		}
	}
}
