package app

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

const meaningfulWorkboardOutputValidator = workboard.MeaningfulTextCandidateValidatorID

type workboardEvaluationEventSource interface {
	Read(context.Context, string, int64, int) ([]runtime.Event, error)
}

// configuredWorkboardCandidateEvaluator combines host-owned deterministic
// outcomes with the independently budgeted advisory reviewer. Model output is
// never given a path to construct deterministic evidence.
type configuredWorkboardCandidateEvaluator struct {
	events     workboardEvaluationEventSource
	reviewer   workboard.BudgetedCandidateEvaluator
	validators *workboard.CandidateValidatorRegistry
}

func newConfiguredWorkboardCandidateEvaluator(events workboardEvaluationEventSource, reviewer workboard.CandidateEvaluator,
	configured ...*workboard.CandidateValidatorRegistry,
) (workboard.CandidateEvaluator, error) {
	budgeted, ok := reviewer.(workboard.BudgetedCandidateEvaluator)
	if events == nil || !ok || len(configured) > 1 {
		return nil, ErrAdmission
	}
	registry, err := workboard.NewCandidateValidatorRegistry(nil)
	if len(configured) == 1 {
		registry, err = configured[0], nil
	}
	if err != nil || registry == nil {
		return nil, ErrAdmission
	}
	if _, err = registry.Resolve(workboard.MeaningfulTextCandidateValidatorID); err != nil {
		return nil, ErrAdmission
	}
	return &configuredWorkboardCandidateEvaluator{events: events, reviewer: budgeted, validators: registry}, nil
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
	if e == nil || e.events == nil || e.validators == nil || e.reviewer == nil || ctx == nil || ctx.Err() != nil || frozen.Validate() != nil {
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
		validator, err := e.validators.Resolve(criterion.ValidatorID)
		if err != nil {
			// Runtime deterministic evaluation events are trusted host output. Bind
			// only the exact configured identity and immutable event result into a
			// one-entry registry; arbitrary or missing identities still fail closed.
			if event, ok := byValidator[criterion.ValidatorID]; ok {
				bound := event
				transient, buildErr := workboard.NewCandidateValidatorRegistry(map[string]workboard.CandidateValidator{
					criterion.ValidatorID: workboard.CandidateValidatorFunc(func(context.Context, workboard.CandidateValidationInput) (workboard.CandidateValidationDecision, error) {
						return workboard.CandidateValidationDecision{Passed: *bound.Data.Accepted, Reference: bound.ID}, nil
					}),
				})
				if buildErr == nil {
					validator, err = transient.Resolve(criterion.ValidatorID)
				}
			}
		}
		if err != nil {
			if !criterion.Required {
				continue
			}
			return nil, ErrAdmission
		}
		decision, err := workboard.InvokeCandidateValidator(ctx, validator, workboard.CandidateValidationInput{
			Candidate: frozen,
			Criterion: criterion,
		})
		if err != nil {
			return nil, ErrAdmission
		}
		outcome := "failed"
		if decision.Passed {
			outcome = "passed"
		}
		item := workboard.EvidenceInput{CriterionID: criterion.ID, Source: "deterministic",
			Outcome: outcome, ActorID: criterion.ValidatorID, ActorType: "validator", Reference: decision.Reference}
		if item.Validate() != nil {
			return nil, ErrAdmission
		}
		result = append(result, item)
	}
	return result, nil
}

func meaningfulWorkboardOutput(output string, criteria []workboard.AcceptanceCriterion) bool {
	return workboard.MeaningfulCandidateOutput(output, criteria)
}

var _ workboard.BudgetedCandidateEvaluator = (*configuredWorkboardCandidateEvaluator)(nil)
