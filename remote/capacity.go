package remote

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/resources"
)

type HarnessCapacity struct {
	Version   int                      `json:"version"`
	Instance  string                   `json:"instance"`
	Request   HarnessIdentityRequest   `json:"request"`
	Identity  harness.Identity         `json:"identity"`
	Need      resources.Need           `json:"need"`
	Capacity  resources.CapacityResult `json:"capacity"`
	CheckedAt time.Time                `json:"checked_at"`
}

func (c HarnessCapacity) valid() bool {
	if c.Version != Version || !id(c.Instance) || !c.Request.valid() || c.Identity.Validate() != nil || resources.ValidateNeed(c.Need) != nil || c.Capacity.Validate() != nil || !freshObservation(c.CheckedAt) || !freshObservation(c.Capacity.SnapshotTime) || !freshObservation(c.Capacity.ObservedAt) || c.Capacity.ObservedAt.After(c.CheckedAt) {
		return false
	}
	if c.Capacity.Action == resources.CapacityAdmit {
		if c.Capacity.Headroom.RAMBytes < c.Need.RAM {
			return false
		}
		if c.Need.VRAM > 0 && (!c.Capacity.Headroom.VRAMKnown || c.Capacity.Headroom.VRAMBytes < c.Need.VRAM) {
			return false
		}
		if c.Need.Device != "" && c.Capacity.Headroom.Device != c.Need.Device {
			return false
		}
	}
	return true
}

// HarnessCapacity is a fresh advisory plan, not a reservation, authorization,
// model-residency assertion or verified native-harness capability attestation.
func (c *Client) HarnessCapacity(ctx context.Context, destination string, q HarnessIdentityRequest) (HarnessCapacity, error) {
	var out HarnessCapacity
	if !q.valid() {
		return out, ErrInvalid
	}
	err := c.call(ctx, destination, "info", "GET", "/v1/remote/harness-capacity", nil, map[string]string{"X-Nexus-Model": q.ModelID, "X-Nexus-Harness": q.HarnessID, "X-Nexus-Context": strconv.Itoa(q.ContextTokens)}, &out)
	if err == nil && (!out.valid() || out.Instance != destination || out.Request != q) {
		return HarnessCapacity{}, ErrUnavailable
	}
	return out, err
}
func (b *SDKBackend) HarnessCapacity(ctx context.Context, q HarnessIdentityRequest, cloud bool) (harness.Identity, resources.Need, resources.CapacityResult, error) {
	var need resources.Need
	var capacity resources.CapacityResult
	if b == nil || b.PlanHarness == nil || ctx == nil {
		return harness.Identity{}, need, capacity, ErrUnavailable
	}
	// The same scoped registration/model/cloud checks precede any measurement.
	expected, err := b.HarnessIdentity(ctx, q, cloud)
	if err != nil {
		return harness.Identity{}, need, capacity, err
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	identity, need, capacity, err := b.PlanHarness(bounded, q.ModelID, q.HarnessID, q.ContextTokens)
	if err != nil || bounded.Err() != nil || identity != expected || resources.ValidateNeed(need) != nil || capacity.Validate() != nil {
		return harness.Identity{}, resources.Need{}, resources.CapacityResult{}, ErrUnavailable
	}
	return identity, need, capacity, nil
}
func (s *Server) harnessCapacity(ctx context.Context, p Peer, r *http.Request) (HarnessCapacity, error) {
	var out HarnessCapacity
	q, err := identityRequest(r)
	if err != nil {
		return out, err
	}
	if !p.permitsIdentity(q) {
		return out, ErrDenied
	}
	b, ok := s.backend.(interface {
		HarnessCapacity(context.Context, HarnessIdentityRequest, bool) (harness.Identity, resources.Need, resources.CapacityResult, error)
	})
	if !ok {
		return out, ErrUnavailable
	}
	identity, need, capacity, err := b.HarnessCapacity(ctx, q, p.AllowCloudInference)
	if err != nil {
		return out, err
	}
	out = HarnessCapacity{Version, s.instance, q, identity, need, capacity, time.Now().UTC()}
	if !out.valid() {
		return HarnessCapacity{}, ErrUnavailable
	}
	return out, nil
}
