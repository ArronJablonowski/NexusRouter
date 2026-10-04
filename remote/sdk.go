package remote

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/internal/usagestats"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/resources"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

// SDKBackend uses the existing durable submission and task-event contracts.
// A normal matching daemon/dispatcher must run separately. Submission is not
// execution; all runtime policy, privacy, tools and resource admission still run.
type SDKBackend struct {
	Schedules     func(context.Context) (webui.SchedulePage, error)
	JobHistory    func(context.Context, string, webui.HistoryOptions) (webui.HistoryPage, error)
	RunnerModelID string
	ControlRunner func(context.Context, string) (RunnerStatus, error)
	Routing       func(context.Context, []string) (webui.ModelInspectionPage, error)
	// ReadStatus borrows the serving dispatcher store when available.
	ReadStatus func(context.Context, string) (submissions.Status, error)

	LogEvents func(context.Context, sessions.EventLogOptions) (sessions.CommittedEventPage, error)

	Usage func(context.Context, []string) (usagestats.RemoteUsage, error)

	CheckHarness func(context.Context, string, string, int) (harness.Readiness, error)
	PlanHarness  func(context.Context, string, string, int) (harness.Identity, resources.Need, resources.CapacityResult, error)
	Identify     func(string, string, int) (harness.Identity, error)
	Harnesses    []Harness
	Client       *sdk.Client
	Models       []Model
	Available    func(context.Context) bool
	// Observe receives only the permitted model catalogue when called by Server.
	// It may attach advisory observations, not change configured capabilities.
	Observe func(context.Context, []Model) ([]ModelObservation, *ResourceObservation, error)
}

func (b *SDKBackend) Info(ctx context.Context) (Info, error) {
	if b == nil || b.Client == nil {
		return Info{}, ErrUnavailable
	}
	out, err := b.info(ctx, b.Models)
	out.HybridVersion = 1
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
	out := Info{HybridVersion: 1, Version: Version, Models: models, Available: b.Available != nil && b.Available(ctx)}
	if b.Schedules != nil {
		page, err := b.Schedules(ctx)
		if err != nil || page.Validate() != nil {
			return Info{}, ErrUnavailable
		}
		out.Schedules = &page
	}
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
	if b.Routing != nil {
		ids := make([]string, 0, len(models))
		for _, m := range models {
			ids = append(ids, m.ID)
		}
		page, err := b.Routing(ctx, ids)
		if err == nil && page.Availability == webui.Available && page.Rankings != nil {
			out.Routing = &webui.RoutingInspection{Rankings: page.Rankings, CommanderID: page.CommanderID, CommanderSource: page.CommanderSource, CommanderFallbackID: page.CommanderFallbackID}
			if out.ValidateRouting() != nil {
				return Info{}, ErrInvalid
			}
		}
	}
	return out, nil
}

func (b *SDKBackend) Submit(ctx context.Context, key string, t Task) (submissions.Status, error) {
	if b != nil && b.RunnerModelID != "" && t.ModelID == b.RunnerModelID {
		state, err := b.Runner(ctx, "status")
		if err != nil || !state.Enabled || state.State != "active" {
			return submissions.Status{}, ErrUnavailable
		}
	}
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
	execution := t.Execution
	if execution == nil {
		execution = &runtime.RemoteExecution{Mode: "direct", Depth: 1}
	}
	return b.Client.Submit(ctx, key, sdk.Request{RemoteExecution: execution, Version: 1, ExpectedHarnessIdentity: t.ExpectedHarnessIdentity, HarnessID: t.HarnessID, HarnessDifficulty: t.HarnessDifficulty, ModelID: t.ModelID, Prompt: t.Prompt, Domain: t.Domain, Profile: t.Profile, ContextTokens: t.ContextTokens, MaxCost: t.MaxCost, LocalRequired: t.Private})
}
func (b *SDKBackend) Status(ctx context.Context, id string) (submissions.Status, error) {
	if b.ReadStatus != nil {
		return b.ReadStatus(ctx, id)
	}
	return b.Client.SubmissionStatus(ctx, id)
}
func (b *SDKBackend) Cancel(ctx context.Context, id string) (submissions.Status, error) {
	return b.Client.CancelSubmission(ctx, id)
}
func (b *SDKBackend) Events(ctx context.Context, id string, after int64, limit int) (sessions.EventPage, error) {
	return b.Client.ReadEvents(ctx, id, after, limit)
}

// Remote nodes have independent clocks. Permit at most one second of positive
// skew for advisory observations; old observations retain the 15-second limit.
// This does not alter certificate validity or destination resource admission.
func freshObservation(t time.Time) bool { return freshObservationAt(t, time.Now()) }
func freshObservationAt(t, now time.Time) bool {
	return !t.IsZero() && !t.After(now.Add(time.Second)) && !t.Before(now.Add(-15*time.Second))
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

func (b *SDKBackend) RemoteUsage(ctx context.Context, tasks []string) (usagestats.RemoteUsage, error) {
	if b.Usage == nil {
		return usagestats.RemoteUsage{}, ErrUnavailable
	}
	return b.Usage(ctx, tasks)
}

func (b *SDKBackend) CommittedLogs(ctx context.Context, o sessions.EventLogOptions) (sessions.CommittedEventPage, error) {
	if b.LogEvents == nil {
		return sessions.CommittedEventPage{}, ErrUnavailable
	}
	return b.LogEvents(ctx, o)
}

// JobDescription uses the redacted browser presentation, never raw system/tool events.
func (b *SDKBackend) JobDescription(ctx context.Context, ids []string) string {
	if b.JobHistory == nil || len(ids) == 0 {
		return ""
	}
	page, err := b.Client.ReadEvents(ctx, ids[0], 0, 1)
	if err != nil {
		return ""
	}
	history, err := b.JobHistory(ctx, page.SessionID, webui.HistoryOptions{Limit: 1})
	if err != nil || history.TaskID != ids[0] {
		return ""
	}
	for _, m := range history.Messages {
		if m.Role == "user" {
			text := strings.Join(strings.Fields(m.Text), " ")
			runes := []rune(text)
			if len(runes) > 150 {
				return string(runes[:147]) + "…"
			}
			return text
		}
	}
	return ""
}
