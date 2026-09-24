package app

import (
	"context"
	"crypto/rand"
	"errors"
	"math"
	"reflect"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// SummarizeTask creates a durable proposal, never an active context change or
// quality signal. Explicit invocation authorizes one bounded auxiliary call;
// generated text still needs accuracy validation before automatic application.
func (s *Service) SummarizeTask(ctx context.Context, task, modelID string, keep int, maxCost float64) (sessions.SummaryAttempt, error) {
	bad := func() (sessions.SummaryAttempt, error) { return sessions.SummaryAttempt{}, ErrAdmission }
	if s == nil || ctx == nil || ctx.Err() != nil || task == "" || len(task) > 128 || keep < 1 || keep > 100000 || maxCost < 0 || math.IsNaN(maxCost) || math.IsInf(maxCost, 0) {
		return bad()
	}
	read, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return bad()
	}
	defer read.Close()
	history, err := sessions.Replay(ctx, read, task)
	if err != nil {
		return bad()
	}
	var model config.Model
	for _, candidate := range s.settings.Models {
		if candidate.ID == modelID {
			model = candidate
			break
		}
	}
	if model.ID == "" || model.ContextTokens < 1 || model.EstimatedCost == nil || *model.EstimatedCost > maxCost {
		return bad()
	}
	local := model.Locality == "local"
	if (s.settings.Mode == "local_only" && !local) || (s.settings.Mode == "cloud_only" && local) || (history.Privacy != "cloud_allowed" && !local) {
		return bad()
	}
	var provider config.Provider
	for _, candidate := range s.settings.Providers {
		if candidate.ID == model.Provider {
			provider = candidate
			break
		}
	}
	secrets := memorySecrets(s.settings, s.secret)
	key := ""
	if s.secret != nil && provider.APIKeyEnv != "" {
		key = s.secret(provider.APIKeyEnv)
	}
	if provider.APIKeyEnv != "" && key == "" {
		return bad()
	}
	// Summary and usage-accounting records copy these identities verbatim.
	// Existing source correlation does not authorize creating a new durable row
	// when one of those values is now a configured secret.
	if !selectionValueClean([]string{task, history.SessionID, modelID, model.ID, model.Model, model.Provider, provider.ID}, secrets) {
		return bad()
	}
	selection, err := selectContextCompaction(ctx, s.contextEngine, history, sessions.CompactionRequest{Keep: keep, Summary: sessions.Summary{Decisions: []string{"pending draft"}}}, secrets)
	if err != nil {
		return bad()
	}
	keep = selection.Keep
	_, provenance, err := sessions.PrepareContinuation(history, selection)
	if err != nil {
		return bad()
	}
	input := history
	if provider.Kind == "codex_app_server" {
		input.Messages, err = nativeSummaryMessages(history.Messages, secrets)
		if !selectionValueClean([]string{task, history.SessionID, model.ID, model.Model, provider.ID, provider.Executable}, secrets) {
			return bad()
		}
	} else {
		input.Messages, err = redactSummaryMessages(history.Messages, secrets)
	}
	if err != nil {
		return bad()
	}
	attempt := sessions.SummaryAttempt{Version: 1, ID: rand.Text(), TaskID: task, SourceSequence: history.Sequence, SourceDigest: provenance.SourceDigest, Model: model.Model, Provider: model.Provider, Status: "started", Keep: keep, EstimatedCost: *model.EstimatedCost, StartedAt: time.Now().UTC()}
	// ID is reused as the auxiliary operation, route, and evidence identity.
	// Check the entire durable object before provider construction or dispatch.
	if !selectionValueClean(attempt, secrets) {
		return bad()
	}
	release := func() error { return nil }
	if local {
		if model.RAMBytes == 0 {
			return bad()
		}
		var profileErr error
		ctx, release, profileErr = s.reserveAuxiliaryExecution(ctx, model)
		if profileErr != nil {
			return bad()
		}
	}
	cleanup := auxiliaryCleanup(nil, release)
	defer func() { _ = cleanup() }()
	write, err := telemetry.Open(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return bad()
	}
	defer write.Close()
	if err := write.BeginSummary(ctx, attempt); err != nil {
		return bad()
	}
	fail := func(code string, cause error) (sessions.SummaryAttempt, error) {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if code == "canceled" {
			attempt.Usage, attempt.Elapsed = nil, 0
		}
		attempt.Status, attempt.Code, attempt.Draft = "failed", code, nil
		attempt.FinishedAt = time.Now().UTC()
		if err := write.FinishSummary(cleanup, attempt); err != nil {
			// Do not report a durable terminal state when its write is uncertain.
			attempt.Status, attempt.Code, attempt.Usage, attempt.Elapsed, attempt.FinishedAt = "started", "", nil, 0, time.Time{}
			return attempt, errors.Join(cause, errors.New("cannot persist summary terminal state"))
		}
		return attempt, cause
	}
	// Provider construction may launch a subprocess. The guarded started row
	// must therefore be durable before construction, not merely before Stream.
	adapter, closeProvider, err := s.openAuxiliaryProvider(ctx, provider, model, history.Privacy, key)
	if err != nil {
		return fail("summary_failed", errors.New("summary provider unavailable"))
	}
	cleanup = auxiliaryCleanup(closeProvider, release)
	if native, ok := adapter.(*codexAuxiliaryProvider); ok {
		// A changed credential must not silently alter the request after context
		// estimation. Recheck the original source before launch and after startup.
		native.beforeStream = func() error {
			secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
			clean, err := nativeSummaryMessages(history.Messages, secrets)
			if err != nil || !reflect.DeepEqual(clean, input.Messages) || !selectionValueClean([]string{task, history.SessionID, model.ID, model.Model, provider.ID, provider.Executable}, secrets) {
				return ErrAdmission
			}
			return nil
		}
	}
	summarizer := sessions.Summarizer{ContextEstimator: s.contextEstimator, Provider: adapter, Model: model.Model, ContextTokens: model.WorkingContextTokens(), Timeout: time.Minute, EstimatedCost: *model.EstimatedCost, MaxCost: maxCost, StructuredOutput: provider.Kind == "codex_app_server"}
	draft, err := summarizer.Draft(ctx, input, keep)
	if cleanupErr := cleanup(); cleanupErr != nil {
		err = errors.Join(err, cleanupErr)
	}
	if err != nil {
		code := "summary_failed"
		if ctx.Err() != nil {
			code = "canceled"
		} else if draft.Usage != nil {
			usage := *draft.Usage
			attempt.Usage, attempt.Elapsed = &usage, draft.Elapsed
		}
		return fail(code, errors.New("summary draft failed"))
	}
	if draft.Usage != nil {
		usage := *draft.Usage
		attempt.Usage, attempt.Elapsed = &usage, draft.Elapsed
	}
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if !selectionValueClean([]string{task, history.SessionID, model.ID, model.Model, model.Provider, provider.ID, provider.Executable, attempt.ID, attempt.SourceDigest}, secrets) {
		// Keep the immutable started identity for lifecycle reconciliation, but
		// do not publish a draft with newly classified credential metadata.
		return fail("summary_failed", errors.New("summary metadata unavailable"))
	}
	draft.Request.Summary = redactSummary(draft.Request.Summary, secrets)
	_, checkpoint, err := sessions.PrepareContinuation(history, draft.Request)
	if err != nil {
		return fail("summary_failed", errors.New("invalid summary draft"))
	}
	// Provenance names the unchanged durable source, not the redacted auxiliary
	// input. Rebuild context estimates against that source and sanitized output.
	draft.Checkpoint = checkpoint
	draft.SourceTaskID, draft.SourceSequence, draft.SourceDigest = task, history.Sequence, checkpoint.SourceDigest
	attempt.Status, attempt.Draft, attempt.FinishedAt = "drafted", &draft, time.Now().UTC()
	attempt.Usage, attempt.Elapsed = nil, 0
	if !selectionValueClean(attempt, secrets) {
		if draft.Usage != nil {
			usage := *draft.Usage
			attempt.Usage, attempt.Elapsed = &usage, draft.Elapsed
		}
		return fail("summary_failed", errors.New("summary metadata unavailable"))
	}
	if err := write.CompleteSummary(ctx, attempt); err != nil {
		// A commit acknowledgement can be lost after SQLite made the drafted row
		// durable. Re-read once without inheriting caller cancellation and accept
		// only the exact proposal; never invoke the provider again.
		inspect, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		committed, readErr := write.SummaryAttempt(inspect, attempt.ID)
		cancel()
		if readErr == nil && reflect.DeepEqual(committed, attempt) {
			return committed, nil
		}
		if draft.Usage != nil {
			usage := *draft.Usage
			attempt.Usage, attempt.Elapsed = &usage, draft.Elapsed
		}
		return fail("persistence_failed", errors.New("cannot persist summary draft"))
	}
	return attempt, nil
}
