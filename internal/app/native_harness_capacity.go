package app

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/resources"
)

// NativeHarnessCapacity measures the same context-scaled model plus fixed
// harness overhead used by admission. It observes this service's live budget;
// it does not reserve, load/unload models, construct providers or read secrets.
// Even a cloud harness requires its local process overhead. This is capacity
// only: deployment mode, credentials and capabilities remain separate gates.
func (s *Service) NativeHarnessCapacity(ctx context.Context, modelID, registration string, tokens int) (identity harness.Identity, need resources.Need, capacity resources.CapacityResult, err error) {
	defer func() {
		if recover() != nil {
			identity = harness.Identity{}
			need = resources.Need{}
			capacity = resources.CapacityResult{}
			err = ErrAdmission
		}
	}()
	if s == nil || ctx == nil || ctx.Err() != nil {
		return identity, need, capacity, ErrAdmission
	}
	if registration == harness.DirectRegistration(modelID) {
		return s.directCapacity(ctx, modelID, tokens)
	}
	identity, err = s.NativeHarnessIdentity(modelID, registration, tokens)
	if err != nil {
		return
	}
	entry := s.nativeHarnesses[registration]
	var model config.Model
	for _, m := range s.settings.Models {
		if m.ID == modelID {
			model = m
			break
		}
	}
	if model.Locality == "local" && model.RAMBytes == 0 {
		return identity, need, capacity, ErrAdmission
	}
	sized, e := nativeReservationModel(model, &entry, tokens)
	if e != nil {
		return identity, need, capacity, e
	}
	need = resources.Need{RAM: sized.RAMBytes, VRAM: sized.VRAMBytes, Device: sized.GPUDevice}
	if resources.ValidateNeed(need) != nil {
		return identity, need, capacity, ErrAdmission
	}
	if err = s.lockResources(ctx); err != nil {
		return
	}
	defer s.mu.Unlock()
	if s.budget == nil {
		return identity, need, capacity, ErrAdmission
	}
	snapshot, e := s.resourceProfile(ctx)
	if e != nil {
		return identity, need, capacity, e
	}
	capacity, err = s.budget.Plan(ctx, resources.CapacityRequest{Version: 1, Snapshot: snapshot, Need: need, Now: s.routingNow()})
	if err == nil && capacity.Validate() != nil {
		err = ErrAdmission
	}
	return
}
