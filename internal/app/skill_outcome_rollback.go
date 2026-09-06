package app

import (
	"context"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

// OutcomeRollbackOnce is an opt-in trusted-host policy action, not a
// deterministic validator. Reports come only from the configured SQLite
// selector. A successful no-action receipt also consumes this activation's
// adjudication; exact receipt retries never select newer evidence. A durable
// preselection intent consumes the attempt even when selection is interrupted.
func (s *Service) OutcomeRollbackOnce(ctx context.Context, operation string, expected skills.ActivationState, request skills.ComparisonSelectionRequest) (out skills.OutcomeRollbackReceipt, err error) {
	defer func() {
		if recover() != nil {
			out, err = skills.OutcomeRollbackReceipt{}, ErrAdmission
		}
	}()
	if ctx == nil || ctx.Err() != nil || !s.outcomeRollbackConfigured(expected) || !skillGenerationIdentifier.MatchString(operation) {
		return out, ErrAdmission
	}
	policy, err := s.skillComparisonSelectionPolicy(request)
	if err != nil || policy.Comparison.Key != expected.Key || policy.Comparison.CandidateVersion != expected.Active {
		return out, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	root, database := s.settings.Skills.Root, s.settings.Telemetry.Database
	secrets := memorySecrets(s.settings, s.secret)
	if s.settings.Skills.Root != root || s.settings.Telemetry.Database != database || !selectionValueClean([]any{operation, expected, request, policy}, secrets) {
		return out, ErrAdmission
	}
	read, err := skills.OpenReadOnly(root, []string{expected.Key.Scope})
	if err != nil {
		return out, ErrAdmission
	}
	defer read.Close()
	cleanReceipt := func(receipt skills.OutcomeRollbackReceipt) bool {
		currentPolicy, policyErr := s.skillComparisonSelectionPolicy(request)
		return s.settings.Skills.Root == root && s.settings.Telemetry.Database == database && s.outcomeRollbackConfigured(expected) && policyErr == nil && currentPolicy == policy && receipt.Validate() == nil && receipt.OperationID == operation && receipt.Expected == expected && receipt.Policy == policy && receipt.Selection.ConfiguredModelID == request.ModelID && selectionValueClean([]any{operation, expected, request, policy, receipt}, secrets)
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
	guard := skills.OutcomeRollbackGuard(func(call context.Context, r skills.OutcomeRollbackReceipt) error {
		secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
		currentPolicy, policyErr := s.skillComparisonSelectionPolicy(request)
		if call.Err() != nil || !s.outcomeRollbackConfigured(expected) || policyErr != nil || currentPolicy != policy || !cleanReceipt(r) || !skillActivationCandidateClean(candidate, secrets) || !skillActivationCandidateClean(baseline, secrets) {
			return ErrAdmission
		}
		return nil
	})
	intentGuard := skills.OutcomeIntentGuard(func(call context.Context, intent skills.OutcomeRollbackIntent) error {
		secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
		currentPolicy, policyErr := s.skillComparisonSelectionPolicy(request)
		if call.Err() != nil || s.settings.Skills.Root != root || s.settings.Telemetry.Database != database || !s.outcomeRollbackConfigured(expected) || policyErr != nil || currentPolicy != policy || intent.Validate() != nil || intent.OperationID != operation || intent.ConfiguredModelID != request.ModelID || intent.Expected != expected || intent.Policy != policy || !selectionValueClean([]any{operation, expected, request, intent}, secrets) || !skillActivationCandidateClean(candidate, secrets) || !skillActivationCandidateClean(baseline, secrets) {
			return ErrAdmission
		}
		return nil
	})
	selectionGuard := skills.OutcomeSelectionGuard(func(call context.Context, checkpoint skills.OutcomeSelectionCheckpoint) error {
		secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
		currentPolicy, policyErr := s.skillComparisonSelectionPolicy(request)
		if call.Err() != nil || s.settings.Skills.Root != root || s.settings.Telemetry.Database != database || !s.outcomeRollbackConfigured(expected) || policyErr != nil || currentPolicy != policy || checkpoint.Validate() != nil || checkpoint.OperationID != operation || checkpoint.Intent.Expected != expected || checkpoint.Intent.Policy != policy || checkpoint.Intent.ConfiguredModelID != request.ModelID || checkpoint.Report.Policy != policy || checkpoint.Report.ConfiguredModelID != request.ModelID || !selectionValueClean([]any{operation, expected, request, checkpoint}, secrets) || !skillActivationCandidateClean(candidate, secrets) || !skillActivationCandidateClean(baseline, secrets) {
			return ErrAdmission
		}
		observedSecrets, sourceErr := s.checkOutcomeSelectionSources(call, database, checkpoint.Report, secrets)
		if sourceErr != nil {
			return ErrAdmission
		}
		secrets = append(secrets, observedSecrets...)
		secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
		currentPolicy, policyErr = s.skillComparisonSelectionPolicy(request)
		if call.Err() != nil || s.settings.Skills.Root != root || s.settings.Telemetry.Database != database || !s.outcomeRollbackConfigured(expected) || policyErr != nil || currentPolicy != policy || !selectionValueClean([]any{operation, expected, request, checkpoint}, secrets) {
			return ErrAdmission
		}
		return nil
	})
	selector := skills.OutcomeSelector(func(call context.Context) (skills.ComparisonSelectionReport, error) {
		secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
		if s.settings.Skills.Root != root || s.settings.Telemetry.Database != database || !s.outcomeRollbackConfigured(expected) || !selectionValueClean([]any{operation, expected, request, policy}, secrets) {
			return skills.ComparisonSelectionReport{}, ErrAdmission
		}
		r, e := s.SelectSkillComparison(call, request)
		if e != nil || r.Policy != policy || r.ConfiguredModelID != request.ModelID {
			return skills.ComparisonSelectionReport{}, ErrAdmission
		}
		return r, nil
	})
	receipt, err = store.OutcomeRollbackOnceCheckpointed(ctx, operation, request.ModelID, expected, policy, selector, guard, intentGuard, selectionGuard)
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if err != nil || ctx.Err() != nil || !cleanReceipt(receipt) {
		return out, ErrAdmission
	}
	return receipt, nil
}

func (s *Service) outcomeRollbackConfigured(expected skills.ActivationState) bool {
	return expected.Validate() == nil && expected.Active != "" && s.skillActivationConfigured(expected.Key) && s.settings.Skills.Rollback && s.settings.Skills.OutcomeRollback
}

// OutcomeRollbackOperation inspects historical metadata without creating a
// catalog, migrating SQLite or authorizing another action. Rollback can be off.
func (s *Service) OutcomeRollbackOperation(ctx context.Context, key skills.Key, operation string) (out skills.OutcomeRollbackReceipt, err error) {
	defer func() {
		if recover() != nil {
			out, err = skills.OutcomeRollbackReceipt{}, ErrAdmission
		}
	}()
	if ctx == nil || ctx.Err() != nil || !s.skillActivationConfigured(key) || !skillGenerationIdentifier.MatchString(operation) {
		return out, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	secrets := memorySecrets(s.settings, s.secret)
	if !selectionValueClean([]any{key, operation}, secrets) {
		return out, ErrAdmission
	}
	store, err := skills.OpenReadOnly(s.settings.Skills.Root, []string{key.Scope})
	if err != nil {
		return out, ErrAdmission
	}
	defer store.Close()
	receipt, err := store.OutcomeRollbackOperation(ctx, key, operation)
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if err != nil || receipt.Validate() != nil || receipt.Expected.Key != key || receipt.OperationID != operation || ctx.Err() != nil || !selectionValueClean(receipt, secrets) {
		return out, ErrAdmission
	}
	return receipt, nil
}
