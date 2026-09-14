package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/accounting"
	"github.com/ArronJablonowski/DarwinRouter/classification"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

type intentFixtureProvider struct {
	purpose   providers.Purpose
	auxiliary *atomic.Int32
	invalid   bool
	unhealthy bool
	forbidden string
	leaked    *atomic.Bool
}

func (p intentFixtureProvider) Models(context.Context) ([]string, error) {
	if p.unhealthy && p.purpose == providers.PurposeDiscovery {
		return nil, nil
	}
	return []string{"a", "z"}, nil
}

func (p intentFixtureProvider) Stream(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
	if p.purpose == providers.PurposeAuxiliary {
		p.auxiliary.Add(1)
		for _, message := range request.Messages {
			if p.forbidden != "" && strings.Contains(message.Content, p.forbidden) && p.leaked != nil {
				p.leaked.Store(true)
			}
		}
		body := `{"version":1,"domain":"code","capabilities":["chat"]}`
		if p.invalid {
			body = `{"version":1,"domain":"code","capabilities":[]}` + " trailing"
		}
		return emit(providers.Chunk{Text: body, Done: true, FinishReason: "stop", Usage: &providers.Usage{InputTokens: 7, OutputTokens: 5}})
	}
	return emit(providers.Chunk{Text: "routed answer", Done: true, FinishReason: "stop"})
}

func intentClassifierService(t *testing.T, invalid bool) (*Service, config.Settings, *atomic.Int32) {
	t.Helper()
	fixture, cfg := autoFixture(t)
	cfg.Routing.Classifier = config.RoutingClassifier{Enabled: true, ModelID: "a", MaxInputTokens: 4096, MaxOutputTokens: 64, Timeout: "1s"}
	var calls atomic.Int32
	factory := applicationProviderFactory(func(_ context.Context, connection providers.Connection) (providers.Provider, error) {
		return intentFixtureProvider{purpose: connection.Purpose, auxiliary: &calls, invalid: invalid}, nil
	})
	svc, err := NewServiceWithProviderFactory(cfg, nil, nil, nil, nil, factory)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = fixture.profile
	return svc, cfg, &calls
}

func TestAuxiliaryIntentClassifierRunsOnceBeforeRoutingAndAccountsUse(t *testing.T) {
	ctx := context.Background()
	svc, cfg, calls := intentClassifierService(t, false)
	result, err := svc.Run(ctx, Request{Prompt: "please handle this ambiguous request"})
	if err != nil || result.Text != "routed answer" || calls.Load() != 1 {
		t.Fatal(result, err, calls.Load())
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.Read(ctx, result.TaskID, 0, 100)
	if err != nil || len(events) < 2 || events[0].Kind != runtime.TaskStarted || events[1].Kind != runtime.RouteSelected {
		t.Fatal(events, err)
	}
	use := events[0].Data.IntentClassification
	if use == nil || use.Status != classification.AttemptCompleted || use.DecisionDigest == "" || events[0].Data.Domain != "code" || len(events[0].Data.Capabilities) != 1 || events[0].Data.Capabilities[0] != "chat" {
		t.Fatal("classification was not frozen at task start", events[0])
	}
	attempt, err := db.IntentClassificationAttempt(ctx, use.AttemptID)
	if err != nil || attempt.TaskID != result.TaskID || attempt.Decision == nil || attempt.Decision.Domain != "code" {
		t.Fatal(attempt, err)
	}
	body, _ := json.Marshal(attempt)
	if string(body) == "" || len(body) > 1<<20 {
		t.Fatal("invalid bounded attempt")
	}
	totals, err := db.UsageTotals(ctx, accounting.Scope{TaskID: result.TaskID})
	if err != nil || totals.Classifier.Records != 1 || totals.Classifier.KnownInputTokens != 7 || totals.Classifier.KnownOutputTokens != 5 {
		t.Fatal(totals, err)
	}
	if _, err := svc.Run(ctx, Request{Prompt: "explicit request", Domain: "code"}); err != nil || calls.Load() != 1 {
		t.Fatal("explicit intent invoked classifier", err, calls.Load())
	}
}

func TestAuxiliaryIntentClassifierFailureStopsRouting(t *testing.T) {
	ctx := context.Background()
	svc, cfg, calls := intentClassifierService(t, true)
	result, err := svc.Run(ctx, Request{Prompt: "ambiguous failure"})
	if result.TaskID == "" || !errors.Is(err, ErrClassification) || calls.Load() != 1 {
		t.Fatal(result, err, calls.Load())
	}
	db, openErr := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer db.Close()
	events, readErr := db.Read(ctx, result.TaskID, 0, 10)
	if readErr != nil || len(events) != 3 || events[0].Kind != runtime.TaskStarted || events[1].Kind != runtime.ErrorRecorded || events[2].Kind != runtime.TaskFailed || events[0].Data.IntentClassification == nil {
		t.Fatal(events, readErr)
	}
	// The redacted terminal attempt is visible and accounted without dispatching
	// a routed model turn.
	attempt, readErr := db.IntentClassificationAttempt(ctx, events[0].Data.IntentClassification.AttemptID)
	if readErr != nil || attempt.Status != classification.AttemptFailed || attempt.Code != classification.CodeInvalidResponse {
		t.Fatal(attempt, readErr)
	}
	totals, totalsErr := db.UsageTotals(ctx, accounting.Scope{TaskID: result.TaskID})
	if totalsErr != nil || totals.Classifier.Records != 1 || totals.Classifier.KnownInputTokens != 7 || totals.Classifier.KnownOutputTokens != 5 || totals.Routed.Records != 0 {
		t.Fatal(totals, totalsErr)
	}
}

func TestUnroutedClassifierJournalIsAcknowledgementSafe(t *testing.T) {
	ctx := context.Background()
	svc, cfg, calls := intentClassifierService(t, true)
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	request, err := classifyRequestIntent(Request{Prompt: "durable synthetic journal", MaxCost: 1})
	if err != nil {
		t.Fatal(err)
	}
	request.intentClassification = &intentClassificationState{}
	prepared, classifyErr := svc.prepareAuxiliaryIntent(ctx, db, request)
	if !errors.Is(classifyErr, ErrClassification) || calls.Load() != 1 {
		t.Fatal(classifyErr, calls.Load())
	}
	first, firstErr := persistUnroutedClassification(ctx, db, prepared, Result{}, classifyErr)
	if first.TaskID == "" || !errors.Is(firstErr, ErrClassification) {
		t.Fatal(first, firstErr)
	}
	events, err := db.Read(ctx, first.TaskID, 0, 10)
	if err != nil || len(events) != 3 {
		t.Fatal(events, err)
	}
	second, secondErr := persistUnroutedClassification(ctx, db, prepared, Result{}, classifyErr)
	if second.TaskID != first.TaskID || !errors.Is(secondErr, ErrClassification) {
		t.Fatal(second, secondErr)
	}
	replayed, err := db.Read(ctx, second.TaskID, 0, 10)
	if err != nil || len(replayed) != len(events) {
		t.Fatal(replayed, err)
	}
	for index := range events {
		if replayed[index].ID != events[index].ID || !replayed[index].Time.Equal(events[index].Time) {
			t.Fatal("synthetic journal changed across acknowledgement retry", events, replayed)
		}
	}
}

func TestAuxiliaryIntentClassifierReusesDurableSubmissionDecision(t *testing.T) {
	ctx := context.Background()
	svc, cfg, calls := intentClassifierService(t, false)
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	request, err := classifyRequestIntent(Request{Prompt: "restart-safe ambiguity", MaxCost: 1, submissionID: "classifier-restart-submission"})
	if err != nil {
		t.Fatal(err)
	}
	request.intentClassification = &intentClassificationState{}
	first, err := svc.prepareAuxiliaryIntent(ctx, db, request)
	if err != nil || calls.Load() != 1 || first.taskID == "" || first.intentClassificationUse == nil {
		t.Fatal(first, err, calls.Load())
	}
	restarted := request
	restarted.intentClassification = &intentClassificationState{}
	second, err := svc.prepareAuxiliaryIntent(ctx, db, restarted)
	if err != nil || calls.Load() != 1 || second.taskID != first.taskID || second.Domain != first.Domain || second.intentClassificationUse == nil || second.intentClassificationUse.AttemptID != first.intentClassificationUse.AttemptID {
		t.Fatal("durable decision was redispatched or changed", second, err, calls.Load())
	}
}

func TestAuxiliaryIntentClassifierDoesNotRedispatchUncertainStartedSubmission(t *testing.T) {
	ctx := context.Background()
	svc, cfg, calls := intentClassifierService(t, false)
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	configID, err := settingsConfigID(svc.settings)
	if err != nil {
		t.Fatal(err)
	}
	request, err := classifyRequestIntent(Request{Prompt: "do not redispatch", MaxCost: 1, submissionID: "uncertain-classifier-submission"})
	if err != nil {
		t.Fatal(err)
	}
	_, requestDigest, ok := classifierInput(request, nil)
	if !ok {
		t.Fatal("could not prepare request digest")
	}
	attempt := classification.Attempt{
		Version: 1, ID: "uncertain-classifier-attempt", TaskID: "uncertain-classifier-task", SessionID: "uncertain-classifier-task",
		SubmissionID: request.submissionID, RequestDigest: requestDigest, ConfigID: configID,
		Model: "a", Provider: "local", Status: classification.AttemptStarted, StartedAt: time.Now().UTC(),
	}
	if err := db.BeginIntentClassification(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	request.intentClassification = &intentClassificationState{}
	settled, err := svc.prepareAuxiliaryIntent(ctx, db, request)
	if !errors.Is(err, ErrClassification) || calls.Load() != 0 || settled.intentClassificationUse != nil {
		t.Fatal("uncertain started attempt was redispatched", err, calls.Load())
	}
	stored, readErr := db.IntentClassificationAttempt(ctx, attempt.ID)
	if readErr != nil || stored.Status != classification.AttemptStarted {
		t.Fatal(stored, readErr)
	}
}

func TestAuxiliaryIntentClassifierSettlesOnlyExpiredStartedSubmission(t *testing.T) {
	ctx := context.Background()
	svc, cfg, calls := intentClassifierService(t, false)
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	request, err := classifyRequestIntent(Request{Prompt: "recover expired classifier", MaxCost: 1, submissionID: "expired-classifier-submission"})
	if err != nil {
		t.Fatal(err)
	}
	_, requestDigest, ok := classifierInput(request, nil)
	configID, configErr := settingsConfigID(svc.settings)
	if !ok || configErr != nil {
		t.Fatal(configErr)
	}
	attempt := classification.Attempt{
		Version: 1, ID: "expired-classifier-attempt", TaskID: "expired-classifier-task", SessionID: "expired-classifier-task",
		SubmissionID: request.submissionID, RequestDigest: requestDigest, ConfigID: configID,
		Model: "a", Provider: "local", Status: classification.AttemptStarted, StartedAt: time.Now().Add(-time.Minute).UTC(),
	}
	if err := db.BeginIntentClassification(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	request.intentClassification = &intentClassificationState{}
	settled, err := svc.prepareAuxiliaryIntent(ctx, db, request)
	if !errors.Is(err, ErrClassification) || calls.Load() != 0 || settled.intentClassificationUse == nil || settled.intentClassificationUse.Code != classification.CodePersistenceFailed {
		t.Fatal(settled, err, calls.Load())
	}
	stored, readErr := db.IntentClassificationAttempt(ctx, attempt.ID)
	if readErr != nil || stored.Status != classification.AttemptFailed || stored.Code != classification.CodePersistenceFailed {
		t.Fatal(stored, readErr)
	}
}

func TestAuxiliaryIntentClassifierRejectsMismatchedSubmissionReplay(t *testing.T) {
	ctx := context.Background()
	svc, cfg, calls := intentClassifierService(t, false)
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	first, err := classifyRequestIntent(Request{Prompt: "first request", MaxCost: 1, submissionID: "classifier-bound-submission"})
	if err != nil {
		t.Fatal(err)
	}
	first.intentClassification = &intentClassificationState{}
	if _, err = svc.prepareAuxiliaryIntent(ctx, db, first); err != nil || calls.Load() != 1 {
		t.Fatal(err, calls.Load())
	}
	changed, err := classifyRequestIntent(Request{Prompt: "changed request", MaxCost: 1, submissionID: first.submissionID})
	if err != nil {
		t.Fatal(err)
	}
	changed.intentClassification = &intentClassificationState{}
	if _, err = svc.prepareAuxiliaryIntent(ctx, db, changed); !errors.Is(err, ErrClassification) || calls.Load() != 1 {
		t.Fatal("mismatched durable attempt was reused or redispatched", err, calls.Load())
	}
}

func TestAuxiliaryIntentClassifierChargesOnceAcrossRecursiveAndFallbackCopies(t *testing.T) {
	ctx := context.Background()
	svc, cfg, calls := intentClassifierService(t, false)
	cost := 0.25
	svc.settings.Models[0].EstimatedCost = &cost
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	original, err := classifyRequestIntent(Request{Prompt: "charge once", MaxCost: 1})
	if err != nil {
		t.Fatal(err)
	}
	original.intentClassification = &intentClassificationState{}
	first, err := svc.prepareAuxiliaryIntent(ctx, db, original)
	if err != nil || first.MaxCost != 0.75 || first.intentClassificationUse == nil || calls.Load() != 1 {
		t.Fatal(first.MaxCost, err, calls.Load())
	}
	recursive, err := svc.prepareAuxiliaryIntent(ctx, db, first)
	if err != nil || recursive.MaxCost != first.MaxCost || recursive.intentClassificationUse == nil {
		t.Fatal("recursive bind charged twice", recursive.MaxCost, err)
	}
	fallback := original
	fallback.retryOfTaskID = first.taskID
	fallback, err = svc.prepareAuxiliaryIntent(ctx, db, fallback)
	if err != nil || fallback.MaxCost != original.MaxCost || fallback.intentClassificationUse != nil || fallback.taskID != "" || fallback.Domain != "code" {
		t.Fatal("fallback bind corrupted attribution or budget", fallback, err)
	}
}

func TestAuxiliaryIntentClassifierUsesOneCredentialSnapshot(t *testing.T) {
	ctx := context.Background()
	svc, cfg, calls := intentClassifierService(t, false)
	const envName, secretValue = "DARWIN_CLASSIFIER_TEST_KEY", "classifier-rotating-secret"
	svc.settings.Providers[0].APIKeyEnv = envName
	var resolutions atomic.Int32
	svc.secret = func(name string) string {
		if name == envName {
			resolutions.Add(1)
			return secretValue
		}
		return ""
	}
	var leaked atomic.Bool
	svc.providerFactory = applicationProviderFactory(func(_ context.Context, connection providers.Connection) (providers.Provider, error) {
		if connection.Purpose == providers.PurposeAuxiliary && connection.APIKey != secretValue {
			t.Fatal("classifier did not use frozen credential")
		}
		return intentFixtureProvider{purpose: connection.Purpose, auxiliary: calls, forbidden: secretValue, leaked: &leaked}, nil
	})
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	request, err := classifyRequestIntent(Request{Prompt: "do not expose " + secretValue, MaxCost: 1})
	if err != nil {
		t.Fatal(err)
	}
	request.intentClassification = &intentClassificationState{}
	if _, err = svc.prepareAuxiliaryIntent(ctx, db, request); err != nil || resolutions.Load() != 1 || leaked.Load() {
		t.Fatal("credential snapshot or redaction failed", err, resolutions.Load(), leaked.Load())
	}
}

func TestAuxiliaryIntentClassifierRunsForLocalToolWorkload(t *testing.T) {
	svc, _, calls := intentClassifierService(t, false)
	svc.settings.Tools.Enabled = true
	svc.settings.Tools.ReadRoot = t.TempDir()
	result, err := svc.Run(context.Background(), Request{Prompt: "ambiguous local tool task"})
	if err != nil || result.TaskID == "" || calls.Load() != 1 {
		t.Fatal(result, err, calls.Load())
	}
}

func TestCompletedClassifierGetsMinimalJournalWhenRoutingFails(t *testing.T) {
	ctx := context.Background()
	svc, cfg, calls := intentClassifierService(t, false)
	svc.providerFactory = applicationProviderFactory(func(_ context.Context, connection providers.Connection) (providers.Provider, error) {
		return intentFixtureProvider{purpose: connection.Purpose, auxiliary: calls, unhealthy: true}, nil
	})
	result, err := svc.Run(ctx, Request{Prompt: "classify before no route"})
	if result.TaskID == "" || err == nil || calls.Load() != 1 {
		t.Fatal(result, err, calls.Load())
	}
	db, openErr := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer db.Close()
	events, readErr := db.Read(ctx, result.TaskID, 0, 10)
	if readErr != nil || len(events) != 3 || events[0].Data.IntentClassification == nil || events[0].Data.IntentClassification.Status != classification.AttemptCompleted || events[1].Kind != runtime.ErrorRecorded || events[1].Data.Code != "routing_admission_failed" || events[2].Kind != runtime.TaskFailed {
		t.Fatal(events, readErr)
	}
}
