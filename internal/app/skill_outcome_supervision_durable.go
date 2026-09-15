package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

const configuredOutcomeSupervisorName = "outcomes"

// OutcomeSupervisionState inspects the configured durable cursor without
// creating, repairing, or advancing it.
func (s *Service) OutcomeSupervisionState(ctx context.Context) (skills.OutcomeSupervisionState, error) {
	if ctx == nil || ctx.Err() != nil || !s.outcomeSupervisionConfigured() {
		return skills.OutcomeSupervisionState{}, ErrAdmission
	}
	store, err := skills.OpenReadOnly(s.settings.Skills.Root, []string{s.settings.Skills.Scope})
	if err != nil {
		return skills.OutcomeSupervisionState{}, ErrInspection
	}
	defer store.Close()
	state, err := store.OutcomeSupervisionState(ctx, s.settings.Skills.Scope, configuredOutcomeSupervisorName)
	interval, intervalErr := configuredOutcomeSupervisionInterval(s)
	policyID, policyErr := s.outcomeSupervisionPolicyDigest(interval)
	if err != nil || intervalErr != nil || policyErr != nil || state.Validate() != nil || state.Interval != interval ||
		state.PolicyDigest != policyID || !selectionValueClean(state, memorySecrets(s.settings, s.secret)) {
		return skills.OutcomeSupervisionState{}, ErrInspection
	}
	return state, nil
}

// OutcomeSupervisionCheck inspects one exact configured durable check.
func (s *Service) OutcomeSupervisionCheck(ctx context.Context, checkID string) (skills.OutcomeSupervisionCheck, error) {
	if ctx == nil || ctx.Err() != nil || !s.outcomeSupervisionConfigured() {
		return skills.OutcomeSupervisionCheck{}, ErrAdmission
	}
	store, err := skills.OpenReadOnly(s.settings.Skills.Root, []string{s.settings.Skills.Scope})
	if err != nil {
		return skills.OutcomeSupervisionCheck{}, ErrInspection
	}
	defer store.Close()
	check, err := store.OutcomeSupervisionCheck(ctx, s.settings.Skills.Scope, configuredOutcomeSupervisorName, checkID)
	interval, intervalErr := configuredOutcomeSupervisionInterval(s)
	policyID, policyErr := s.outcomeSupervisionPolicyDigest(interval)
	if err != nil || intervalErr != nil || policyErr != nil || check.Validate() != nil || check.PolicyDigest != policyID ||
		!selectionValueClean(check, memorySecrets(s.settings, s.secret)) {
		return skills.OutcomeSupervisionCheck{}, ErrInspection
	}
	return check, nil
}

// DurableOutcomeSupervisionStep reserves one exact activation pair before it
// reads evidence. The durable check, selected-evidence checkpoint, outcome
// receipt, and structural journal let a later process reconcile every boundary
// without rediscovering a different activation or repeating adjudication.
func (s *Service) DurableOutcomeSupervisionStep(ctx context.Context) (out skills.OutcomeSupervisionState, err error) {
	defer func() {
		if recover() != nil {
			out, err = skills.OutcomeSupervisionState{}, ErrAdmission
		}
	}()
	if ctx == nil || ctx.Err() != nil || !s.outcomeSupervisionConfigured() {
		return out, ErrAdmission
	}
	interval, err := configuredOutcomeSupervisionInterval(s)
	if err != nil {
		return out, err
	}
	policyID, err := s.outcomeSupervisionPolicyDigest(interval)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	root, scope := s.settings.Skills.Root, s.settings.Skills.Scope
	secrets := memorySecrets(s.settings, s.secret)
	identities := []string{scope, configuredOutcomeSupervisorName, policyID}
	if !selectionValueClean(identities, secrets) {
		return out, ErrAdmission
	}
	read, err := skills.OpenReadOnly(root, []string{scope})
	if err != nil {
		return out, ErrAdmission
	}
	defer read.Close()
	store, err := skills.Open(root, []string{scope})
	if err != nil {
		return out, ErrAdmission
	}
	defer store.Close()
	store.SetAutomatic(true)
	store.SetOutcomeRollback(true)
	guard := skills.OutcomeSupervisionGuard(func(call context.Context, state skills.OutcomeSupervisionState, check skills.OutcomeSupervisionCheck) error {
		secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
		current, policyErr := s.outcomeSupervisionPolicyDigest(interval)
		if call.Err() != nil || policyErr != nil || current != policyID || state.Scope != scope ||
			state.Name != configuredOutcomeSupervisorName || state.PolicyDigest != policyID || state.Interval != interval ||
			!selectionValueClean(identities, secrets) || !selectionValueClean(state, secrets) || !selectionValueClean(check, secrets) {
			return ErrAdmission
		}
		return nil
	})
	out, check, err := store.PrepareOutcomeSupervision(ctx, scope, configuredOutcomeSupervisorName, policyID, interval, guard)
	if err != nil || out.Validate() != nil || guard(ctx, out, check) != nil {
		return skills.OutcomeSupervisionState{}, ErrAdmission
	}
	if check.CheckID == "" {
		return out, nil
	}
	if check.Validate() != nil || check.Status != "pending" || check.Candidate.Current.Key.Scope != scope {
		return skills.OutcomeSupervisionState{}, ErrAdmission
	}
	request, err := s.outcomeSupervisionRequest(check.Candidate)
	if err != nil {
		return out, s.failOutcomeSupervisionCheck(ctx, store, check, policyID, guard)
	}
	selectionPolicy, err := s.skillComparisonSelectionPolicy(request)
	if err != nil {
		return out, s.failOutcomeSupervisionCheck(ctx, store, check, policyID, guard)
	}
	operationID := outcomeSupervisionOperationID(check.Candidate.Current, selectionPolicy)
	if check.OutcomeOperationID == "" {
		check, err = store.BindOutcomeSupervisionOperation(ctx, check, operationID, guard)
		if err != nil {
			return out, ErrAdmission
		}
	} else if check.OutcomeOperationID != operationID {
		return out, ErrAdmission
	}
	if receipt, lookupErr := read.OutcomeRollbackOperation(ctx, check.Candidate.Current.Key, operationID); lookupErr == nil {
		return s.settleDurableOutcomeSupervision(ctx, store, check, policyID, receipt, guard)
	} else if !errors.Is(lookupErr, skills.ErrNotFound) {
		return out, ErrAdmission
	}
	if reconciled, state, reconcileErr := s.reconcileOutcomeSupervisionJournal(ctx, store, check, policyID, guard); reconcileErr != nil {
		return out, reconcileErr
	} else if reconciled {
		return state, nil
	}
	current, currentErr := read.ActivationState(ctx, check.Candidate.Current.Key)
	if currentErr != nil || current != check.Candidate.Current {
		if journalErr := s.recordOutcomeSupervisionEvent(ctx, check, policyID, telemetry.OutcomeSupervisionNoAction); journalErr != nil {
			return out, ErrAdmission
		}
		return store.CompleteOutcomeSupervisionCheck(ctx, check, skills.OutcomeSupervisionCompletion{Code: "stale_activation", OutcomeOperationID: operationID}, guard)
	}
	var readiness OutcomeRollbackReadiness
	checkpoint, checkpointErr := read.OutcomeSelectionCheckpoint(ctx, check.Candidate.Current.Key, operationID)
	if checkpointErr == nil {
		readiness = OutcomeRollbackReadiness{Version: 1, Status: "ready", OperationID: operationID, Candidate: check.Candidate, Selection: checkpoint.Report}
		if readiness.Validate() != nil {
			return out, ErrAdmission
		}
	} else if errors.Is(checkpointErr, skills.ErrNotFound) {
		readiness, err = s.inspectOutcomeRollbackCandidate(ctx, check.Candidate)
		if err != nil {
			return out, s.failOutcomeSupervisionCheck(ctx, store, check, policyID, guard)
		}
	} else {
		return out, s.failOutcomeSupervisionCheck(ctx, store, check, policyID, guard)
	}
	if readiness.OperationID != operationID {
		return out, s.failOutcomeSupervisionCheck(ctx, store, check, policyID, guard)
	}
	if readiness.Status == "waiting" {
		if err = s.recordOutcomeSupervisionEvent(ctx, check, policyID, telemetry.OutcomeSupervisionWaiting); err != nil {
			return out, ErrAdmission
		}
		return store.CompleteOutcomeSupervisionCheck(ctx, check, skills.OutcomeSupervisionCompletion{Code: "waiting"}, guard)
	}
	if err = s.recordOutcomeSupervisionEvent(ctx, check, policyID, telemetry.OutcomeSupervisionReady); err != nil {
		return out, ErrAdmission
	}
	receipt, err := s.OutcomeRollbackPrepared(ctx, operationID, check.Candidate.Current, request, readiness.Selection)
	if err != nil {
		// The action may have committed before its acknowledgement was lost.
		if receipt, lookupErr := read.OutcomeRollbackOperation(ctx, check.Candidate.Current.Key, operationID); lookupErr == nil {
			return s.settleDurableOutcomeSupervision(ctx, store, check, policyID, receipt, guard)
		}
		return out, s.failOutcomeSupervisionCheck(ctx, store, check, policyID, guard)
	}
	return s.settleDurableOutcomeSupervision(ctx, store, check, policyID, receipt, guard)
}

func configuredOutcomeSupervisionInterval(s *Service) (time.Duration, error) {
	if s == nil {
		return 0, ErrAdmission
	}
	interval, err := time.ParseDuration(s.settings.Skills.OutcomeRollbackSupervisor.Interval)
	if err != nil || interval < time.Second || interval > 24*time.Hour {
		return 0, ErrAdmission
	}
	return interval, nil
}

func (s *Service) outcomeSupervisionPolicyDigest(interval time.Duration) (string, error) {
	if !s.outcomeSupervisionConfigured() {
		return "", ErrAdmission
	}
	body, err := json.Marshal(struct {
		Version   int
		Mode      string
		Scope     string
		LocalOnly bool
		Interval  time.Duration
		Policy    any
	}{1, s.settings.Mode, s.settings.Skills.Scope, s.settings.Skills.LocalOnly, interval, s.settings.Skills.OutcomeRollbackSupervisor})
	if err != nil {
		return "", ErrAdmission
	}
	digest := sha256.Sum256(append([]byte("darwin-outcome-supervision-policy-v1\x00"), body...))
	return hex.EncodeToString(digest[:]), nil
}

func (s *Service) recordOutcomeSupervisionEvent(ctx context.Context, check skills.OutcomeSupervisionCheck, policyID string, code telemetry.OutcomeSupervisionCode) error {
	db, err := telemetry.Open(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.RecordOutcomeSupervisionEvent(ctx, telemetry.OutcomeSupervisionEventRequest{Version: 1,
		OperationID: check.OutcomeOperationID, CheckID: check.CheckID, Code: code,
		SkillScope: check.Candidate.Current.Key.Scope, SkillName: check.Candidate.Current.Key.Name,
		ActivationID: check.Candidate.Current.Active, ActivationRevision: check.Candidate.Current.Revision, PolicyID: policyID})
	return err
}

func (s *Service) reconcileOutcomeSupervisionJournal(ctx context.Context, store *skills.FileStore, check skills.OutcomeSupervisionCheck, policyID string, guard skills.OutcomeSupervisionGuard) (bool, skills.OutcomeSupervisionState, error) {
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return false, skills.OutcomeSupervisionState{}, ErrAdmission
	}
	defer db.Close()
	page, err := db.OutcomeSupervisionEvents(ctx, check.OutcomeOperationID, 0, 10)
	if err != nil || page.HasMore {
		return false, skills.OutcomeSupervisionState{}, ErrAdmission
	}
	for i := len(page.Items) - 1; i >= 0; i-- {
		event := page.Items[i]
		if event.CheckID != check.CheckID || event.PolicyID != policyID {
			continue
		}
		var completion skills.OutcomeSupervisionCompletion
		switch event.Code {
		case telemetry.OutcomeSupervisionWaiting:
			completion = skills.OutcomeSupervisionCompletion{Code: "waiting"}
		case telemetry.OutcomeSupervisionNoAction:
			completion = skills.OutcomeSupervisionCompletion{Code: "stale_activation", OutcomeOperationID: check.OutcomeOperationID}
		case telemetry.OutcomeSupervisionError:
			completion = skills.OutcomeSupervisionCompletion{Code: "check_failed", OutcomeOperationID: check.OutcomeOperationID}
		case telemetry.OutcomeSupervisionRolledBack:
			return false, skills.OutcomeSupervisionState{}, ErrAdmission
		default:
			return false, skills.OutcomeSupervisionState{}, nil
		}
		state, completeErr := store.CompleteOutcomeSupervisionCheck(ctx, check, completion, guard)
		if completeErr != nil {
			return false, skills.OutcomeSupervisionState{}, ErrAdmission
		}
		return true, state, nil
	}
	return false, skills.OutcomeSupervisionState{}, nil
}

func (s *Service) settleDurableOutcomeSupervision(ctx context.Context, store *skills.FileStore, check skills.OutcomeSupervisionCheck, policyID string, receipt skills.OutcomeRollbackReceipt, guard skills.OutcomeSupervisionGuard) (skills.OutcomeSupervisionState, error) {
	code := telemetry.OutcomeSupervisionNoAction
	if receipt.Validate() != nil || receipt.OperationID != check.OutcomeOperationID || receipt.Expected != check.Candidate.Current {
		return skills.OutcomeSupervisionState{}, ErrAdmission
	}
	if receipt.Decision == "rolled_back" {
		code = telemetry.OutcomeSupervisionRolledBack
	} else if receipt.Decision != "no_action" {
		return skills.OutcomeSupervisionState{}, ErrAdmission
	}
	if err := s.recordOutcomeSupervisionEvent(ctx, check, policyID, code); err != nil {
		return skills.OutcomeSupervisionState{}, ErrAdmission
	}
	return store.CompleteOutcomeSupervisionCheck(ctx, check, skills.OutcomeSupervisionCompletion{Code: "evaluated", OutcomeOperationID: check.OutcomeOperationID}, guard)
}

func (s *Service) failOutcomeSupervisionCheck(ctx context.Context, store *skills.FileStore, check skills.OutcomeSupervisionCheck, policyID string, guard skills.OutcomeSupervisionGuard) error {
	if check.OutcomeOperationID == "" {
		return ErrAdmission
	}
	if err := s.recordOutcomeSupervisionEvent(ctx, check, policyID, telemetry.OutcomeSupervisionError); err != nil {
		return ErrAdmission
	}
	_, _ = store.CompleteOutcomeSupervisionCheck(ctx, check, skills.OutcomeSupervisionCompletion{Code: "check_failed", OutcomeOperationID: check.OutcomeOperationID}, guard)
	return ErrAdmission
}
