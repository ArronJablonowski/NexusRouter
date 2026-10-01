package remote

import (
	"context"
	"slices"
)

// Harness is a configured destination registration, not live executable or
// capability attestation. Paths and credentials are deliberately not exported.
type Harness struct {
	ID            string `json:"id"`
	ModelID       string `json:"model_id"`
	Kind          string `json:"kind"`
	ModelRevision string `json:"model_revision"`
	NativeTools   bool   `json:"native_tools"`
}

func (b *SDKBackend) InfoForHarnesses(ctx context.Context, models []string, cloud bool, allowed []string) (Info, error) {
	out, err := b.InfoFor(ctx, models, cloud)
	if err == nil {
		out.Harnesses = filterHarnesses(b.Harnesses, out.Models, allowed, false)
	}
	return out, err
}
func filterHarnesses(configured []Harness, models []Model, allowed []string, all bool) []Harness {
	var out []Harness
	for _, h := range configured {
		if !all && !slices.Contains(allowed, h.ID) {
			continue
		}
		for _, m := range models {
			if m.ID == h.ModelID {
				out = append(out, h)
				break
			}
		}
	}
	return out
}
