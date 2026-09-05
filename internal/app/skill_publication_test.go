package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func publicationFixture(t *testing.T) (*Service, skills.GenerationAttempt) {
	t.Helper()
	svc, tasks := skillGenerationAppFixture(t)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc.settings.Skills.Root = filepath.Join(parent, "catalog")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const draft = `{"version":1,"description":"Publication workflow","tags":["creative"],"steps":["Inspect requirements"],"required_tools":[],"configuration":"","risks":[],"validation_cases":["Check constraints"]}`
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", draft)
	}))
	t.Cleanup(server.Close)
	svc.settings.Providers[0].Endpoint = server.URL
	a, err := svc.GenerateSkillDraft(context.Background(), "publication", "a", skills.Key{Scope: "project", Name: "workflow"}, tasks, 0)
	if err != nil {
		t.Fatal(err)
	}
	return svc, a
}

func TestPublishSkillGenerationInactiveRepeatRestart(t *testing.T) {
	svc, a := publicationFixture(t)
	ctx := context.Background()
	version, err := svc.PublishSkillGeneration(ctx, a.ID)
	if err != nil || version.ID == "" || !reflect.DeepEqual(version.Draft, a.Result.Draft) {
		t.Fatal(version, err)
	}
	for range 2 {
		restarted, err := NewService(svc.settings, svc.secret)
		if err != nil {
			t.Fatal(err)
		}
		again, err := restarted.PublishSkillGeneration(ctx, a.ID)
		if err != nil || !reflect.DeepEqual(again, version) {
			t.Fatal("publication not idempotent", again, err)
		}
	}
	store, err := skills.OpenReadOnly(svc.settings.Skills.Root, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	history, err := store.History(ctx, a.Key)
	if err != nil || history.Active != "" || len(history.Versions) != 1 || len(history.Activations) != 0 {
		t.Fatal("publication activated or duplicated", history, err)
	}
	db, err := telemetry.OpenReadOnly(ctx, svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	saved, err := db.SkillGenerationAttempt(ctx, a.ID)
	if err != nil || !reflect.DeepEqual(saved, a) {
		t.Fatal("publication changed attempt", err)
	}
}

func TestPublishSkillGenerationAdmissionDoesNotCreateCatalog(t *testing.T) {
	for _, kind := range []string{"missing", "failed", "scope", "secret", "disabled", "automatic", "injected", "canceled", "nil-context"} {
		t.Run(kind, func(t *testing.T) {
			svc, a := publicationFixture(t)
			ctx := context.Background()
			id := a.ID
			switch kind {
			case "missing":
				id = "missing"
			case "failed":
				db, err := telemetry.Open(ctx, svc.settings.Telemetry.Database)
				if err != nil {
					t.Fatal(err)
				}
				b := a
				b.ID, b.Status, b.Result, b.FinishedAt = "failed", "started", nil, time.Time{}
				if err = db.BeginSkillGeneration(ctx, b); err != nil {
					t.Fatal(err)
				}
				b.Status, b.Code, b.FinishedAt = "failed", "generation_failed", a.FinishedAt
				if err = db.FinishSkillGeneration(ctx, b); err != nil {
					t.Fatal(err)
				}
				db.Close()
				id = b.ID
			case "scope":
				svc.settings.Skills.Scope = "other"
			case "secret":
				svc.secret = func(string) string { return "Inspect requirements" }
			case "disabled":
				svc.settings.Skills.Enabled = false
			case "automatic":
				svc.settings.Skills.AutoDraft = false
			case "injected":
				injected, _ := contextSkillStore(t)
				svc.skillStore = injected
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "nil-context":
				ctx = nil
			}
			got, err := svc.PublishSkillGeneration(ctx, id)
			if err == nil || !reflect.DeepEqual(got, skills.Version{}) {
				t.Fatal("invalid publication accepted", got, err)
			}
			if _, err = os.Stat(svc.settings.Skills.Root); !os.IsNotExist(err) {
				t.Fatal("rejection created catalog", err)
			}
		})
	}
	var svc *Service
	if _, err := svc.PublishSkillGeneration(context.Background(), "publication"); err == nil {
		t.Fatal("nil service accepted")
	}
}
