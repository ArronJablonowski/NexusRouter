package app

import (
	"context"
	"strings"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

const meaningfulWorkboardOutputValidator = "deterministic.meaningful_text.v1"

type workboardEvaluationEventSource interface {
	Read(context.Context, string, int64, int) ([]runtime.Event, error)
}

// configuredWorkboardCandidateEvaluator combines host-owned deterministic
// outcomes with the independently budgeted advisory reviewer. Model output is
// never given a path to construct deterministic evidence.
type configuredWorkboardCandidateEvaluator struct {
	events   workboardEvaluationEventSource
	reviewer workboard.BudgetedCandidateEvaluator
}

func newConfiguredWorkboardCandidateEvaluator(events workboardEvaluationEventSource,
	reviewer workboard.CandidateEvaluator,
) (workboard.CandidateEvaluator, error) {
	budgeted, ok := reviewer.(workboard.BudgetedCandidateEvaluator)
	if events == nil || !ok {
		return nil, ErrAdmission
	}
	return &configuredWorkboardCandidateEvaluator{events: events, reviewer: budgeted}, nil
}

func (*configuredWorkboardCandidateEvaluator) EvaluateCandidate(context.Context,
	workboard.CandidateEvaluationRequest,
) ([]workboard.EvidenceInput, error) {
	return nil, ErrAdmission
}

func (e *configuredWorkboardCandidateEvaluator) AuxiliaryReviewReservation(
	frozen workboard.CandidateEvaluationRequest,
) (workboard.AuxiliaryReviewReservation, error) {
	if e == nil || e.reviewer == nil {
		return workboard.AuxiliaryReviewReservation{}, ErrAdmission
	}
	return e.reviewer.AuxiliaryReviewReservation(frozen)
}

func (e *configuredWorkboardCandidateEvaluator) EvaluateBudgetedCandidate(ctx context.Context,
	frozen workboard.CandidateEvaluationRequest,
) (workboard.BudgetedCandidateEvaluation, error) {
	if e == nil || e.events == nil || e.reviewer == nil || ctx == nil || ctx.Err() != nil || frozen.Validate() != nil {
		return workboard.BudgetedCandidateEvaluation{}, ErrAdmission
	}
	deterministic, err := e.deterministicEvidence(ctx, frozen)
	if err != nil {
		return workboard.BudgetedCandidateEvaluation{}, err
	}
	result, err := e.reviewer.EvaluateBudgetedCandidate(ctx, frozen)
	if err != nil {
		return result, err
	}
	if workboard.ValidateAuxiliaryReviewEvidence(frozen, result.Audit, result.Evidence) != nil {
		return workboard.BudgetedCandidateEvaluation{}, ErrAdmission
	}
	result.Evidence = append(deterministic, result.Evidence...)
	if workboard.ValidateBudgetedCandidateEvidence(frozen, result.Audit, result.Evidence) != nil {
		return workboard.BudgetedCandidateEvaluation{}, ErrAdmission
	}
	return result, nil
}

func (e *configuredWorkboardCandidateEvaluator) deterministicEvidence(ctx context.Context,
	frozen workboard.CandidateEvaluationRequest,
) ([]workboard.EvidenceInput, error) {
	events, err := e.events.Read(ctx, frozen.SourceTaskID, frozen.SourceCompletionSequence,
		int(frozen.SourceTerminalSequence-frozen.SourceCompletionSequence))
	if err != nil {
		return nil, err
	}
	byValidator := make(map[string]runtime.Event, len(events))
	for _, event := range events {
		if event.Kind != runtime.EvaluationRecorded || event.TaskID != frozen.SourceTaskID ||
			event.SessionID != frozen.SourceSessionID || event.TurnID != frozen.SourceTurnID ||
			event.AttemptID != frozen.SourceAttemptID || event.Sequence <= frozen.SourceCompletionSequence ||
			event.Sequence >= frozen.SourceTerminalSequence || event.Data.Accepted == nil || event.Data.Code == "" {
			continue
		}
		if _, duplicate := byValidator[event.Data.Code]; duplicate {
			return nil, ErrAdmission
		}
		byValidator[event.Data.Code] = event
	}
	result := make([]workboard.EvidenceInput, 0, len(frozen.Criteria))
	for _, criterion := range frozen.Criteria {
		if criterion.RequiredSource != "deterministic" {
			continue
		}
		passed, reference, found := false, "", false
		if criterion.ValidatorID == meaningfulWorkboardOutputValidator {
			passed, reference, found = meaningfulWorkboardOutput(frozen.SourceOutput, frozen.Criteria), frozen.SourceCompletionEventID, true
		} else if event, ok := byValidator[criterion.ValidatorID]; ok {
			passed, reference, found = *event.Data.Accepted, event.ID, true
		}
		if !found {
			continue
		}
		outcome := "failed"
		if passed {
			outcome = "passed"
		}
		item := workboard.EvidenceInput{CriterionID: criterion.ID, Source: "deterministic",
			Outcome: outcome, ActorID: criterion.ValidatorID, ActorType: "validator", Reference: reference}
		if item.Validate() != nil {
			return nil, ErrAdmission
		}
		result = append(result, item)
	}
	return result, nil
}

// meaningfulWorkboardOutput is deliberately conservative. It proves only that
// the exact runtime output contains substantive text rather than whitespace or
// a promise to perform the requested work later.
func meaningfulWorkboardOutput(output string, criteria []workboard.AcceptanceCriterion) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(output), " "))
	normalized = strings.Trim(normalized, " .,!?:;\"'")
	if normalized == "" {
		return false
	}
	for _, criterion := range criteria {
		repeated := strings.ToLower(strings.Join(strings.Fields(criterion.Description), " "))
		if normalized == strings.Trim(repeated, " .,!?:;\"'") {
			return false
		}
	}
	words := strings.Fields(normalized)
	for _, promise := range []string{
		"i will ", "i'll ", "i can ", "let me ", "working on it", "sure, i will ", "sure, i'll ",
	} {
		if len(words) <= 8 && (normalized == strings.TrimSpace(promise) || strings.HasPrefix(normalized, promise)) {
			return false
		}
	}
	return true
}

var _ workboard.BudgetedCandidateEvaluator = (*configuredWorkboardCandidateEvaluator)(nil)
