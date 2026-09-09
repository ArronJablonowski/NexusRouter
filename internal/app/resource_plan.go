package app

import (
	"context"
	"errors"

	"github.com/ArronJablonowski/DarwinRouter/resources"
)

const (
	ResourceActionLocal   = "local"
	ResourceActionQueue   = "queue"
	ResourceActionOffload = "offload"
	ResourceActionReject  = "reject"
)

type ResourcePlanRequest struct {
	Need          resources.Need
	LocalRequired bool
}

// ResourcePlanResult is advisory only. Capacity is empty when cloud-only mode
// resolves the action without measuring the host.
type ResourcePlanResult struct {
	Capacity resources.CapacityResult
	Action   string
	Reason   string
}

// ResourcePlan reports the action the current deployment policy would take for
// one local workload. It observes live reservations but never creates one.
func (s *Service) ResourcePlan(ctx context.Context, request ResourcePlanRequest) (result ResourcePlanResult, err error) {
	bad := func(cause error) (ResourcePlanResult, error) {
		if cause != nil {
			return ResourcePlanResult{}, errors.Join(ErrAdmission, cause)
		}
		return ResourcePlanResult{}, ErrAdmission
	}
	defer func() {
		if recover() != nil {
			result, err = ResourcePlanResult{}, ErrAdmission
		}
	}()
	if s == nil || ctx == nil || s.settings.Validate() != nil || resources.ValidateNeed(request.Need) != nil {
		return bad(nil)
	}
	if ctx.Err() != nil {
		return bad(ctx.Err())
	}
	if s.settings.Mode == "cloud_only" {
		if request.LocalRequired {
			return ResourcePlanResult{Action: ResourceActionReject, Reason: "local_required"}, nil
		}
		return ResourcePlanResult{Action: ResourceActionOffload, Reason: "cloud_only"}, nil
	}
	if err := s.lockResources(ctx); err != nil {
		if ctx.Err() != nil {
			return bad(ctx.Err())
		}
		return bad(nil)
	}
	defer s.mu.Unlock()
	if s.budget == nil {
		return bad(nil)
	}
	snapshot, profileErr := s.resourceProfile(ctx)
	if profileErr != nil {
		if ctx.Err() != nil {
			return bad(ctx.Err())
		}
		return bad(nil)
	}
	now := s.routingNow()
	capacity, planErr := s.budget.Plan(ctx, resources.CapacityRequest{Version: resources.CapacityContractVersion, Snapshot: snapshot, Need: request.Need, Now: now})
	if planErr != nil || capacity.Validate() != nil {
		if ctx.Err() != nil {
			return bad(ctx.Err())
		}
		return bad(nil)
	}
	result.Capacity, result.Reason = capacity, capacity.Reason
	if capacity.Action == resources.CapacityAdmit {
		result.Action = ResourceActionLocal
		return result, nil
	}
	if s.settings.Mode == "hybrid" && !request.LocalRequired {
		result.Action = ResourceActionOffload
	} else if s.settings.Hardware.LocalPressurePolicy == "wait" {
		result.Action = ResourceActionQueue
	} else {
		result.Action = ResourceActionReject
	}
	return result, nil
}
