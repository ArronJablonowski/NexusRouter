package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

type learningValidation struct {
	id        string
	validator skills.Validator
}

// LearningStepWithValidation enables validation/activation for an explicitly
// configured trusted Go host. The named validator is part of durable policy;
// changing its implementation requires a new identity. No model assertion or
// generated test text is automatically trusted as validation evidence.
func (s *Service) LearningStepWithValidation(ctx context.Context, validatorID string, validator skills.Validator) (skills.LearningState, error) {
	validation, err := s.learningValidator(validatorID, validator)
	if err != nil {
		return skills.LearningState{}, err
	}
	return s.learningStep(ctx, validation)
}

func (s *Service) learningValidator(id string, validator skills.Validator) (*learningValidation, error) {
	if s == nil || !s.settings.Skills.AutoActivate || !skillGenerationIdentifier.MatchString(id) || validator == nil || nilSkillActivationValidator(validator) || !selectionValueClean(id, memorySecrets(s.settings, s.secret)) {
		return nil, ErrLearningAttention
	}
	return &learningValidation{id: id, validator: validator}, nil
}

func (s *Service) learningPolicyForValidation(validation *learningValidation) (string, error) {
	base, err := s.learningPolicy()
	if err != nil || validation == nil {
		return base, err
	}
	if _, err := s.learningValidator(validation.id, validation.validator); err != nil {
		return "", err
	}
	body, err := json.Marshal(struct {
		Version     int    `json:"version"`
		Config      string `json:"config"`
		ValidatorID string `json:"validator_id"`
	}{1, base, validation.id})
	if err != nil {
		return "", ErrLearningAttention
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

// The immutable intent is persisted before a later tick invokes validation. Its
// saved precondition is never refreshed after a conflict or lost acknowledgement.
func (s *Service) learningActivate(ctx context.Context, db *telemetry.Store, state skills.LearningState, version skills.Version, validation *learningValidation) (bool, error) {
	bad := func() (bool, error) { return false, ErrLearningAttention }
	intent, err := db.LearningActivationIntent(ctx, state.Scope, state.Name, state.PendingSelectionID)
	if errors.Is(err, sql.ErrNoRows) {
		expected, err := s.SkillActivationState(ctx, version.Draft.Key)
		if err != nil {
			return bad()
		}
		intent = skills.LearningActivationIntent{Version: 1, Scope: state.Scope, Name: state.Name, SelectionID: state.PendingSelectionID, PolicyDigest: state.PolicyDigest, ValidatorID: validation.id, LearningRevision: state.Revision, Expected: expected, Candidate: version.ID, CreatedAt: time.Now().UTC()}
		if intent.Validate() != nil || !selectionValueClean(intent, memorySecrets(s.settings, s.secret)) {
			return bad()
		}
		if err := db.PutLearningActivationIntent(ctx, intent); err != nil {
			// A competing tick may have persisted the identical decision with
			// its own timestamp. Adopt that immutable binding, never replace it.
			stored, readErr := db.LearningActivationIntent(ctx, state.Scope, state.Name, state.PendingSelectionID)
			comparison := stored
			comparison.CreatedAt = intent.CreatedAt
			if readErr != nil || stored.Validate() != nil || comparison != intent || !selectionValueClean(stored, memorySecrets(s.settings, s.secret)) {
				return bad()
			}
		}
		return false, nil
	}
	if err != nil || intent.Validate() != nil || intent.Scope != state.Scope || intent.Name != state.Name || intent.SelectionID != state.PendingSelectionID || intent.PolicyDigest != state.PolicyDigest || intent.ValidatorID != validation.id || intent.LearningRevision != state.Revision || intent.Expected.Key != version.Draft.Key || intent.Candidate != version.ID || !selectionValueClean(intent, memorySecrets(s.settings, s.secret)) {
		return bad()
	}
	// Include the validator's durable identity in credential checks before and
	// after callback execution; it is new metadata not covered by the base guard.
	guarded := skills.ValidatorFunc(func(call context.Context, candidate skills.Version) (skills.Evidence, error) {
		before := memorySecrets(s.settings, s.secret)
		if !selectionValueClean(intent, before) {
			return skills.Evidence{}, skills.ErrValidation
		}
		proof, err := validation.validator.Validate(call, candidate)
		if err != nil || !selectionValueClean(intent, append(before, memorySecrets(s.settings, s.secret)...)) {
			return skills.Evidence{}, skills.ErrValidation
		}
		return proof, nil
	})
	if s.ActivateSkillVersionOnce(ctx, state.PendingSelectionID, intent.Expected, intent.Candidate, guarded) != nil {
		return bad()
	}
	return true, nil
}
