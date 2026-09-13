package workboard

import (
	"context"
	"reflect"
	"strings"
)

const (
	// MeaningfulTextCandidateValidatorID is the protected identity of the
	// stock, deterministic validator that rejects empty and promise-only output.
	MeaningfulTextCandidateValidatorID = "deterministic.meaningful_text.v1"
	MaxCandidateValidators             = 64
)

// CandidateValidationInput is an owned view of the exact durable candidate
// snapshot and the criterion whose configured validator is being invoked.
type CandidateValidationInput struct {
	Candidate CandidateEvaluationRequest
	Criterion AcceptanceCriterion
}

// CandidateValidationDecision contains only a trusted validator's result and
// durable reference. The host, rather than the callback, constructs evidence
// identity, source, actor, and criterion fields.
type CandidateValidationDecision struct {
	Passed    bool
	Reference string
}

type CandidateValidator interface {
	ValidateCandidate(context.Context, CandidateValidationInput) (CandidateValidationDecision, error)
}

type CandidateValidatorFunc func(context.Context, CandidateValidationInput) (CandidateValidationDecision, error)

func (f CandidateValidatorFunc) ValidateCandidate(ctx context.Context, input CandidateValidationInput) (CandidateValidationDecision, error) {
	return f(ctx, input)
}

// CandidateValidatorRegistry owns an immutable snapshot of trusted host
// bindings. Callback implementations must be repeat-safe and side-effect-free;
// changed semantics require a new validator identity.
type CandidateValidatorRegistry struct{ validators map[string]CandidateValidator }

// NewCandidateValidatorRegistry installs the protected stock meaningful-text
// validator and snapshots optional host validators. The stock identity cannot
// be replaced by caller code.
func NewCandidateValidatorRegistry(input map[string]CandidateValidator) (*CandidateValidatorRegistry, error) {
	if len(input) > MaxCandidateValidators-1 {
		return nil, fail(CodeLimitExceeded, "candidate_validators")
	}
	owned := make(map[string]CandidateValidator, len(input)+1)
	owned[MeaningfulTextCandidateValidatorID] = meaningfulTextCandidateValidator{}
	for id, validator := range input {
		if !validID(id) || id == MeaningfulTextCandidateValidatorID || candidateValidatorNil(validator) {
			return nil, fail(CodeInvalid, "candidate_validator")
		}
		owned[id] = validator
	}
	return &CandidateValidatorRegistry{validators: owned}, nil
}

// Resolve performs no callback invocation and never substitutes a default for
// an unknown identity.
func (r *CandidateValidatorRegistry) Resolve(id string) (CandidateValidator, error) {
	if r == nil || !validID(id) {
		return nil, fail(CodeInvalid, "candidate_validator")
	}
	validator, ok := r.validators[id]
	if !ok {
		return nil, fail(CodeMissingNode, "candidate_validator")
	}
	if candidateValidatorNil(validator) {
		return nil, fail(CodeInvalid, "candidate_validator")
	}
	return validator, nil
}

// InvokeCandidateValidator contains callback panics, honors cancellation before
// and after cooperative execution, validates the immutable binding, and returns
// only a small host-validated decision.
func InvokeCandidateValidator(ctx context.Context, validator CandidateValidator,
	input CandidateValidationInput,
) (decision CandidateValidationDecision, err error) {
	if ctx == nil {
		return CandidateValidationDecision{}, fail(CodeInvalid, "candidate_validator")
	}
	if ctx.Err() != nil {
		return CandidateValidationDecision{}, ctx.Err()
	}
	if candidateValidatorNil(validator) || input.Candidate.Validate() != nil ||
		input.Criterion.Validate() != nil || !candidateContainsCriterion(input.Candidate.Criteria, input.Criterion) {
		return CandidateValidationDecision{}, fail(CodeInvalid, "candidate_validator")
	}
	defer func() {
		if recover() != nil {
			decision = CandidateValidationDecision{}
			if ctx.Err() != nil {
				err = ctx.Err()
			} else {
				err = fail(CodeInvalid, "candidate_validator")
			}
		}
	}()
	owned := CandidateValidationInput{Candidate: ownCandidateEvaluationRequest(input.Candidate), Criterion: input.Criterion}
	decision, err = validator.ValidateCandidate(ctx, owned)
	if ctx.Err() != nil {
		return CandidateValidationDecision{}, ctx.Err()
	}
	if err != nil || !validID(decision.Reference) {
		return CandidateValidationDecision{}, fail(CodeInvalid, "candidate_validator")
	}
	return decision, nil
}

type meaningfulTextCandidateValidator struct{}

func (meaningfulTextCandidateValidator) ValidateCandidate(_ context.Context,
	input CandidateValidationInput,
) (CandidateValidationDecision, error) {
	return CandidateValidationDecision{
		Passed:    MeaningfulCandidateOutput(input.Candidate.SourceOutput, input.Candidate.Criteria),
		Reference: input.Candidate.SourceCompletionEventID,
	}, nil
}

// MeaningfulCandidateOutput exposes the stock validator's conservative pure
// predicate for earlier host-side admission checks. It does not create evidence.
func MeaningfulCandidateOutput(output string, criteria []AcceptanceCriterion) bool {
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
	for _, promise := range []string{"i will ", "i'll ", "i can ", "let me ", "working on it", "sure, i will ", "sure, i'll "} {
		if len(words) <= 8 && (normalized == strings.TrimSpace(promise) || strings.HasPrefix(normalized, promise)) {
			return false
		}
	}
	return true
}

func candidateValidatorNil(validator CandidateValidator) bool {
	if validator == nil {
		return true
	}
	value := reflect.ValueOf(validator)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func candidateContainsCriterion(criteria []AcceptanceCriterion, target AcceptanceCriterion) bool {
	for _, criterion := range criteria {
		if criterion == target {
			return true
		}
	}
	return false
}

func ownCandidateEvaluationRequest(input CandidateEvaluationRequest) CandidateEvaluationRequest {
	owned := input
	owned.ArtifactRefs = append([]string(nil), input.ArtifactRefs...)
	owned.Criteria = append([]AcceptanceCriterion(nil), input.Criteria...)
	return owned
}
