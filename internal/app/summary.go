package app

import (
	"context"
	"crypto/rand"
	"errors"
	"math"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/policy"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// SummarizeTask creates a durable proposal, never an active context change or
// quality signal. Explicit invocation authorizes one bounded auxiliary call;
// generated text still needs accuracy validation before automatic application.
func (s *Service) SummarizeTask(ctx context.Context, task, modelID string, keep int, maxCost float64) (sessions.SummaryAttempt, error) {
	bad := func() (sessions.SummaryAttempt, error) { return sessions.SummaryAttempt{}, ErrAdmission }
	if task == "" || len(task) > 128 || keep < 1 || keep > 100000 || maxCost < 0 || math.IsNaN(maxCost) || math.IsInf(maxCost, 0) {
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
	_, provenance, err := sessions.PrepareContinuation(history, sessions.CompactionRequest{Keep: keep, Summary: sessions.Summary{Decisions: []string{"pending draft"}}})
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
	input := history
	input.Messages, err = redactSummaryMessages(history.Messages, secrets)
	if err != nil {
		return bad()
	}
	if local {
		if model.RAMBytes == 0 {
			return bad()
		}
		release, profileErr := s.reserveExplicit(ctx, model)
		if profileErr != nil {
			return bad()
		}
		defer release()
	}
	transport, err := policy.NewTransport(s.settings.Mode == "local_only" || local, []string{provider.Endpoint})
	if err != nil {
		return bad()
	}
	defer transport.CloseIdleConnections()
	adapter, err := providers.NewHTTP(provider.Endpoint, provider.Kind, key, transport)
	if err != nil {
		return bad()
	}
	write, err := telemetry.Open(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return bad()
	}
	defer write.Close()
	attempt := sessions.SummaryAttempt{Version: 1, ID: rand.Text(), TaskID: task, SourceSequence: history.Sequence, SourceDigest: provenance.SourceDigest, Model: model.Model, Provider: model.Provider, Status: "started", Keep: keep, EstimatedCost: *model.EstimatedCost, StartedAt: time.Now().UTC()}
	if err := write.BeginSummary(ctx, attempt); err != nil {
		return bad()
	}
	fail := func(code string, cause error) (sessions.SummaryAttempt, error) {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		attempt.Status, attempt.Code, attempt.Draft = "failed", code, nil
		attempt.FinishedAt = time.Now().UTC()
		if err := write.FinishSummary(cleanup, attempt); err != nil {
			// Do not report a durable terminal state when its write is uncertain.
			attempt.Status, attempt.Code, attempt.FinishedAt = "started", "", time.Time{}
			return attempt, errors.Join(cause, errors.New("cannot persist summary terminal state"))
		}
		return attempt, cause
	}
	summarizer := sessions.Summarizer{Provider: adapter, Model: model.Model, ContextTokens: model.ContextTokens, Timeout: time.Minute, EstimatedCost: *model.EstimatedCost, MaxCost: maxCost}
	draft, err := summarizer.Draft(ctx, input, keep)
	if err != nil {
		code := "summary_failed"
		if ctx.Err() != nil {
			code = "canceled"
		}
		return fail(code, errors.New("summary draft failed"))
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
	if err := write.CompleteSummary(ctx, attempt); err != nil {
		return fail("persistence_failed", errors.New("cannot persist summary draft"))
	}
	return attempt, nil
}
