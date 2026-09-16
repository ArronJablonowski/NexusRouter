package app

import (
	"context"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/resources"
)

// RunExplicit is a one-shot execution without automatic review or fallback.
// Concurrent callers must reuse a Service to share resource reservations.
func RunExplicit(ctx context.Context, cfg config.Settings, r Request, secret func(string) string) (Result, error) {
	var classifyErr error
	r, classifyErr = classifyRequestIntent(r)
	if classifyErr != nil {
		return Result{}, classifyErr
	}
	svc, err := NewService(cfg, secret)
	if err != nil {
		return Result{}, ErrAdmission
	}
	select {
	case svc.execution <- struct{}{}:
		defer func() { <-svc.execution }()
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	return svc.runWithPressure(ctx, r, svc.runExplicit)
}

func (s *Service) runExplicit(ctx context.Context, r Request) (result Result, runErr error) {
	var classifyErr error
	r, classifyErr = classifyRequestIntent(r)
	if classifyErr != nil {
		return Result{}, classifyErr
	}
	if s == nil || s.settings.Validate() != nil || validateInput(r) != nil || ctx.Err() != nil || validateRuntimeHostStore(ctx, s.settings, r) != nil {
		return Result{}, ErrAdmission
	}
	r.providerFactory = s.providerFactory
	r.codexLauncher = s.codexLauncher
	r.contextEstimator = s.contextEstimator
	r.contextEngine = s.contextEngine
	r = s.bindToolExtension(r)
	var model config.Model
	for _, m := range s.settings.Models {
		if m.ID == r.ModelID {
			model = m
			break
		}
	}
	if model.ID == "" || (r.LocalRequired && model.Locality != "local") || (s.settings.Mode == "local_only" && model.Locality != "local") || (s.settings.Mode == "cloud_only" && model.Locality != "cloud") {
		return Result{}, ErrAdmission
	}
	if r.ContextTokens > 0 && model.ContextTokens < r.ContextTokens {
		return Result{}, ErrAdmission
	}
	if (r.Compaction != nil || r.SummaryAttemptID != "") && model.ContextTokens < 1 {
		return Result{}, ErrAdmission
	}
	if r.MaxCost > 0 && (model.EstimatedCost == nil || *model.EstimatedCost > r.MaxCost) {
		return Result{}, ErrAdmission
	}
	for _, required := range r.Capabilities {
		matched := false
		for _, capability := range model.Capabilities {
			matched = matched || required == capability
		}
		if !matched {
			return Result{}, ErrAdmission
		}
	}
	if r.delegatedParent == "" && (s.settings.Tools.Enabled || s.settings.Tools.WorkboardReadEnabled || len(r.toolExtension.Names()) > 0) && (model.Locality != "local" || model.ContextTokens == 0) {
		return Result{}, ErrAdmission
	}
	if r.delegatedParent == "" && s.settings.Workers.DelegateModel != "" && model.ContextTokens == 0 {
		return Result{}, ErrAdmission
	}
	if r.runtimeHostAdmission != nil {
		for _, provider := range s.settings.Providers {
			if provider.ID == model.Provider && provider.ManageResidency {
				return Result{}, ErrAdmission
			}
		}
	}
	var err error
	r, err = s.prepareExplicitApprovedCompaction(ctx, r, model)
	if err != nil {
		return Result{}, ErrAdmission
	}
	// Managed residency is an admission-time maintenance action. Reject known
	// credential/source failures before any such provider mutation.
	for _, provider := range s.settings.Providers {
		if provider.ID == model.Provider && provider.ManageResidency {
			if provider.APIKeyEnv != "" && (s.secret == nil || s.secret(provider.APIKeyEnv) == "") {
				return Result{}, ErrAdmission
			}
		}
	}
	// A durable local reservation must bind the same session identity that the
	// eventual TaskStarted event will use. Resolve continuation lineage before
	// capacity admission instead of discovering it after a claim is held.
	if model.Locality == "local" && r.ContinueTaskID != "" && r.continuation == nil {
		db, openErr := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
		if openErr != nil {
			return Result{}, ErrAdmission
		}
		r.continuation, err = loadContinuation(ctx, db, r, memorySecrets(s.settings, s.secret))
		closeErr := db.Close()
		if err != nil || closeErr != nil {
			return Result{}, ErrAdmission
		}
	}
	if r.continuation != nil {
		if r.sessionID != "" && r.sessionID != r.continuation.SessionID {
			return Result{}, ErrAdmission
		}
		r.sessionID = r.continuation.SessionID
	}
	admission := ctx
	if r.admissionContext != nil {
		admission = r.admissionContext
	}
	if model.Locality == "local" {
		if model.RAMBytes == 0 {
			return Result{}, ErrAdmission
		}
		reservedContext, release, reserveErr := s.reservePrimary(ctx, admission, model, r)
		if reserveErr != nil {
			return Result{}, errors.Join(ErrAdmission, reserveErr)
		}
		ctx = reservedContext
		defer func() {
			if releaseErr := release(); releaseErr != nil {
				result = Result{}
				runErr = errors.Join(runErr, releaseErr)
			}
		}()
	}
	if ctx.Err() != nil || admission.Err() != nil {
		return Result{}, ErrAdmission
	}
	// A Workboard host-bound execution is already a bounded child capability.
	// It must not inherit the service's ordinary recursive delegation surface.
	if r.delegatedParent == "" && r.runtimeHostAdmission == nil {
		r.delegate = s.bindDelegate(r)
		r.delegateAudit = s.bindDelegationAudit()
	}
	return runExplicitAdmitted(ctx, s.settings, r, s.secret)
}

func (s *Service) reserveExplicit(ctx context.Context, model config.Model) (release func(), err error) {
	return s.reserveAuxiliary(ctx, model)
}

func (s *Service) reserveExplicitLocal(ctx context.Context, model config.Model) (release func(), err error) {
	for _, provider := range s.settings.Providers {
		if provider.ID == model.Provider && provider.ManageResidency {
			return s.reserveManagedResidency(ctx, provider, model)
		}
	}
	return s.reserveUnmanaged(ctx, model)
}

func (s *Service) reserveUnmanaged(ctx context.Context, model config.Model) (release func(), err error) {
	if err := s.lockResources(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	defer func() {
		if recover() != nil {
			release = nil
			err = ErrAdmission
		}
	}()
	if s.profile == nil || s.budget == nil {
		return nil, ErrAdmission
	}
	snapshot, err := s.resourceProfile(ctx)
	if err != nil {
		return nil, ErrAdmission
	}
	return s.budget.Reserve(snapshot, modelResources(model), time.Now())
}

func modelResources(model config.Model) resources.Need {
	return resources.Need{RAM: model.RAMBytes, VRAM: model.VRAMBytes, Device: model.GPUDevice}
}

// lockResources lets admission expire while another request owns the profiler.
func (s *Service) lockResources(ctx context.Context) error {
	if ctx.Err() != nil {
		return ErrAdmission
	}
	if s.mu.TryLock() {
		return nil
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ErrAdmission
		case <-ticker.C:
			if s.mu.TryLock() {
				if ctx.Err() != nil {
					s.mu.Unlock()
					return ErrAdmission
				}
				return nil
			}
		}
	}
}

func (s *Service) resourceProfile(ctx context.Context) (snapshot resources.Snapshot, err error) {
	defer func() {
		if recover() != nil {
			snapshot = resources.Snapshot{}
			err = ErrAdmission
		}
	}()
	query, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if s.profile == nil {
		return snapshot, ErrAdmission
	}
	snapshot, err = s.profile(query)
	if err != nil || query.Err() != nil {
		return resources.Snapshot{}, ErrAdmission
	}
	return snapshot, nil
}
