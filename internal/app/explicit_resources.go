package app

import (
	"context"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/resources"
)

// RunExplicit is a one-shot execution without automatic review or fallback.
// Concurrent callers must reuse a Service to share resource reservations.
func RunExplicit(ctx context.Context, cfg config.Settings, r Request, secret func(string) string) (Result, error) {
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

func (s *Service) runExplicit(ctx context.Context, r Request) (Result, error) {
	if s == nil || s.settings.Validate() != nil || s.settings.Telemetry.OTEL || validateInput(r) != nil || ctx.Err() != nil {
		return Result{}, ErrAdmission
	}
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
	if r.delegatedParent == "" && s.settings.Tools.Enabled && (model.Locality != "local" || model.ContextTokens == 0) {
		return Result{}, ErrAdmission
	}
	if r.delegatedParent == "" && s.settings.Workers.DelegateModel != "" && model.ContextTokens == 0 {
		return Result{}, ErrAdmission
	}
	admission := ctx
	if r.admissionContext != nil {
		admission = r.admissionContext
	}
	if model.Locality == "local" {
		if model.RAMBytes == 0 {
			return Result{}, ErrAdmission
		}
		release, err := s.reserveExplicit(admission, model)
		if err != nil {
			return Result{}, errors.Join(ErrAdmission, err)
		}
		defer release()
	}
	if ctx.Err() != nil || admission.Err() != nil {
		return Result{}, ErrAdmission
	}
	if r.delegatedParent == "" {
		r.delegate = s.bindDelegate(r)
	}
	return runExplicitAdmitted(ctx, s.settings, r, s.secret)
}

func (s *Service) reserveExplicit(ctx context.Context, model config.Model) (release func(), err error) {
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
