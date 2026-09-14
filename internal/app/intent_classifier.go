package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/classification"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

var ErrClassification = errors.New("intent classification failed")

type intentClassificationState struct {
	mu      sync.Mutex
	loaded  bool
	skipped bool
	bound   bool
	attempt classification.Attempt
}

func (s *Service) prepareAuxiliaryIntent(ctx context.Context, db *telemetry.Store, r Request) (Request, error) {
	state := r.intentClassification
	if state == nil {
		return r, nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.loaded {
		if state.attempt.Status == classification.AttemptCompleted {
			return bindClassificationState(r, state)
		}
		return bindFailedClassification(r, state), errors.Join(ErrAdmission, ErrClassification)
	}
	model, provider, ok := classifierTarget(s.settings)
	if !ok || provider.Kind == "codex_app_server" || model.EstimatedCost == nil {
		state.loaded, state.skipped = true, true
		return r, nil
	}
	configID, err := settingsConfigID(s.settings)
	if err != nil {
		return Request{}, errors.Join(ErrAdmission, ErrClassification)
	}
	secretValues, secrets := classifierSecretSnapshot(s.settings, s.secret)
	input, digest, ok := classifierInput(r, secrets)
	if !ok {
		state.loaded, state.skipped = true, true
		return r, nil
	}
	expectedSession := ""
	if r.continuation != nil {
		expectedSession = r.continuation.SessionID
	}
	if r.submissionID != "" {
		existing, readErr := db.IntentClassificationAttemptForSubmission(ctx, r.submissionID)
		if readErr == nil {
			if !classificationAttemptMatches(existing, digest, configID, model, provider, expectedSession) {
				return Request{}, errors.Join(ErrAdmission, ErrClassification)
			}
			existing, ready, recoveryErr := s.recoverExpiredIntentClassification(ctx, db, existing)
			if recoveryErr != nil || !ready {
				return Request{}, errors.Join(ErrAdmission, ErrClassification, recoveryErr)
			}
			state.loaded, state.attempt = true, existing
			if existing.Status == classification.AttemptCompleted {
				return bindClassificationState(r, state)
			}
			return bindFailedClassification(r, state), errors.Join(ErrAdmission, ErrClassification)
		}
		if !errors.Is(readErr, sql.ErrNoRows) {
			return Request{}, errors.Join(ErrAdmission, ErrClassification)
		}
	}
	if (r.LocalRequired && model.Locality != "local") || r.MaxCost < *model.EstimatedCost {
		state.loaded, state.skipped = true, true
		return r, nil
	}
	key := secretValues[provider.APIKeyEnv]
	if provider.APIKeyEnv != "" && key == "" {
		state.loaded, state.skipped = true, true
		return r, nil
	}
	release := func() {}
	if model.Locality == "local" {
		var err error
		release, err = s.reserveExplicit(ctx, model)
		if err != nil {
			state.loaded, state.skipped = true, true
			return r, nil
		}
	}
	defer release()
	taskID := rand.Text()
	sessionID := taskID
	if r.continuation != nil {
		sessionID = r.continuation.SessionID
	}
	started := time.Now().UTC()
	attempt := classification.Attempt{
		Version: 1, ID: rand.Text(), TaskID: taskID, SessionID: sessionID,
		SubmissionID: r.submissionID, RequestDigest: digest, ConfigID: configID,
		Model: model.Model, Provider: provider.ID, EstimatedCost: *model.EstimatedCost,
		Status: classification.AttemptStarted, StartedAt: started,
	}
	if err := db.BeginIntentClassification(ctx, attempt); err != nil {
		if r.submissionID != "" && errors.Is(err, telemetry.ErrConflict) {
			existing, readErr := db.IntentClassificationAttemptForSubmission(ctx, r.submissionID)
			if readErr == nil && classificationAttemptMatches(existing, digest, configID, model, provider, expectedSession) {
				existing, ready, recoveryErr := s.recoverExpiredIntentClassification(ctx, db, existing)
				if recoveryErr != nil || !ready {
					return Request{}, errors.Join(ErrAdmission, ErrClassification, recoveryErr)
				}
				state.loaded, state.attempt = true, existing
				if existing.Status == classification.AttemptCompleted {
					return bindClassificationState(r, state)
				}
				return bindFailedClassification(r, state), errors.Join(ErrAdmission, ErrClassification)
			}
		}
		return Request{}, errors.Join(ErrAdmission, ErrClassification)
	}
	privacy := "cloud_allowed"
	if model.Locality == "local" {
		privacy = "local_only"
	}
	adapter, closeProvider, openErr := s.openAuxiliaryProvider(ctx, provider, model, privacy, key)
	if openErr != nil {
		failure := s.finishClassifierFailure(ctx, db, state, attempt, classification.CodeProviderFailed, nil, 0)
		return bindFailedClassification(r, state), failure
	}
	defer closeProvider()
	timeout, _ := config.Duration(s.settings.Routing.Classifier.Timeout)
	classifier, buildErr := classification.NewModelClassifier(adapter, classification.ModelConfig{
		Model: model.Model, Timeout: timeout, MaxInputTokens: s.settings.Routing.Classifier.MaxInputTokens,
		MaxOutputTokens: s.settings.Routing.Classifier.MaxOutputTokens, MaxInputBytes: classification.MaxClassifierInputBytes,
		ContextEstimator: s.contextEstimator, AllowedDomains: []string{"code", "creative", "general", "math", "structured_json"},
		AllowedCapabilities: classifierCapabilities(s.settings.Models),
	})
	if buildErr != nil {
		failure := s.finishClassifierFailure(ctx, db, state, attempt, classification.CodeProviderFailed, nil, 0)
		return bindFailedClassification(r, state), failure
	}
	result, classifyErr := classifier.Classify(ctx, input)
	finished := time.Now().UTC()
	if classifyErr != nil {
		code := classification.CodeProviderFailed
		if errors.Is(classifyErr, classification.ErrInvalidResponse) || errors.Is(classifyErr, classification.ErrInvalidInput) {
			code = classification.CodeInvalidResponse
		}
		if errors.Is(classifyErr, context.DeadlineExceeded) {
			code = classification.CodeTimeout
		}
		if errors.Is(classifyErr, context.Canceled) && ctx.Err() != nil {
			attempt.Status, attempt.Code, attempt.FinishedAt = classification.AttemptCanceled, classification.CodeCanceled, finished
		} else {
			attempt.Status, attempt.Code, attempt.FinishedAt = classification.AttemptFailed, code, finished
			attempt.Usage, attempt.Elapsed = result.Usage, boundedClassifierElapsed(result.Elapsed, finished.Sub(started))
		}
		if err := finishIntentClassificationBounded(ctx, db, attempt); err != nil {
			return Request{}, errors.Join(ErrAdmission, ErrClassification)
		}
		state.loaded, state.attempt = true, attempt
		return bindFailedClassification(r, state), errors.Join(ErrAdmission, ErrClassification, classifyErr)
	}
	attempt.Status, attempt.FinishedAt = classification.AttemptCompleted, finished
	attempt.Usage, attempt.Elapsed, attempt.Decision = result.Usage, boundedClassifierElapsed(result.Elapsed, finished.Sub(started)), &result.Decision
	if err := finishIntentClassificationBounded(ctx, db, attempt); err != nil {
		return Request{}, errors.Join(ErrAdmission, ErrClassification)
	}
	state.loaded, state.attempt = true, attempt
	return bindClassificationState(r, state)
}

func (s *Service) finishClassifierFailure(ctx context.Context, db *telemetry.Store, state *intentClassificationState, attempt classification.Attempt, code string, usage *providers.Usage, elapsed time.Duration) error {
	attempt.Status, attempt.Code, attempt.FinishedAt = classification.AttemptFailed, code, time.Now().UTC()
	attempt.Usage, attempt.Elapsed = usage, boundedClassifierElapsed(elapsed, attempt.FinishedAt.Sub(attempt.StartedAt))
	if err := finishIntentClassificationBounded(ctx, db, attempt); err != nil {
		return errors.Join(ErrAdmission, ErrClassification)
	}
	state.loaded, state.attempt = true, attempt
	return errors.Join(ErrAdmission, ErrClassification)
}

func bindClassificationState(r Request, state *intentClassificationState) (Request, error) {
	if state.skipped {
		return r, nil
	}
	if state.attempt.Status != classification.AttemptCompleted || state.attempt.Decision == nil {
		return Request{}, errors.Join(ErrAdmission, ErrClassification)
	}
	if r.intentClassificationUse == nil {
		if state.bound {
			if r.retryOfTaskID != "" {
				r.Domain = state.attempt.Decision.Domain
				r.Capabilities = mergedClassifierCapabilities(r.Capabilities, state.attempt.Decision.Capabilities)
				return r, nil
			}
			return Request{}, errors.Join(ErrAdmission, ErrClassification)
		}
		r.taskID, r.sessionID = state.attempt.TaskID, state.attempt.SessionID
		body, err := json.Marshal(state.attempt.Decision)
		if err != nil {
			return Request{}, errors.Join(ErrAdmission, ErrClassification)
		}
		digest := sha256.Sum256(body)
		r.intentClassificationUse = &runtime.IntentClassificationUse{Version: 1, AttemptID: state.attempt.ID, Status: state.attempt.Status, DecisionDigest: hex.EncodeToString(digest[:])}
		r.intentClassificationCharged = true
		state.bound = true
	}
	r.Domain = state.attempt.Decision.Domain
	r.Capabilities = mergedClassifierCapabilities(r.Capabilities, state.attempt.Decision.Capabilities)
	if r.intentClassificationCharged {
		if r.MaxCost < state.attempt.EstimatedCost {
			return Request{}, errors.Join(ErrAdmission, ErrClassification)
		}
		r.MaxCost -= state.attempt.EstimatedCost
		r.intentClassificationCharged = false
	}
	return r, nil
}

func bindFailedClassification(r Request, state *intentClassificationState) Request {
	r.taskID, r.sessionID = state.attempt.TaskID, state.attempt.SessionID
	r.intentClassificationUse = &runtime.IntentClassificationUse{Version: 1, AttemptID: state.attempt.ID, Status: state.attempt.Status, Code: state.attempt.Code}
	return r
}

func finishIntentClassificationBounded(ctx context.Context, db *telemetry.Store, attempt classification.Attempt) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return db.FinishIntentClassification(cleanup, attempt)
}

// recoverExpiredIntentClassification refuses to interfere with a classifier
// that may still be inside its bounded call or terminal-write window. Only a
// durable attempt older than both limits is converged to a terminal orphan
// outcome; the store CAS arbitrates against a late original completion.
func (s *Service) recoverExpiredIntentClassification(ctx context.Context, db *telemetry.Store, attempt classification.Attempt) (classification.Attempt, bool, error) {
	if attempt.Status != classification.AttemptStarted {
		return attempt, true, nil
	}
	timeout, err := config.Duration(s.settings.Routing.Classifier.Timeout)
	if err != nil || time.Now().UTC().Before(attempt.StartedAt.Add(timeout).Add(5*time.Second)) {
		return attempt, false, err
	}
	attempt.Status, attempt.Code, attempt.FinishedAt = classification.AttemptFailed, classification.CodePersistenceFailed, time.Now().UTC()
	attempt.Elapsed = boundedClassifierElapsed(0, attempt.FinishedAt.Sub(attempt.StartedAt))
	if err := finishIntentClassificationBounded(ctx, db, attempt); err != nil {
		return classification.Attempt{}, false, err
	}
	return attempt, true, nil
}

func classificationAttemptMatches(attempt classification.Attempt, requestDigest, configID string, model config.Model, provider config.Provider, sessionID string) bool {
	return attempt.RequestDigest == requestDigest && attempt.ConfigID == configID && attempt.Model == model.Model &&
		attempt.Provider == provider.ID && model.EstimatedCost != nil && attempt.EstimatedCost == *model.EstimatedCost &&
		(sessionID != "" && attempt.SessionID == sessionID || sessionID == "" && attempt.SessionID == attempt.TaskID)
}

func mergedClassifierCapabilities(base, classified []string) []string {
	values := append(append([]string(nil), base...), classified...)
	slices.Sort(values)
	return slices.Compact(values)
}

func classifierTarget(settings config.Settings) (config.Model, config.Provider, bool) {
	var model config.Model
	for _, candidate := range settings.Models {
		if candidate.ID == settings.Routing.Classifier.ModelID {
			model = candidate
			break
		}
	}
	for _, provider := range settings.Providers {
		if model.ID != "" && provider.ID == model.Provider {
			return model, provider, true
		}
	}
	return config.Model{}, config.Provider{}, false
}

func classifierCapabilities(models []config.Model) []string {
	values := []string{"chat"}
	for _, model := range models {
		values = append(values, model.Capabilities...)
	}
	slices.Sort(values)
	values = slices.Compact(values)
	if len(values) > classification.MaxDecisionCapabilities {
		bounded := []string{"chat"}
		for _, value := range values {
			if value != "chat" && len(bounded) < classification.MaxDecisionCapabilities {
				bounded = append(bounded, value)
			}
		}
		slices.Sort(bounded)
		values = bounded
	}
	return values
}

func classifierInput(r Request, secrets []string) (classification.Input, string, bool) {
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	messages := make([]message, 0, len(r.Messages))
	for _, item := range r.Messages {
		if len(item.ToolCalls) != 0 || item.ToolCallID != "" {
			return classification.Input{}, "", false
		}
		messages = append(messages, message{Role: item.Role, Content: redact(item.Content, secrets)})
	}
	body, err := json.Marshal(struct {
		Prompt   string    `json:"prompt,omitempty"`
		Messages []message `json:"messages,omitempty"`
	}{Prompt: redact(r.Prompt, secrets), Messages: messages})
	if err != nil || len(body) == 0 || len(body) > classification.MaxClassifierInputBytes {
		return classification.Input{}, "", false
	}
	sum := sha256.Sum256(body)
	return classification.Input{Task: string(body)}, hex.EncodeToString(sum[:]), true
}

func classifierSecretSnapshot(settings config.Settings, resolve func(string) string) (map[string]string, []string) {
	values := map[string]string{}
	if resolve == nil {
		return values, nil
	}
	names := []string{"DARWIN_API_TOKEN"}
	for _, provider := range settings.Providers {
		if provider.APIKeyEnv != "" {
			names = append(names, provider.APIKeyEnv)
		}
	}
	names = append(names, settings.Security.RedactEnv...)
	if settings.Telemetry.MetricsExport != nil {
		names = append(names, settings.Telemetry.MetricsExport.APIKeyEnv)
	}
	if settings.Telemetry.TraceExport != nil {
		names = append(names, settings.Telemetry.TraceExport.APIKeyEnv)
	}
	slices.Sort(names)
	names = slices.Compact(names)
	secrets := make([]string, 0, len(names))
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			continue
		}
		value := resolve(name)
		values[name] = value
		if value != "" {
			secrets = append(secrets, value)
		}
	}
	return values, secrets
}

// persistUnroutedClassification closes the lifecycle gap between a terminal
// auxiliary call and normal runtime dispatch. It creates a minimal, redacted
// task journal only when no routed TaskStarted event was committed, so known
// classifier usage remains inspectable and exactly-once accounted.
func persistUnroutedClassification(ctx context.Context, db *telemetry.Store, r Request, result Result, cause error) (Result, error) {
	if r.intentClassificationUse == nil || r.taskID == "" || r.sessionID == "" {
		return result, cause
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	attempt, err := db.IntentClassificationAttempt(cleanup, r.intentClassificationUse.AttemptID)
	if err != nil {
		return result, errors.Join(cause, ErrClassification, err)
	}
	appendEvent := func(expected int64, event runtime.Event) error {
		if r.submissionID == "" {
			return db.Append(cleanup, expected, event)
		}
		if r.submissionToken == "" {
			return ErrAdmission
		}
		return db.AppendSubmission(cleanup, expected, event, r.submissionID, r.submissionToken)
	}
	privacy := "cloud_allowed"
	if r.continuation != nil {
		privacy = r.continuation.Privacy
	}
	if r.LocalRequired {
		privacy = "local_only"
	}
	start := runtime.Event{
		Version: 1, ID: classificationEventID(attempt.ID, "task-started"), TaskID: r.taskID, SessionID: r.sessionID,
		CorrelationID: r.taskID, Sequence: 1, Time: attempt.StartedAt, Kind: runtime.TaskStarted,
		Data: runtime.Data{
			IntentClassification: r.intentClassificationUse, SubmissionID: r.submissionID,
			ConfigID: attempt.ConfigID, Domain: r.Domain, Profile: r.Profile,
			Capabilities: append([]string(nil), r.Capabilities...), ParentTaskID: r.ContinueTaskID,
			Privacy: privacy,
		},
	}
	terminalKind, code := runtime.TaskFailed, "routing_admission_failed"
	if attempt.Status == classification.AttemptFailed {
		code = "intent_classification_failed"
	}
	if attempt.Status == classification.AttemptCanceled {
		terminalKind, code = runtime.TaskCanceled, "intent_classification_canceled"
	}
	errorEvent := runtime.Event{
		Version: 1, ID: classificationEventID(attempt.ID, "error-recorded"), TaskID: r.taskID, SessionID: r.sessionID,
		CorrelationID: r.taskID, Sequence: 2, Time: attempt.FinishedAt, Kind: runtime.ErrorRecorded,
		Data: runtime.Data{Code: code},
	}
	terminal := runtime.Event{
		Version: 1, ID: classificationEventID(attempt.ID, "task-terminal"), TaskID: r.taskID, SessionID: r.sessionID,
		CorrelationID: r.taskID, Sequence: 3, Time: attempt.FinishedAt, Kind: terminalKind,
		Data: runtime.Data{Code: code},
	}
	desired := []runtime.Event{start, errorEvent, terminal}
	existing, err := db.Read(cleanup, r.taskID, 0, len(desired))
	if err != nil {
		return result, errors.Join(cause, ErrClassification, err)
	}
	if len(existing) > 0 && existing[0].ID != start.ID {
		// A normal runtime journal already owns this task identity.
		return result, cause
	}
	for index, event := range existing {
		got, gotErr := event.Encode()
		want, wantErr := desired[index].Encode()
		if gotErr != nil || wantErr != nil || !slices.Equal(got, want) {
			return result, errors.Join(cause, ErrClassification, telemetry.ErrConflict)
		}
	}
	for index := len(existing); index < len(desired); index++ {
		if err := appendEvent(int64(index), desired[index]); err != nil {
			return result, errors.Join(cause, ErrClassification, err)
		}
	}
	result.TaskID = r.taskID
	return result, cause
}

func classificationEventID(attemptID, kind string) string {
	digest := sha256.Sum256([]byte(attemptID + "\x00" + kind))
	return "ic-" + hex.EncodeToString(digest[:])
}

func boundedClassifierElapsed(measured, wall time.Duration) time.Duration {
	if measured < 0 {
		return 0
	}
	if measured > wall {
		return wall
	}
	if measured > classification.MaxClassifierTimeout {
		return classification.MaxClassifierTimeout
	}
	return measured
}
