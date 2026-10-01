package remote

import (
	"context"
	"math"
	"slices"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

// SDKBackend uses the existing durable submission and task-event contracts.
// A normal matching daemon/dispatcher must run separately. Submission is not
// execution; all runtime policy, privacy, tools and resource admission still run.
type SDKBackend struct {
	Identify  func(string, string, int) (harness.Identity, error)
	Harnesses []Harness
	Client    *sdk.Client
	Models    []Model
	Available func(context.Context) bool
	// Observe receives only the permitted model catalogue when called by Server.
	// It may attach advisory observations, not change configured capabilities.
	Observe func(context.Context, []Model) ([]ModelObservation, *ResourceObservation, error)
}

func (b *SDKBackend) Info(ctx context.Context) (Info, error) {
	if b == nil || b.Client == nil {
		return Info{}, ErrUnavailable
	}
	out, err := b.info(ctx, b.Models)
	if err == nil {
		out.Harnesses = filterHarnesses(b.Harnesses, out.Models, nil, true)
	}
	return out, err
}

// InfoFor scopes discovery before provider traffic, so an info-only caller
// cannot trigger probes for models or cloud providers outside its peer policy.
func (b *SDKBackend) InfoFor(ctx context.Context, allowed []string, cloud bool) (Info, error) {
	if b == nil || b.Client == nil {
		return Info{}, ErrUnavailable
	}
	var models []Model
	for _, m := range b.Models {
		if slices.Contains(allowed, m.ID) && (cloud || m.Local) {
			models = append(models, m)
		}
	}
	return b.info(ctx, models)
}
func (b *SDKBackend) info(ctx context.Context, configured []Model) (Info, error) {
	if ctx == nil || ctx.Err() != nil {
		return Info{}, ErrUnavailable
	}
	models := cloneModels(configured)
	out := Info{Version: Version, Models: models, Available: b.Available != nil && b.Available(ctx)}
	if b.Observe != nil {
		observations, resources, err := b.Observe(ctx, cloneModels(models))
		if err != nil || ctx.Err() != nil || len(observations) != len(models) {
			return Info{}, ErrUnavailable
		}
		for i, observation := range observations {
			if !freshObservation(observation.CheckedAt) || (observation.State != "present" && observation.State != "absent" && observation.State != "unknown") {
				return Info{}, ErrUnavailable
			}
			out.Models[i].Observation = &observation
		}
		if resources != nil {
			if !freshObservation(resources.CheckedAt) {
				return Info{}, ErrUnavailable
			}
			switch resources.State {
			case "unknown":
				if resources.TotalRAM != nil || resources.AvailableRAM != nil {
					return Info{}, ErrUnavailable
				}
			case "measured":
				if resources.TotalRAM == nil || resources.AvailableRAM == nil || *resources.TotalRAM == 0 || *resources.AvailableRAM > *resources.TotalRAM {
					return Info{}, ErrUnavailable
				}
			default:
				return Info{}, ErrUnavailable
			}
			copy := *resources
			if copy.TotalRAM != nil {
				n := *copy.TotalRAM
				copy.TotalRAM = &n
			}
			if copy.AvailableRAM != nil {
				n := *copy.AvailableRAM
				copy.AvailableRAM = &n
			}
			out.Resources = &copy
		}
	}
	return out, nil
}

func (b *SDKBackend) Submit(ctx context.Context, key string, t Task) (submissions.Status, error) {
	if b == nil || b.Client == nil || t.Validate() != nil {
		return submissions.Status{}, ErrInvalid
	}
	if t.HarnessID != "" {
		found := false
		for _, h := range b.Harnesses {
			if h.ID == t.HarnessID && h.ModelID == t.ModelID {
				found = true
			}
		}
		if !found {
			return submissions.Status{}, ErrDenied
		}
	}
	eligible := false
	for _, m := range b.Models {
		if m.ID == t.ModelID && (!t.Private || m.Local) && m.ContextTokens >= t.ContextTokens && m.EstimatedCost != nil && !math.IsNaN(*m.EstimatedCost) && !math.IsInf(*m.EstimatedCost, 0) && *m.EstimatedCost >= 0 && *m.EstimatedCost <= t.MaxCost {
			eligible = true
		}
	}
	if !eligible {
		return submissions.Status{}, ErrDenied
	}
	return b.Client.Submit(ctx, key, sdk.Request{Version: 1, HarnessID: t.HarnessID, HarnessDifficulty: t.HarnessDifficulty, ModelID: t.ModelID, Prompt: t.Prompt, Domain: t.Domain, Profile: t.Profile, ContextTokens: t.ContextTokens, MaxCost: t.MaxCost, LocalRequired: t.Private})
}
func (b *SDKBackend) Status(ctx context.Context, id string) (submissions.Status, error) {
	return b.Client.SubmissionStatus(ctx, id)
}
func (b *SDKBackend) Cancel(ctx context.Context, id string) (submissions.Status, error) {
	return b.Client.CancelSubmission(ctx, id)
}
func (b *SDKBackend) Events(ctx context.Context, id string, after int64, limit int) (sessions.EventPage, error) {
	return b.Client.ReadEvents(ctx, id, after, limit)
}

func freshObservation(t time.Time) bool {
	now := time.Now()
	return !t.IsZero() && !t.After(now) && !t.Before(now.Add(-15*time.Second))
}
func cloneModels(source []Model) []Model {
	out := slices.Clone(source)
	for i := range out {
		out[i].Capabilities = slices.Clone(out[i].Capabilities)
		out[i].Observation = nil
		if out[i].EstimatedCost != nil {
			n := *out[i].EstimatedCost
			out[i].EstimatedCost = &n
		}
	}
	return out
}
