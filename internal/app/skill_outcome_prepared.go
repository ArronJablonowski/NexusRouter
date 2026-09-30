package app

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

// OutcomeRollbackPrepared applies an already-selected, decision-ready snapshot.
// Unlike OutcomeRollbackOnce it never invokes a selector after claiming the
// activation: intent and checkpoint are committed together by the skills store.
func (s *Service) OutcomeRollbackPrepared(ctx context.Context, operation string, expected skills.ActivationState,
	request skills.ComparisonSelectionRequest, selection skills.ComparisonSelectionReport,
) (out skills.OutcomeRollbackReceipt, err error) {
	defer func() {
		if recover() != nil {
			out, err = skills.OutcomeRollbackReceipt{}, ErrAdmission
		}
	}()
	if ctx == nil || ctx.Err() != nil || !s.outcomeRollbackConfigured(expected) || !skillGenerationIdentifier.MatchString(operation) {
		return out, ErrAdmission
	}
	policy, err := s.skillComparisonSelectionPolicy(request)
	if err != nil || policy.Comparison.Key != expected.Key || policy.Comparison.CandidateVersion != expected.Active ||
		selection.Validate() != nil || selection.Policy != policy || selection.ConfiguredModelID != request.ModelID {
		return out, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	root, database := s.settings.Skills.Root, s.settings.Telemetry.Database
	secrets := memorySecrets(s.settings, s.secret)
	if !selectionValueClean([]any{operation, expected, request, policy, selection}, secrets) {
		return out, ErrAdmission
	}
	read, err := skills.OpenReadOnly(root, []string{expected.Key.Scope})
	if err != nil {
		return out, ErrAdmission
	}
	defer read.Close()
	cleanReceipt := func(receipt skills.OutcomeRollbackReceipt) bool {
		currentPolicy, policyErr := s.skillComparisonSelectionPolicy(request)
		return s.settings.Skills.Root == root && s.settings.Telemetry.Database == database && s.outcomeRollbackConfigured(expected) &&
			policyErr == nil && currentPolicy == policy && receipt.Validate() == nil && receipt.OperationID == operation &&
			receipt.Expected == expected && receipt.Policy == policy && reflect.DeepEqual(receipt.Selection, selection) &&
			selectionValueClean([]any{operation, expected, request, policy, selection, receipt}, secrets)
	}
	receipt, err := read.OutcomeRollbackOperation(ctx, expected.Key, operation)
	if err == nil {
		secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
		if ctx.Err() != nil || !cleanReceipt(receipt) {
			return out, ErrAdmission
		}
		return receipt, nil
	}
	if !errors.Is(err, skills.ErrNotFound) {
		return out, ErrAdmission
	}
	current, err := read.ActivationState(ctx, expected.Key)
	if err != nil || current != expected {
		return out, ErrAdmission
	}
	candidate, err := read.Load(ctx, expected.Key, expected.Active)
	if err != nil || candidate.Validate() != nil || !skillActivationCandidateClean(candidate, secrets) {
		return out, ErrAdmission
	}
	baseline, err := read.Load(ctx, expected.Key, policy.Comparison.BaselineVersion)
	if err != nil || baseline.Validate() != nil || !skillActivationCandidateClean(baseline, secrets) {
		return out, ErrAdmission
	}
	store, err := skills.Open(root, []string{expected.Key.Scope})
	if err != nil {
		return out, ErrAdmission
	}
	defer store.Close()
	store.SetAutomatic(true)
	store.SetOutcomeRollback(true)
	receiptGuard := skills.OutcomeRollbackGuard(func(call context.Context, receipt skills.OutcomeRollbackReceipt) error {
		secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
		if call.Err() != nil || !cleanReceipt(receipt) || !skillActivationCandidateClean(candidate, secrets) || !skillActivationCandidateClean(baseline, secrets) {
			return ErrAdmission
		}
		return nil
	})
	intentGuard := skills.OutcomeIntentGuard(func(call context.Context, intent skills.OutcomeRollbackIntent) error {
		secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
		currentPolicy, policyErr := s.skillComparisonSelectionPolicy(request)
		if call.Err() != nil || s.settings.Skills.Root != root || s.settings.Telemetry.Database != database || !s.outcomeRollbackConfigured(expected) ||
			policyErr != nil || currentPolicy != policy || intent.Validate() != nil || intent.OperationID != operation ||
			intent.ConfiguredModelID != request.ModelID || intent.Expected != expected || intent.Policy != policy ||
			!selectionValueClean([]any{operation, expected, request, intent}, secrets) || !skillActivationCandidateClean(candidate, secrets) || !skillActivationCandidateClean(baseline, secrets) {
			return ErrAdmission
		}
		return nil
	})
	selectionGuard := skills.OutcomeSelectionGuard(func(call context.Context, checkpoint skills.OutcomeSelectionCheckpoint) error {
		secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
		currentPolicy, policyErr := s.skillComparisonSelectionPolicy(request)
		if call.Err() != nil || s.settings.Skills.Root != root || s.settings.Telemetry.Database != database || !s.outcomeRollbackConfigured(expected) ||
			policyErr != nil || currentPolicy != policy || checkpoint.Validate() != nil || checkpoint.OperationID != operation ||
			checkpoint.Intent.Expected != expected || checkpoint.Intent.Policy != policy || checkpoint.Intent.ConfiguredModelID != request.ModelID ||
			!reflect.DeepEqual(checkpoint.Report, selection) || !selectionValueClean([]any{operation, expected, request, checkpoint}, secrets) ||
			!skillActivationCandidateClean(candidate, secrets) || !skillActivationCandidateClean(baseline, secrets) {
			return ErrAdmission
		}
		observedSecrets, sourceErr := s.checkOutcomeSelectionSources(call, database, checkpoint.Report, secrets)
		if sourceErr != nil {
			return ErrAdmission
		}
		secrets = append(secrets, observedSecrets...)
		secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
		currentPolicy, policyErr = s.skillComparisonSelectionPolicy(request)
		if call.Err() != nil || s.settings.Skills.Root != root || s.settings.Telemetry.Database != database || !s.outcomeRollbackConfigured(expected) ||
			policyErr != nil || currentPolicy != policy || !selectionValueClean([]any{operation, expected, request, checkpoint}, secrets) {
			return ErrAdmission
		}
		return nil
	})
	receipt, err = store.OutcomeRollbackPrepared(ctx, operation, request.ModelID, expected, policy, selection, receiptGuard, intentGuard, selectionGuard)
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if err != nil || ctx.Err() != nil || !cleanReceipt(receipt) {
		return out, ErrAdmission
	}
	return receipt, nil
}
