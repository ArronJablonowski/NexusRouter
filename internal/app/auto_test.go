package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func autoFixture(t *testing.T) (*Service, config.Settings) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"z"}]}`)
			return
		}
		var b struct {
			Model string `json:"model"`
		}
		if json.NewDecoder(r.Body).Decode(&b) != nil {
			t.Error("bad body")
		}
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", b.Model)
	}))
	t.Cleanup(server.Close)
	cfg := config.Defaults()
	// These routing fixtures explicitly isolate their own tool catalogs.
	cfg.Tools.Enabled = false
	cfg.Mode = "local_only"
	cfg.Routing.Exploration = 0
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "auto.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: server.URL}}
	zero := 0.0
	for _, id := range []string{"a", "z"} {
		cfg.Models = append(cfg.Models, config.Model{ID: id, Model: id, Provider: "local", Locality: "local", Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero, RAMBytes: 100})
	}
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now(), TotalRAM: 1000, AvailableRAM: 1000}, nil
	}
	return svc, cfg
}

func TestAutomaticUsesDurableDomainFitnessAndAuditsBeforeTurn(t *testing.T) {
	ctx := context.Background()
	svc, cfg := autoFixture(t)
	prior, err := RunExplicit(ctx, cfg, Request{ModelID: "z", Prompt: "seed"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.Read(ctx, prior.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	attempt := ""
	for _, e := range events {
		if e.Kind == runtime.TurnStarted {
			attempt = e.AttemptID
		}
	}
	routingNow := time.Now().UTC()
	svc.now = func() time.Time { return routingNow }
	svc.settings.Routing.DecayOverrides = []config.DecayOverride{{Domain: "code", Profile: "default", HalfLife: "1h"}}
	err = db.RecordEvaluation(ctx, evaluation.Record{Version: 1, ID: "eval", TaskID: prior.TaskID, AttemptID: attempt, Key: routing.Key{Model: "z", Provider: "local", Domain: "code", Profile: "default"}, Checks: []evaluation.Check{{Source: evaluation.Deterministic, Reference: "fixture", Passed: true}}, ExecutionSucceeded: true, Time: routingNow.Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	out, err := svc.Run(ctx, Request{Prompt: "private payload", Domain: "code"})
	if err != nil || out.Text != "z" {
		t.Fatalf("%+v %v", out, err)
	}
	events, err = db.Read(ctx, out.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 3 || events[1].Kind != runtime.RouteSelected || events[2].Kind != runtime.TurnStarted {
		t.Fatal("route must be persisted before inference")
	}
	route := events[1]
	raw, _ := route.Encode()
	primary := route.Data.Route.Primary
	if strings.Contains(string(raw), "private payload") || strings.Contains(string(raw), cfg.Providers[0].Endpoint) || primary.Samples != 1 || primary.EffectiveSamples != .5 || primary.DecayContribution != .5 || !primary.WindowStart.Equal(routingNow.Add(-time.Hour)) || !primary.WindowEnd.Equal(routingNow.Add(-time.Hour)) || route.Data.ConfigID == "" {
		t.Fatalf("invalid audit %s", raw)
	}
	explanation, err := db.RouteExplanation(ctx, out.TaskID)
	if err != nil || explanation.Validate() != nil || explanation.Selection.Primary.DecayContribution != .5 {
		t.Fatalf("decayed route explanation unavailable: %+v %v", explanation, err)
	}
	isolated, err := svc.Run(ctx, Request{Prompt: "hello", Domain: "math"})
	if err != nil || isolated.Text != "a" {
		t.Fatalf("domain leaked %+v %v", isolated, err)
	}
}

func TestAutomaticRouteExplainsMissingCredentialWithoutPersistingIt(t *testing.T) {
	ctx := context.Background()
	svc, cfg := autoFixture(t)
	secured := cfg.Providers[0]
	secured.ID, secured.APIKeyEnv = "secured", "DARWIN_TEST_MISSING_PROVIDER_KEY"
	svc.settings.Providers = append(svc.settings.Providers, secured)
	for i := range svc.settings.Models {
		if svc.settings.Models[i].ID == "z" {
			svc.settings.Models[i].Provider = secured.ID
		}
	}

	result, err := svc.Run(ctx, Request{Prompt: "credential route", Domain: "general"})
	if err != nil || result.Text != "a" {
		t.Fatalf("healthy credential-free route not selected: %+v %v", result, err)
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
	for _, event := range events {
		if event.Kind != runtime.RouteSelected {
			continue
		}
		for _, exclusion := range event.Data.Route.Excluded {
			if exclusion.Model != "z" || exclusion.Provider != secured.ID {
				continue
			}
			credential, policy := false, false
			for _, reason := range exclusion.Reasons {
				credential = credential || reason == "credential"
				policy = policy || reason == "policy"
			}
			body, _ := event.Encode()
			if !credential || policy || strings.Contains(string(body), secured.APIKeyEnv) {
				t.Fatalf("credential exclusion was ambiguous or disclosed configuration: %s", body)
			}
			return
		}
	}
	t.Fatal("missing credential exclusion not persisted")
}

func TestAutoUnknownMetadataAndSharedReservations(t *testing.T) {
	ctx := context.Background()
	svc, _ := autoFixture(t)
	snapshot, _ := svc.profile(ctx)
	release, err := svc.budget.Reserve(snapshot, resources.Need{RAM: 100}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Run(ctx, Request{Prompt: "hello"}); !errors.Is(err, routing.ErrNoRoute) {
		t.Fatalf("shared capacity ignored: %v", err)
	}
	release()
	if _, err = svc.Run(ctx, Request{Prompt: "hello"}); err != nil {
		t.Fatal(err)
	}
	for i := range svc.settings.Models {
		svc.settings.Models[i].ContextTokens = 0
	}
	if _, err = svc.Run(ctx, Request{Prompt: "hello"}); !errors.Is(err, routing.ErrNoRoute) {
		t.Fatalf("unknown context admitted: %v", err)
	}
}

func TestAutomaticContinuationKeepsLocalPrivacy(t *testing.T) {
	ctx := context.Background()
	svc, cfg := autoFixture(t)
	prior, err := RunExplicit(ctx, cfg, Request{ModelID: "a", Prompt: "private history"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.settings.Mode = "hybrid"
	for i := range svc.settings.Models {
		svc.settings.Models[i].Locality = "cloud"
	}
	if _, err = svc.Run(ctx, Request{Prompt: "follow up", ContinueTaskID: prior.TaskID}); !errors.Is(err, routing.ErrNoRoute) {
		t.Fatalf("local history admitted to cloud: %v", err)
	}
}

func TestExplicitStructuredMessagesArePreserved(t *testing.T) {
	ctx := context.Background()
	_, cfg := autoFixture(t)
	messages := []providers.Message{{Role: "system", Content: "system instruction"}, {Role: "user", Content: "hello"}}
	out, err := RunExplicit(ctx, cfg, Request{ModelID: "a", Messages: messages}, nil)
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.Read(ctx, out.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events[0].Data.Messages) != 2 || events[0].Data.Messages[0].Role != "system" || out.FinishReason != "stop" {
		t.Fatal("message roles or completion reason lost")
	}
	if _, err = RunExplicit(ctx, cfg, Request{ModelID: "a", Messages: messages, Prompt: "mixed"}, nil); err != ErrAdmission {
		t.Fatal("ambiguous input admitted")
	}
}
