package app

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/accounting"
	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

type forbiddenAuxiliaryProvider struct {
	streams *atomic.Int32
}

func (forbiddenAuxiliaryProvider) Models(context.Context) ([]string, error) {
	return []string{"a", "z"}, nil
}

func (p forbiddenAuxiliaryProvider) Stream(context.Context, providers.Request, func(providers.Chunk) error) error {
	p.streams.Add(1)
	return nil
}

type auxiliarySourceIdentity struct {
	session, turn, attempt string
}

func readAuxiliarySourceIdentity(t *testing.T, database, task string) (auxiliarySourceIdentity, []runtime.Event) {
	t.Helper()
	db, err := telemetry.OpenReadOnly(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	history, err := sessions.Replay(context.Background(), db, task)
	if err != nil {
		t.Fatal(err)
	}
	events, err := db.Read(context.Background(), task, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	identity := auxiliarySourceIdentity{session: history.SessionID}
	for _, event := range events {
		if event.Kind == runtime.TurnStarted {
			identity.turn, identity.attempt = event.TurnID, event.AttemptID
		}
	}
	if identity.session == "" || identity.turn == "" || identity.attempt == "" {
		t.Fatal("source identity unavailable")
	}
	return identity, events
}

func assertNoAuxiliaryPersistence(t *testing.T, database, task string, before []runtime.Event) {
	t.Helper()
	db, err := telemetry.OpenReadOnly(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reviews, reviewErr := db.ReviewAttempts(context.Background(), task, "", 100)
	audits, auditErr := db.Audits(context.Background(), task, "", 100)
	summaries, summaryErr := db.ListSummaryAttempts(context.Background(), task, "", 100)
	totals, totalsErr := db.UsageTotals(context.Background(), accounting.Scope{TaskID: task})
	after, eventErr := db.Read(context.Background(), task, 0, 100)
	if reviewErr != nil || auditErr != nil || summaryErr != nil || totalsErr != nil || eventErr != nil {
		t.Fatal(reviewErr, auditErr, summaryErr, totalsErr, eventErr)
	}
	if len(reviews) != 0 || len(audits) != 0 || len(summaries) != 0 || totals.Auxiliary.Records != 0 {
		t.Fatalf("secret-bearing auxiliary identity persisted: reviews=%+v audits=%+v summaries=%+v totals=%+v", reviews, audits, summaries, totals)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("denied auxiliary admission changed the durable source")
	}
}

func installForbiddenAuxiliaryFactory(svc *Service) (*atomic.Int32, *atomic.Int32) {
	var builds, streams atomic.Int32
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		builds.Add(1)
		return forbiddenAuxiliaryProvider{streams: &streams}, nil
	})
	return &builds, &streams
}

func TestAuditAdmissionRejectsSecretBearingDurableIdentities(t *testing.T) {
	for _, field := range []string{"reviewer", "model", "provider", "task", "session", "attempt", "turn"} {
		t.Run(field, func(t *testing.T) {
			svc, cfg := autoFixture(t)
			source, err := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "source"})
			if err != nil {
				t.Fatal(err)
			}
			identity, before := readAuxiliarySourceIdentity(t, cfg.Telemetry.Database, source.TaskID)
			secret := ""
			switch field {
			case "reviewer":
				secret = "z"
			case "model":
				svc.settings.Models[1].Model = "review-native-model"
				secret = svc.settings.Models[1].Model
			case "provider":
				secret = svc.settings.Models[1].Provider
			case "task":
				secret = source.TaskID
			case "session":
				secret = identity.session
			case "attempt":
				secret = identity.attempt
			case "turn":
				secret = identity.turn
			}
			svc.secret = func(name string) string {
				if name == "DARWIN_API_TOKEN" {
					return secret
				}
				return ""
			}
			builds, streams := installForbiddenAuxiliaryFactory(svc)
			if _, err := svc.AuditTask(context.Background(), source.TaskID, "z", 0); err == nil {
				t.Fatal("secret-bearing audit identity admitted")
			}
			if builds.Load() != 0 || streams.Load() != 0 {
				t.Fatalf("denied audit reached provider: builds=%d streams=%d", builds.Load(), streams.Load())
			}
			assertNoAuxiliaryPersistence(t, cfg.Telemetry.Database, source.TaskID, before)
		})
	}
}

func TestPublicAuditRejectsSecretBearingOperationIdentity(t *testing.T) {
	svc, cfg := autoFixture(t)
	source, err := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "source"})
	if err != nil {
		t.Fatal(err)
	}
	_, before := readAuxiliarySourceIdentity(t, cfg.Telemetry.Database, source.TaskID)
	request := evaluation.AuditRequest{Version: 1, TaskID: source.TaskID, ReviewerModelID: "z", IdempotencyKey: "operation-identity-key", MaxCost: 0}
	secret := auditOperationID(request.IdempotencyKey)
	svc.secret = func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return secret
		}
		return ""
	}
	builds, streams := installForbiddenAuxiliaryFactory(svc)
	if _, err := svc.RunAudit(context.Background(), request, func(evaluation.AuditEvent) error { return nil }); err == nil {
		t.Fatal("secret-bearing audit operation admitted")
	}
	if builds.Load() != 0 || streams.Load() != 0 {
		t.Fatalf("denied public audit reached provider: builds=%d streams=%d", builds.Load(), streams.Load())
	}
	assertNoAuxiliaryPersistence(t, cfg.Telemetry.Database, source.TaskID, before)
}

func TestSummaryAdmissionRejectsSecretBearingDurableIdentities(t *testing.T) {
	for _, field := range []string{"model_id", "model", "provider", "task", "session"} {
		t.Run(field, func(t *testing.T) {
			svc, cfg := autoFixture(t)
			source, err := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "source"})
			if err != nil {
				t.Fatal(err)
			}
			identity, before := readAuxiliarySourceIdentity(t, cfg.Telemetry.Database, source.TaskID)
			secret := ""
			switch field {
			case "model_id":
				secret = "a"
			case "model":
				svc.settings.Models[0].Model = "summary-native-model"
				secret = svc.settings.Models[0].Model
			case "provider":
				secret = svc.settings.Models[0].Provider
			case "task":
				secret = source.TaskID
			case "session":
				secret = identity.session
			}
			svc.secret = func(name string) string {
				if name == "DARWIN_API_TOKEN" {
					return secret
				}
				return ""
			}
			builds, streams := installForbiddenAuxiliaryFactory(svc)
			if _, err := svc.SummarizeTask(context.Background(), source.TaskID, "a", 1, 0); err == nil {
				t.Fatal("secret-bearing summary identity admitted")
			}
			if builds.Load() != 0 || streams.Load() != 0 {
				t.Fatalf("denied summary reached provider: builds=%d streams=%d", builds.Load(), streams.Load())
			}
			assertNoAuxiliaryPersistence(t, cfg.Telemetry.Database, source.TaskID, before)
		})
	}
}
