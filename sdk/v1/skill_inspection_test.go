package v1_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func TestSDKSkillGenerationInspectionReadOnly(t *testing.T) {
	options, database := sdkToolOptions(t)
	var calls atomic.Int32
	options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		calls.Add(1)
		return nil, errors.New("must not invoke")
	})
	ctx := context.Background()
	store, err := telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	a := skills.GenerationAttempt{Version: 1, ID: "attempt-a", Key: skills.Key{Scope: "project", Name: "workflow"}, Model: "fixture", Provider: "local", InputDigest: strings.Repeat("a", 64), SourceSessions: []string{"session-a", "session-b"}, SourceEvidence: []string{"proof-a"}, Status: "started", StartedAt: time.Now().UTC()}
	if err = store.BeginSkillGeneration(ctx, a); err != nil {
		t.Fatal(err)
	}
	a.Status, a.FinishedAt = "drafted", a.StartedAt.Add(time.Millisecond)
	a.Result = &skills.ModelDraftResult{Model: a.Model, Elapsed: time.Millisecond, Draft: skills.Draft{Key: a.Key, Description: "private-proposal", Tags: []string{"go"}, SourceSessions: a.SourceSessions, SourceEvidence: a.SourceEvidence, Steps: []string{"private-step"}, ValidationCases: []string{"Check fixture"}}}
	if err = store.FinishSkillGeneration(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := a
	b.ID, b.Status, b.Result, b.FinishedAt = "attempt-b", "started", nil, time.Time{}
	if err = store.BeginSkillGeneration(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.InspectSkillGeneration(ctx, "project", a.ID)
	if err != nil || !reflect.DeepEqual(got, a) {
		t.Fatalf("show: %+v %v", got, err)
	}
	got.Result.Draft.Steps[0] = "mutated"
	fresh, err := client.InspectSkillGeneration(ctx, "project", a.ID)
	if err != nil || !reflect.DeepEqual(fresh, a) {
		t.Fatal("inspection mutated durable proposal", err)
	}
	wrong, err := client.InspectSkillGeneration(ctx, "other", a.ID)
	if err == nil || !reflect.DeepEqual(wrong, skills.GenerationAttempt{}) {
		t.Fatal("scope mismatch exposed proposal")
	}
	page, err := client.ListSkillGenerations(ctx, "project", "", 1)
	if err != nil || len(page) != 1 || page[0].ID != a.ID || !page[0].HasResult {
		t.Fatalf("list: %+v %v", page, err)
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range []string{"private-proposal", "private-step", "session-a", "proof-a", "SourceSessions", "SourceEvidence", "Draft", "Usage"} {
		if strings.Contains(string(raw), sensitive) {
			t.Fatalf("list exposed %s", sensitive)
		}
	}
	next, err := client.ListSkillGenerations(ctx, "project", page[0].ID, 1)
	if err != nil || len(next) != 1 || next[0].ID != b.ID || next[0].HasResult {
		t.Fatalf("cursor: %+v %v", next, err)
	}
	other, err := client.ListSkillGenerations(ctx, "other", "", 25)
	if err != nil || len(other) != 0 {
		t.Fatal("scope leaked metadata", err)
	}
	if calls.Load() != 0 {
		t.Fatal("inspection invoked provider")
	}
}

func TestSDKSkillGenerationInspectionGuardsAndNoInitialization(t *testing.T) {
	for _, client := range []*sdk.Client{nil, {}} {
		got, err := client.InspectSkillGeneration(context.Background(), "project", "attempt")
		if !errors.Is(err, sdk.ErrAdmission) || !reflect.DeepEqual(got, skills.GenerationAttempt{}) {
			t.Fatal("invalid client show accepted")
		}
		page, err := client.ListSkillGenerations(context.Background(), "project", "", 25)
		if !errors.Is(err, sdk.ErrAdmission) || len(page) != 0 {
			t.Fatal("invalid client list accepted")
		}
	}
	options, database := sdkToolOptions(t)
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, canceled, context.Background()} {
		got, err := client.InspectSkillGeneration(ctx, "project", "attempt")
		if err == nil || !reflect.DeepEqual(got, skills.GenerationAttempt{}) {
			t.Fatal("missing storage show accepted")
		}
		page, err := client.ListSkillGenerations(ctx, "project", "", 25)
		if err == nil || len(page) != 0 {
			t.Fatal("missing storage list accepted")
		}
	}
	if _, err = os.Stat(database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("inspection initialized storage", err)
	}
}
