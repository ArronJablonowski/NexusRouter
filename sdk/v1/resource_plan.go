package v1

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/resources"
)

// ResourcePlanAction is a conservative recommendation, not a reservation or
// authorization. Execution must still pass normal routing, privacy and budget
// admission against a fresh measurement.
type ResourcePlanAction string

const (
	ResourcePlanContractVersion                    = 1
	ResourcePlanExecuteLocal    ResourcePlanAction = "execute_local"
	ResourcePlanQueue           ResourcePlanAction = "queue"
	ResourcePlanOffload         ResourcePlanAction = "offload"
	ResourcePlanReject          ResourcePlanAction = "reject"
)

// ResourcePlanRequest describes one prospective local execution. RAM includes
// model weights plus context/KV memory. Unified-memory models must set VRAM to
// zero; a discrete GPU request binds VRAM to an explicit device when supplied.
type ResourcePlanRequest struct {
	Version       int
	RAMBytes      uint64
	VRAMBytes     uint64
	GPUDevice     string
	LocalRequired bool
}

func (r ResourcePlanRequest) Validate() error {
	if r.Version != ResourcePlanContractVersion || len(r.GPUDevice) > 128 || resources.ValidateNeed(resources.Need{RAM: r.RAMBytes, VRAM: r.VRAMBytes, Device: r.GPUDevice}) != nil {
		return ErrAdmission
	}
	return nil
}

// ResourcePlanResult reports capacity visible at one bounded observation. It
// is deliberately advisory: concurrent work can consume the reported headroom
// immediately after this method returns.
type ResourcePlanResult struct {
	Version                  int
	Action                   ResourcePlanAction
	Reason                   string
	SnapshotTime, ObservedAt time.Time
	RAMHeadroomBytes         uint64
	VRAMHeadroomBytes        uint64
	VRAMKnown                bool
	GPUDevice                string
	MaxAdditionalLocal       int
	Pressure                 bool
}

func (r ResourcePlanResult) Validate() error {
	if r.Version != ResourcePlanContractVersion || len(r.Reason) == 0 || len(r.Reason) > 64 || !utf8.ValidString(r.Reason) || strings.ContainsFunc(r.Reason, unicode.IsControl) || r.MaxAdditionalLocal < 0 || r.MaxAdditionalLocal > 64 || len(r.GPUDevice) > 128 || (!r.VRAMKnown && r.VRAMHeadroomBytes != 0) || (r.GPUDevice != "" && !r.VRAMKnown) {
		return ErrAdmission
	}
	switch r.Action {
	case ResourcePlanExecuteLocal:
		if r.Pressure || r.Reason != resources.CapacityAvailable || r.MaxAdditionalLocal < 1 || r.RAMHeadroomBytes == 0 || (r.GPUDevice != "" && r.VRAMHeadroomBytes == 0) || r.SnapshotTime.IsZero() || r.ObservedAt.IsZero() {
			return ErrAdmission
		}
	case ResourcePlanQueue, ResourcePlanOffload, ResourcePlanReject:
		if r.MaxAdditionalLocal != 0 {
			return ErrAdmission
		}
		if r.Pressure {
			if r.SnapshotTime.IsZero() || (r.Reason != resources.CapacityExhausted && r.Reason != resources.CapacityThermal && r.Reason != resources.CapacitySwap) {
				return ErrAdmission
			}
		} else if !r.SnapshotTime.IsZero() || (r.Action == ResourcePlanOffload && r.Reason != "cloud_only") || (r.Action == ResourcePlanReject && r.Reason != "local_required") || r.Action == ResourcePlanQueue {
			return ErrAdmission
		}
	default:
		return ErrAdmission
	}
	measured := !r.SnapshotTime.IsZero()
	if measured != !r.ObservedAt.IsZero() || measured && (!resourcePlanTime(r.SnapshotTime) || !resourcePlanTime(r.ObservedAt) || r.SnapshotTime.After(r.ObservedAt)) {
		return ErrAdmission
	}
	if !measured && (r.RAMHeadroomBytes != 0 || r.VRAMHeadroomBytes != 0 || r.VRAMKnown || r.GPUDevice != "") {
		return ErrAdmission
	}
	if r.GPUDevice != "" && !resources.ValidGPUDeviceID(r.GPUDevice) {
		return ErrAdmission
	}
	return nil
}

func resourcePlanTime(value time.Time) bool {
	return value.Location() == time.UTC && value.Year() >= 1970 && value.Year() < 2261
}

// ResourcePlan measures and evaluates prospective local capacity using the
// same profiler, hard limits and live in-process reservations as execution.
// It performs no reservation, provider construction, inference or persistence.
func (c *Client) ResourcePlan(ctx context.Context, request ResourcePlanRequest) (ResourcePlanResult, error) {
	empty := ResourcePlanResult{Version: ResourcePlanContractVersion}
	if !c.valid(ctx) || request.Validate() != nil {
		return empty, ErrAdmission
	}
	result, err := c.service.ResourcePlan(ctx, app.ResourcePlanRequest{
		Need:          resources.Need{RAM: request.RAMBytes, VRAM: request.VRAMBytes, Device: request.GPUDevice},
		LocalRequired: request.LocalRequired,
	})
	if err != nil {
		if ctx.Err() != nil {
			return empty, errors.Join(ErrAdmission, ctx.Err())
		}
		return empty, ErrAdmission
	}
	var action ResourcePlanAction
	switch result.Action {
	case app.ResourceActionLocal:
		action = ResourcePlanExecuteLocal
	case app.ResourceActionQueue:
		action = ResourcePlanQueue
	case app.ResourceActionOffload:
		action = ResourcePlanOffload
	case app.ResourceActionReject:
		action = ResourcePlanReject
	default:
		return empty, ErrAdmission
	}
	public := ResourcePlanResult{
		Version:            ResourcePlanContractVersion,
		Action:             action,
		Reason:             result.Reason,
		SnapshotTime:       result.Capacity.SnapshotTime,
		ObservedAt:         result.Capacity.ObservedAt,
		RAMHeadroomBytes:   result.Capacity.Headroom.RAMBytes,
		VRAMHeadroomBytes:  result.Capacity.Headroom.VRAMBytes,
		VRAMKnown:          result.Capacity.Headroom.VRAMKnown,
		GPUDevice:          result.Capacity.Headroom.Device,
		MaxAdditionalLocal: result.Capacity.MaxAdditional,
		Pressure:           result.Capacity.Action == resources.CapacityWait,
	}
	if public.Validate() != nil {
		return empty, ErrAdmission
	}
	return public, nil
}
