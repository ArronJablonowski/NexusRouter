package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/health"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

// OutcomeRollbackReadiness is a read-only observation for one activation.
// Ready means both eligible cohorts meet policy; it is not rollback authority.
type OutcomeRollbackReadiness struct {
	Version     int                              `json:"version"`
	Status      string                           `json:"status"`
	OperationID string                           `json:"operation_id"`
	Candidate   skills.OutcomeRollbackCandidate  `json:"candidate"`
	Selection   skills.ComparisonSelectionReport `json:"selection"`
}

func (r OutcomeRollbackReadiness) Validate() error {
	if r.Version != 1 || (r.Status != "waiting" && r.Status != "ready") || !skillGenerationIdentifier.MatchString(r.OperationID) ||
		r.Candidate.Validate() != nil || r.Selection.Validate() != nil || r.Selection.Policy.Comparison.Key != r.Candidate.Current.Key ||
		r.Selection.Policy.Comparison.BaselineVersion != r.Candidate.Predecessor || r.Selection.Policy.Comparison.CandidateVersion != r.Candidate.Current.Active ||
		outcomeSupervisionOperationID(r.Candidate.Current, r.Selection.Policy) != r.OperationID {
		return ErrInspection
	}
	ready := r.Selection.Comparison != nil && r.Selection.Comparison.Baseline.Samples >= r.Selection.Policy.Comparison.MinSamples &&
		r.Selection.Comparison.Candidate.Samples >= r.Selection.Policy.Comparison.MinSamples
	if ready != (r.Status == "ready") {
		return ErrInspection
	}
	return nil
}

// OutcomeRollbackCandidate inspects the current activation and its immediate
// validated predecessor without creating or repairing catalog state.
func (s *Service) OutcomeRollbackCandidate(ctx context.Context, key skills.Key) (out skills.OutcomeRollbackCandidate, err error) {
	defer func() {
		if recover() != nil {
			out, err = skills.OutcomeRollbackCandidate{}, ErrInspection
		}
	}()
	if ctx == nil || ctx.Err() != nil || !s.skillActivationConfigured(key) {
		return out, ErrAdmission
	}
	store, err := skills.OpenReadOnly(s.settings.Skills.Root, []string{key.Scope})
	if err != nil {
		return out, ErrInspection
	}
	defer store.Close()
	out, err = store.OutcomeRollbackCandidate(ctx, key)
	if err != nil || ctx.Err() != nil || out.Validate() != nil || out.Current.Key != key || !selectionValueClean(out, memorySecrets(s.settings, s.secret)) {
		return skills.OutcomeRollbackCandidate{}, ErrInspection
	}
	return out, nil
}

// InspectOutcomeRollbackReadiness selects a fresh bounded comparison snapshot.
// Waiting is successful inspection, including empty or exclusion-heavy windows.
// This method never creates an outcome intent or selection checkpoint.
func (s *Service) InspectOutcomeRollbackReadiness(ctx context.Context, key skills.Key) (out OutcomeRollbackReadiness, err error) {
	defer func() {
		if recover() != nil {
			out, err = OutcomeRollbackReadiness{}, ErrInspection
		}
	}()
	if ctx == nil || ctx.Err() != nil || !s.outcomeSupervisionConfigured() {
		return out, ErrAdmission
	}
	candidate, err := s.OutcomeRollbackCandidate(ctx, key)
	if err != nil {
		return out, err
	}
	return s.inspectOutcomeRollbackCandidate(ctx, candidate)
}

// inspectOutcomeRollbackCandidate evaluates the exact activation pair already
// reserved by a durable supervisor check. It deliberately does not rediscover
// the current activation, so a restart cannot silently change the comparison.
func (s *Service) inspectOutcomeRollbackCandidate(ctx context.Context, candidate skills.OutcomeRollbackCandidate) (out OutcomeRollbackReadiness, err error) {
	if ctx == nil || ctx.Err() != nil || candidate.Validate() != nil || !s.outcomeSupervisionConfigured() {
		return out, ErrAdmission
	}
	request, err := s.outcomeSupervisionRequest(candidate)
	if err != nil {
		return out, err
	}
	selection, err := s.SelectSkillComparison(ctx, request)
	if err != nil {
		return out, err
	}
	out = OutcomeRollbackReadiness{Version: 1, Status: "waiting", OperationID: outcomeSupervisionOperationID(candidate.Current, selection.Policy), Candidate: candidate, Selection: selection}
	if selection.Comparison != nil && selection.Comparison.Baseline.Samples >= request.MinSamples && selection.Comparison.Candidate.Samples >= request.MinSamples {
		out.Status = "ready"
	}
	if out.Validate() != nil || ctx.Err() != nil || !selectionValueClean(out, memorySecrets(s.settings, s.secret)) {
		return OutcomeRollbackReadiness{}, ErrInspection
	}
	return out, nil
}

func (s *Service) outcomeSupervisionRequest(candidate skills.OutcomeRollbackCandidate) (skills.ComparisonSelectionRequest, error) {
	if !s.outcomeSupervisionConfigured() || candidate.Validate() != nil || candidate.Current.Key.Scope != s.settings.Skills.Scope {
		return skills.ComparisonSelectionRequest{}, ErrAdmission
	}
	p := s.settings.Skills.OutcomeRollbackSupervisor
	request := skills.ComparisonSelectionRequest{Version: 1, ModelID: p.ModelID, Domain: p.Domain, Profile: p.Profile, Name: candidate.Current.Key.Name,
		BaselineVersion: candidate.Predecessor, CandidateVersion: candidate.Current.Active, Source: evaluation.Source(p.Source),
		MinSamples: p.MinSamples, MinDrop: p.MinDrop, Privacy: p.Privacy, TasksPerVersion: p.TasksPerVersion}
	if request.Validate() != nil {
		return skills.ComparisonSelectionRequest{}, ErrAdmission
	}
	return request, nil
}

func (s *Service) outcomeSupervisionConfigured() bool {
	return s != nil && s.settings.Validate() == nil && s.settings.Skills.OutcomeRollbackSupervisor.Enabled && s.settings.Skills.Enabled &&
		s.settings.Skills.Rollback && s.settings.Skills.OutcomeRollback && s.settings.Skills.Root != "" && s.settings.Skills.Scope != "" && s.skillStore == nil
}

func outcomeSupervisionOperationID(expected skills.ActivationState, policy skills.ComparisonSelectionPolicy) string {
	body, err := json.Marshal(struct {
		Version  int                              `json:"version"`
		Revision string                           `json:"activation_revision"`
		Policy   skills.ComparisonSelectionPolicy `json:"policy"`
	}{1, expected.Revision, policy})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(append([]byte("outcome-supervision-operation-v1\x00"), body...))
	return hex.EncodeToString(sum[:])
}

// OutcomeSupervisionStep advances one lexical active-skill cursor. Ineligible
// first activations are skipped. A ready selection is handed to the atomic
// prepared path; waiting remains entirely read-only.
func (s *Service) OutcomeSupervisionStep(ctx context.Context, after string) (next string, readiness OutcomeRollbackReadiness, err error) {
	next = after
	defer func() {
		if recover() != nil {
			next, readiness, err = after, OutcomeRollbackReadiness{}, ErrAdmission
		}
	}()
	if ctx == nil || ctx.Err() != nil || !s.outcomeSupervisionConfigured() || (after != "" && !skillGenerationIdentifier.MatchString(after)) {
		return next, readiness, ErrAdmission
	}
	store, err := skills.OpenReadOnly(s.settings.Skills.Root, []string{s.settings.Skills.Scope})
	if err != nil {
		return next, readiness, ErrAdmission
	}
	states, readErr := store.ActiveStates(ctx, s.settings.Skills.Scope, after, 1)
	closeErr := store.Close()
	if readErr != nil || closeErr != nil || len(states) > 1 || ctx.Err() != nil {
		return next, readiness, ErrAdmission
	}
	if len(states) == 0 {
		return "", readiness, nil
	}
	next = states[0].Key.Name
	readiness, err = s.InspectOutcomeRollbackReadiness(ctx, states[0].Key)
	if err != nil {
		// A structurally sound active skill without an eligible predecessor is a
		// normal scan result, not an operational supervisor failure.
		probe, openErr := skills.OpenReadOnly(s.settings.Skills.Root, []string{s.settings.Skills.Scope})
		if openErr == nil {
			_, candidateErr := probe.OutcomeRollbackCandidate(ctx, states[0].Key)
			_ = probe.Close()
			if errors.Is(candidateErr, skills.ErrConflict) || errors.Is(candidateErr, skills.ErrValidation) {
				return next, OutcomeRollbackReadiness{}, nil
			}
		}
		return next, OutcomeRollbackReadiness{}, err
	}
	if readiness.Status == "waiting" {
		return next, readiness, nil
	}
	request, requestErr := s.outcomeSupervisionRequest(readiness.Candidate)
	if requestErr != nil {
		return next, OutcomeRollbackReadiness{}, requestErr
	}
	if _, err = s.OutcomeRollbackPrepared(ctx, readiness.OperationID, readiness.Candidate.Current, request, readiness.Selection); err != nil {
		return next, readiness, err
	}
	return next, readiness, nil
}

type OutcomeSupervisionMonitor struct {
	mu          sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
	err         error
	status      string
	code        string
	stepStarted time.Time
}

// StartOutcomeSupervision starts an immediate pass and then one pass per
// configured interval. The caller owns the returned monitor and must Close it.
func StartOutcomeSupervision(ctx context.Context, s *Service) (*OutcomeSupervisionMonitor, error) {
	if ctx == nil || ctx.Err() != nil || !s.outcomeSupervisionConfigured() {
		return nil, ErrAdmission
	}
	interval, err := config.Duration(s.settings.Skills.OutcomeRollbackSupervisor.Interval)
	if err != nil || interval < time.Second || interval > 24*time.Hour {
		return nil, ErrAdmission
	}
	owned, cancel := context.WithCancel(ctx)
	m := &OutcomeSupervisionMonitor{cancel: cancel, done: make(chan struct{}), status: "unknown", code: "supervisor_starting"}
	go m.run(owned, s, interval)
	return m, nil
}

func (m *OutcomeSupervisionMonitor) run(ctx context.Context, s *Service, interval time.Duration) {
	defer close(m.done)
	defer func() {
		if recover() != nil {
			m.mu.Lock()
			m.err = ErrAdmission
			m.mu.Unlock()
		}
		m.mu.Lock()
		m.stepStarted = time.Time{}
		m.status, m.code = "unavailable", "supervisor_stopped"
		m.mu.Unlock()
	}()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		m.mu.Lock()
		m.stepStarted = time.Now()
		m.mu.Unlock()
		_, err := s.DurableOutcomeSupervisionStep(ctx)
		if ctx.Err() != nil {
			return
		}
		m.mu.Lock()
		m.stepStarted = time.Time{}
		if err != nil {
			m.err = ErrAdmission
			m.status, m.code = "degraded", "supervisor_error"
		} else {
			m.status, m.code = "healthy", "supervisor_ok"
		}
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *OutcomeSupervisionMonitor) Close() error {
	if m == nil || m.done == nil {
		return nil
	}
	m.cancel()
	<-m.done
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

func (m *OutcomeSupervisionMonitor) Health() health.Check {
	out := health.Check{Component: "outcome_supervision", Status: "unknown", Code: "supervisor_starting"}
	if m == nil || m.done == nil {
		return out
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out.Status, out.Code = m.status, m.code
	if !m.stepStarted.IsZero() && time.Since(m.stepStarted) > 10*time.Second {
		out.Status, out.Code = "degraded", "supervisor_stalled"
	}
	return out
}
