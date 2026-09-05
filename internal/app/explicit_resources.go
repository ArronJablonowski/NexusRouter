package app

import (
	"context"
	"time"

	"darwinrouter/internal/config"
	"darwinrouter/resources"
)

// RunExplicit is a one-shot execution without automatic review or fallback.
// Concurrent callers must reuse a Service to share resource reservations.
func RunExplicit(ctx context.Context, cfg config.Settings, r Request, secret func(string) string) (Result, error) {
	svc, err := NewService(cfg, secret)
	if err != nil {
		return Result{}, ErrAdmission
	}
	return svc.runExplicit(ctx, r)
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
	if s.settings.Tools.Enabled && (model.Locality != "local" || model.ContextTokens == 0) {
		return Result{}, ErrAdmission
	}
	if model.Locality == "local" {
		if model.RAMBytes == 0 {
			return Result{}, ErrAdmission
		}
		release, err := s.reserveExplicit(ctx, model)
		if err != nil {
			return Result{}, ErrAdmission
		}
		defer release()
	}
	if ctx.Err() != nil {
		return Result{}, ErrAdmission
	}
	return runExplicitAdmitted(ctx, s.settings, r, s.secret)
}

func (s *Service) reserveExplicit(ctx context.Context, model config.Model) (release func(), err error) {
	s.mu.Lock()
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
	return s.budget.Reserve(snapshot, resources.Need{RAM: model.RAMBytes, VRAM: model.VRAMBytes}, time.Now())
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
