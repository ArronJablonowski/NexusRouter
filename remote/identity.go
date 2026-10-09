package remote

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
)

// HarnessIdentityRequest contains no prompt, output or execution authority.
type HarnessIdentityRequest struct {
	ModelID       string `json:"model_id"`
	HarnessID     string `json:"harness_id"`
	ContextTokens int    `json:"context_tokens"`
}

func (q HarnessIdentityRequest) valid() bool {
	return name(q.ModelID) && name(q.HarnessID) && q.HarnessID != "auto" && q.ContextTokens >= 8192 && q.ContextTokens <= 1<<24
}
func (p Peer) permitsIdentity(q HarnessIdentityRequest) bool {
	return q.valid() && slices.Contains(p.Models, q.ModelID) && (slices.Contains(p.Harnesses, q.HarnessID) || q.HarnessID == harness.DirectRegistration(q.ModelID)) && q.ContextTokens <= p.MaxContextTokens
}

// HarnessIdentity is a configured preview, not verified completion provenance.
// Match actual execution identity separately before accepting quality feedback.
type HarnessIdentity struct {
	Version   int                    `json:"version"`
	Instance  string                 `json:"instance"`
	Request   HarnessIdentityRequest `json:"request"`
	Identity  harness.Identity       `json:"identity"`
	CheckedAt time.Time              `json:"checked_at"`
}

func (c *Client) HarnessIdentity(ctx context.Context, destination string, q HarnessIdentityRequest) (HarnessIdentity, error) {
	var out HarnessIdentity
	if !q.valid() {
		return out, ErrInvalid
	}
	headers := map[string]string{"X-Nexus-Model": q.ModelID, "X-Nexus-Harness": q.HarnessID, "X-Nexus-Context": strconv.Itoa(q.ContextTokens)}
	err := c.call(ctx, destination, "info", http.MethodGet, "/v1/remote/harness-identity", nil, headers, &out)
	if err == nil && (out.Version != Version || out.Instance != destination || out.Request != q || out.Identity.Validate() != nil || !freshObservation(out.CheckedAt)) {
		return HarnessIdentity{}, ErrUnavailable
	}
	return out, err
}
func identityRequest(r *http.Request) (HarnessIdentityRequest, error) {
	tokens, err := strconv.Atoi(r.Header.Get("X-Nexus-Context"))
	q := HarnessIdentityRequest{r.Header.Get("X-Nexus-Model"), r.Header.Get("X-Nexus-Harness"), tokens}
	if err != nil || !q.valid() {
		return q, ErrInvalid
	}
	return q, nil
}
func (b *SDKBackend) HarnessIdentity(ctx context.Context, q HarnessIdentityRequest, cloud bool) (harness.Identity, error) {
	if b == nil || b.Identify == nil || ctx == nil || ctx.Err() != nil {
		return harness.Identity{}, ErrUnavailable
	}
	if !q.valid() {
		return harness.Identity{}, ErrInvalid
	}
	for _, m := range b.Models {
		if m.ID != q.ModelID {
			continue
		}
		if (!cloud && !m.Local) || q.ContextTokens > m.ContextTokens {
			return harness.Identity{}, ErrDenied
		}
		for _, h := range b.Harnesses {
			if h.ID != q.HarnessID || h.ModelID != m.ID {
				continue
			}
			identity, err := b.Identify(q.ModelID, q.HarnessID, q.ContextTokens)
			if err != nil || identity.Validate() != nil || identity.Provider != m.Provider || identity.Model != m.Model || identity.Harness != h.Kind || identity.ModelRevision != h.ModelRevision {
				return harness.Identity{}, ErrUnavailable
			}
			return identity, nil
		}
	}
	return harness.Identity{}, ErrDenied
}
func (s *Server) harnessIdentity(ctx context.Context, p Peer, r *http.Request) (HarnessIdentity, error) {
	var out HarnessIdentity
	q, err := identityRequest(r)
	if err != nil {
		return out, err
	}
	if !p.permitsIdentity(q) {
		return out, ErrDenied
	}
	backend, ok := s.backend.(interface {
		HarnessIdentity(context.Context, HarnessIdentityRequest, bool) (harness.Identity, error)
	})
	if !ok {
		return out, ErrUnavailable
	}
	identity, err := backend.HarnessIdentity(ctx, q, p.AllowCloudInference)
	if err != nil {
		return out, err
	}
	if identity.Validate() != nil {
		return out, ErrUnavailable
	}
	return HarnessIdentity{Version, s.instance, q, identity, time.Now().UTC()}, nil
}
