package remote

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
)

type HarnessReadiness struct {
	Version   int                    `json:"version"`
	Instance  string                 `json:"instance"`
	Request   HarnessIdentityRequest `json:"request"`
	Readiness harness.Readiness      `json:"readiness"`
	CheckedAt time.Time              `json:"checked_at"`
}

func (c HarnessReadiness) valid() bool {
	return c.Version == Version && id(c.Instance) && c.Request.valid() && c.Readiness.Validate() == nil && freshObservation(c.CheckedAt)
}

// HarnessReadiness observes prerequisites, not runtime behavior or quality.
func (c *Client) HarnessReadiness(ctx context.Context, destination string, q HarnessIdentityRequest) (HarnessReadiness, error) {
	var out HarnessReadiness
	if !q.valid() {
		return out, ErrInvalid
	}
	err := c.call(ctx, destination, "info", "GET", "/v1/remote/harness-readiness", nil, map[string]string{"X-Nexus-Model": q.ModelID, "X-Nexus-Harness": q.HarnessID, "X-Nexus-Context": strconv.Itoa(q.ContextTokens)}, &out)
	if err == nil && (!out.valid() || out.Instance != destination || out.Request != q) {
		return HarnessReadiness{}, ErrUnavailable
	}
	return out, err
}
func (b *SDKBackend) HarnessReadiness(ctx context.Context, q HarnessIdentityRequest, cloud bool) (harness.Readiness, error) {
	if b == nil || b.CheckHarness == nil || ctx == nil {
		return harness.Readiness{}, ErrUnavailable
	}
	expected, err := b.HarnessIdentity(ctx, q, cloud)
	if err != nil {
		return harness.Readiness{}, err
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := b.CheckHarness(bounded, q.ModelID, q.HarnessID, q.ContextTokens)
	if err != nil || bounded.Err() != nil || out.Identity != expected || out.Validate() != nil {
		return harness.Readiness{}, ErrUnavailable
	}
	return out, nil
}
func (s *Server) harnessReadiness(ctx context.Context, p Peer, r *http.Request) (HarnessReadiness, error) {
	var out HarnessReadiness
	q, err := identityRequest(r)
	if err != nil {
		return out, err
	}
	if !p.permitsIdentity(q) {
		return out, ErrDenied
	}
	b, ok := s.backend.(interface {
		HarnessReadiness(context.Context, HarnessIdentityRequest, bool) (harness.Readiness, error)
	})
	if !ok {
		return out, ErrUnavailable
	}
	readiness, err := b.HarnessReadiness(ctx, q, p.AllowCloudInference)
	if err != nil {
		return out, err
	}
	out = HarnessReadiness{Version, s.instance, q, readiness, time.Now().UTC()}
	if !out.valid() {
		return HarnessReadiness{}, ErrUnavailable
	}
	return out, nil
}
