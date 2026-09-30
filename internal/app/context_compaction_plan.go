package app

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"time"

	"github.com/ArronJablonowski/NexusRouter/contextengine"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// prepareCustomCompactionPlan turns one deterministic, approved schema-50
// summary operation into the exact future prefix replacement admitted by the
// runtime. Start.Tiers records summary-preparation provenance; the sealed
// prefix digests bind the later full and compact continuation assemblies.
func (s *Service) prepareCustomCompactionPlan(ctx context.Context, read *telemetry.Store, attempt sessions.SummaryAttempt, review sessions.SummaryReview, original, replacement []providers.Message, compaction *runtime.ContextCompaction, before runtime.ContextEngineIdentity) (*runtime.ContextCompactionPlan, error) {
	if review.Version != 2 {
		// Legacy/manual reviews remain valid for explicit initial continuation,
		// but cannot authorize deferred custom-engine activation.
		return nil, nil
	}
	state, err := read.ContextCompactionPlanForAttempt(ctx, attempt.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil || attempt.Draft == nil || state.Validate() != nil || state.Start.AttemptID != attempt.ID || state.TerminalAttempt == nil ||
		!reflect.DeepEqual(*state.TerminalAttempt, attempt) || review.AttemptID != attempt.ID || review.Decision != "approved" ||
		review.SourceSequence != attempt.SourceSequence || review.SourceDigest != attempt.SourceDigest || compaction == nil ||
		compaction.SummaryAttemptID != attempt.ID || compaction.SummaryReviewID != review.ID || before.Validate() != nil ||
		before != state.Start.Engine {
		return nil, ErrAdmission
	}
	after, err := contextengine.DescribeEngine(ctx, s.contextEngine)
	if err != nil || after != before || ctx.Err() != nil {
		return nil, ErrAdmission
	}
	draftDigest, err := sessions.SummaryDraftDigest(*attempt.Draft)
	if err != nil || review.DraftDigest != draftDigest {
		return nil, ErrAdmission
	}
	plan, err := runtime.SealContextCompactionPlan(runtime.ContextCompactionPlan{
		OperationID: state.Start.OperationID, OperationDigest: state.Start.OperationDigest,
		RequestID: state.Start.RequestID, RequestDigest: state.Start.RequestDigest,
		Compaction: compaction, ConfigDigest: state.Start.ConfigDigest, PolicyDigest: state.Start.PolicyDigest,
		Engine: state.Start.Engine, Tiers: state.Start.Tiers, OriginalPrefix: original,
		ReplacementPrefix: replacement, LiveSuffixBoundary: len(original), DraftDigest: draftDigest,
	})
	if err != nil || !selectionValueClean(plan, memorySecrets(s.settings, s.secret)) {
		return nil, ErrAdmission
	}
	write, err := telemetry.Open(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return nil, ErrAdmission
	}
	defer write.Close()
	state, err = advanceCustomCompactionPlan(ctx, write, state, plan, review)
	if err != nil || state.Status != sessions.ContextCompactionApproved || state.Plan == nil || !reflect.DeepEqual(*state.Plan, plan) {
		return nil, ErrAdmission
	}
	owned := *state.Plan
	return &owned, nil
}

func advanceCustomCompactionPlan(ctx context.Context, store *telemetry.Store, state sessions.ContextCompactionOperationState, plan runtime.ContextCompactionPlan, review sessions.SummaryReview) (sessions.ContextCompactionOperationState, error) {
	for {
		if state.Plan != nil && !reflect.DeepEqual(*state.Plan, plan) {
			return state, telemetry.ErrConflict
		}
		var kind sessions.ContextCompactionLifecycleKind
		var advance func(context.Context, sessions.ContextCompactionLifecycleFact) (sessions.ContextCompactionOperationState, error)
		switch state.Status {
		case sessions.ContextCompactionStarted:
			kind, advance = sessions.ContextCompactionPrepared, func(ctx context.Context, fact sessions.ContextCompactionLifecycleFact) (sessions.ContextCompactionOperationState, error) {
				return store.PrepareContextCompactionPlan(ctx, plan, fact)
			}
		case sessions.ContextCompactionPrepared:
			kind, advance = sessions.ContextCompactionValidated, store.ValidateContextCompactionPlan
		case sessions.ContextCompactionValidated:
			kind, advance = sessions.ContextCompactionApproved, store.ApproveContextCompactionPlan
		case sessions.ContextCompactionApproved:
			return state, nil
		default:
			return state, telemetry.ErrConflict
		}
		last := state.Facts[len(state.Facts)-1]
		fact, err := sessions.SealContextCompactionLifecycleFact(sessions.ContextCompactionLifecycleFact{
			ID: summaryPreparationID(string(kind)+"-fact", plan.OperationID), OperationID: plan.OperationID,
			Sequence: last.Sequence + 1, PreviousID: last.ID, Kind: kind, PlanDigest: plan.PlanDigest,
			SummaryAttemptID: plan.Compaction.SummaryAttemptID, SummaryReviewID: plan.Compaction.SummaryReviewID,
			CreatedAt: review.Time.UTC(),
		})
		if err != nil {
			return state, err
		}
		next, err := advance(ctx, fact)
		if err == nil {
			state = next
			continue
		}
		// A storage error may mean the commit won but its acknowledgement was
		// lost. Inspect with a bounded uncancelled context before deciding.
		inspect, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		next, inspectErr := store.ContextCompactionPlan(inspect, plan.OperationID)
		cancel()
		if inspectErr != nil || next.Status != kind || next.Plan == nil || !reflect.DeepEqual(*next.Plan, plan) {
			return state, err
		}
		state = next
	}
}
