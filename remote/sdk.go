package remote

import (
	"context"
	"math"
	"slices"

	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

// SDKBackend uses the existing durable submission and task-event contracts.
// A normal matching daemon/dispatcher must run separately. Submission is not
// execution; all runtime policy, privacy, tools and resource admission still run.
type SDKBackend struct {
	Client    *sdk.Client
	Models    []Model
	Available func(context.Context) bool
}

func (b *SDKBackend) Info(ctx context.Context) (Info, error) {
	if b == nil || b.Client == nil {
		return Info{}, ErrUnavailable
	}
	models := slices.Clone(b.Models)
	for i := range models {
		models[i].Capabilities = slices.Clone(models[i].Capabilities)
	}
	return Info{Version: Version, Models: models, Available: b.Available != nil && b.Available(ctx)}, nil
}
func (b *SDKBackend) Submit(ctx context.Context, key string, t Task) (submissions.Status, error) {
	if b == nil || b.Client == nil || t.Validate() != nil {
		return submissions.Status{}, ErrInvalid
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
	return b.Client.Submit(ctx, key, sdk.Request{Version: 1, ModelID: t.ModelID, Prompt: t.Prompt, Domain: t.Domain, Profile: t.Profile, ContextTokens: t.ContextTokens, MaxCost: t.MaxCost, LocalRequired: t.Private})
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
