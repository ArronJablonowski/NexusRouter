package remote

import (
	"context"
	"slices"
)

// Catalogue returns paired configuration only. Unlike Info it does not invoke
// provider inventory or resource probes, including for ineligible cloud models.
func (c *Client) Catalogue(ctx context.Context, destination string) (Info, error) {
	var out Info
	err := c.call(ctx, destination, "info", "GET", "/v1/remote/catalogue", nil, nil, &out)
	if err == nil && (out.Version != Version || out.Instance != destination || len(out.Models) > 4096 || len(out.Harnesses) > 256) {
		return Info{}, ErrInvalid
	}
	return out, err
}
func (b *SDKBackend) Catalogue(ctx context.Context, models []string, cloud bool, allowed []string) (Info, error) {
	if b == nil || ctx == nil || ctx.Err() != nil {
		return Info{}, ErrUnavailable
	}
	out := Info{Version: Version, Available: b.Available != nil && b.Available(ctx)}
	for _, m := range b.Models {
		if slices.Contains(models, m.ID) && (cloud || m.Local) {
			out.Models = append(out.Models, m)
		}
	}
	out.Models = cloneModels(out.Models)
	out.Harnesses = filterHarnesses(b.Harnesses, out.Models, allowed, false)
	return out, nil
}
func (s *Server) catalogue(ctx context.Context, p Peer) (Info, error) {
	b, ok := s.backend.(interface {
		Catalogue(context.Context, []string, bool, []string) (Info, error)
	})
	if !ok {
		return Info{}, ErrUnavailable
	}
	out, err := b.Catalogue(ctx, slices.Clone(p.Models), p.AllowCloudInference, slices.Clone(p.Harnesses))
	if err != nil {
		return Info{}, err
	}
	out.Version = Version
	out.Instance = s.instance
	models := []Model{}
	for _, m := range out.Models {
		if slices.Contains(p.Models, m.ID) && (p.AllowCloudInference || m.Local) {
			models = append(models, m)
		}
	}
	out.Models = cloneModels(models)
	out.Resources = nil
	out.Harnesses = filterHarnesses(out.Harnesses, out.Models, p.Harnesses, false)
	return out, nil
}
