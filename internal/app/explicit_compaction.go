package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ArronJablonowski/DarwinRouter/contextengine"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

// prepareExplicitApprovedCompaction freezes the complete initial context before
// local resource or managed-residency mutation. An initial overflow may select
// a currently approved replacement immediately. When full history still fits,
// the built-in history-first path instead retains full context for turn one and
// gives the runtime one exact approved alternative for later growth. Trusted
// custom estimators still run only inside the durable runtime.
func (s *Service) prepareExplicitApprovedCompaction(ctx context.Context, r Request, model config.Model) (Request, error) {
	if !s.settings.Runtime.AutoApprovedCompaction || r.delegatedParent != "" || r.ContinueTaskID == "" || r.Compaction != nil || r.SummaryAttemptID != "" || model.ContextTokens < 1 {
		return r, nil
	}
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return r, ErrAdmission
	}
	defer db.Close()
	var preparedEngine runtime.ContextEngineIdentity
	if s.contextEngine != nil {
		// A schema-50 plan may use a custom engine only when its identity is
		// stable across both complete assemblies. Undescribed legacy engines
		// remain usable for ordinary, non-planned context assembly.
		preparedEngine, _ = contextengine.DescribeEngine(ctx, s.contextEngine)
	}
	secrets := memorySecrets(s.settings, s.secret)
	full, inference, err := s.prepareExplicitInference(ctx, db, r, model, secrets)
	if err != nil {
		return r, err
	}
	fullEstimate, err := providers.EstimateContext(inference)
	if err != nil {
		return full, err
	}
	pendingEligible := fullEstimate <= model.ContextTokens && midTaskCompactionProvider(s.settings, model)
	if fullEstimate <= model.ContextTokens && !pendingEligible {
		return full, nil
	}
	attempt, review, err := db.LatestApprovedSummary(ctx, r.ContinueTaskID)
	if errors.Is(err, sql.ErrNoRows) {
		return full, nil
	}
	if err != nil {
		return r, ErrAdmission
	}
	compact := full
	compact.SummaryAttemptID = attempt.ID
	compact.continuation = nil
	compact.preparedContext = nil
	compact, compactInference, err := s.prepareExplicitInference(ctx, db, compact, model, secrets)
	if err != nil {
		return r, err
	}
	compactEstimate, err := providers.EstimateContext(compactInference)
	if err != nil {
		return r, err
	}
	if compactEstimate > model.ContextTokens {
		return full, nil
	}
	if fullEstimate > model.ContextTokens {
		return compact, nil
	}
	if compactEstimate >= fullEstimate || !exactMessagePrefix(inference.Messages, full.continuation.Messages) ||
		!exactMessagePrefix(compactInference.Messages, compact.continuation.Messages) {
		return full, nil
	}
	fullTail := inference.Messages[len(full.continuation.Messages):]
	compactTail := compactInference.Messages[len(compact.continuation.Messages):]
	if !sameMessages(fullTail, compactTail) {
		return full, nil
	}
	if s.contextEngine == nil {
		full.approvedCompaction = &runtime.ApprovedCompaction{
			Compaction:        compact.continuation.Compaction,
			OriginalPrefix:    inference.Messages,
			ReplacementPrefix: compactInference.Messages,
		}
		return full, nil
	}
	plan, err := s.prepareCustomCompactionPlan(ctx, db, attempt, review, inference.Messages, compactInference.Messages, compact.continuation.Compaction, preparedEngine)
	if err != nil {
		return r, err
	}
	full.compactionPlan = plan
	return full, nil
}

func midTaskCompactionProvider(settings config.Settings, model config.Model) bool {
	for _, provider := range settings.Providers {
		if provider.ID == model.Provider {
			return provider.Kind != "codex_app_server"
		}
	}
	return false
}

func exactMessagePrefix(all, prefix []providers.Message) bool {
	return len(prefix) > 0 && len(all) >= len(prefix) && sameMessages(all[:len(prefix)], prefix)
}

func sameMessages(a, b []providers.Message) bool {
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

func (s *Service) prepareExplicitInference(ctx context.Context, db *telemetry.Store, r Request, model config.Model, secrets []string) (Request, providers.Request, error) {
	if r.continuation == nil {
		var err error
		r.continuation, err = loadContinuation(ctx, db, r, secrets)
		if err != nil {
			return r, providers.Request{}, err
		}
	}
	if r.continuation.Privacy != "cloud_allowed" && model.Locality != "local" {
		return r, providers.Request{}, ErrAdmission
	}
	var err error
	if !r.memoryPrepared && (model.Locality == "local" || !s.settings.Memory.LocalOnly) {
		r.memoryContext, err = loadMemoryContext(ctx, selectMemoryStore(r.memoryStore, db), s.settings.Memory, model.Locality == "local", secrets, memoryTaskQuery(r))
		if err != nil {
			return r, providers.Request{}, ErrAdmission
		}
	}
	if r.memoryContext != nil && (r.memoryContext.LocalOnly && model.Locality != "local") {
		return r, providers.Request{}, ErrAdmission
	}
	if !r.skillPrepared && (model.Locality == "local" || !s.settings.Skills.LocalOnly) {
		r.skillContext, err = loadSkillContextFrom(ctx, r.skillStore, s.settings.Skills, r.Domain, contextTools(s.settings, r.toolExtension), secrets)
		if err != nil {
			return r, providers.Request{}, ErrAdmission
		}
	}
	if r.skillContext != nil && r.skillContext.LocalOnly && model.Locality != "local" {
		return r, providers.Request{}, ErrAdmission
	}
	messages, err := prepareTaskContext(ctx, &r, secrets)
	if err != nil {
		return r, providers.Request{}, ErrAdmission
	}
	return r, providers.Request{Model: model.Model, Messages: messages, Tools: initialTaskTools(s.settings, r)}, nil
}

func initialTaskTools(cfg config.Settings, r Request) []providers.Tool {
	result := r.toolExtension.Catalog()
	if cfg.Tools.Enabled {
		result = append(result, readFileSpec())
	}
	if cfg.Tools.WorkboardReadEnabled {
		result = append(result, workboardListSpec(), workboardReadSpec())
	}
	if cfg.Tools.WorkboardWriteEnabled {
		result = append(result, workboardMutationSpecs()...)
	}
	if cfg.Tools.CreateEnabled {
		result = append(result, createFileSpec())
	}
	if cfg.Tools.ReplaceEnabled {
		result = append(result, replaceFileSpec())
	}
	if cfg.Workers.DelegateModel != "" {
		result = append(result, delegateSpec(), delegateBatchSpec())
	}
	return result
}
