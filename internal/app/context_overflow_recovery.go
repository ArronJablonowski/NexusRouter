package app

import (
	"context"
	"errors"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// providerOverflowCompaction authorizes one new task, never replay of the
// failed provider call. It requires exact durable proof that the provider
// rejected the first turn before any output, tool activity or side effect.
func (s *Service) providerOverflowCompaction(ctx context.Context, r Request, failed Result, runErr error) (Request, bool) {
	var failure *providers.Failure
	if !s.settings.Runtime.AutoApprovedCompaction || ctx.Err() != nil || r.delegatedParent != "" || r.ContinueTaskID == "" || r.Compaction != nil || r.SummaryAttemptID != "" || failed.TaskID == "" || !errors.As(runErr, &failure) || failure == nil || failure.Code != "context_overflow" {
		return Request{}, false
	}
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return Request{}, false
	}
	defer db.Close()
	if len(failed.PreviousTaskIDs) >= sessions.MaxTerminalRouteAttempts-1 {
		return Request{}, false
	}
	modelID, ok := verifiedOutputFreeContextFailure(ctx, db, failed.TaskID, r.ContinueTaskID, s.settings.Models)
	if !ok {
		return Request{}, false
	}
	attempt, _, err := db.LatestApprovedSummary(ctx, r.ContinueTaskID)
	if err != nil {
		return Request{}, false
	}
	recovered := r
	recovered.SummaryAttemptID = attempt.ID
	recovered.retryOfTaskID = failed.TaskID
	recovered.continuation = nil
	recovered.preparedContext = nil
	recovered.autoCompactionTried = true
	if r.ModelID == "" || r.ModelID == "auto" {
		recovered.onlyModelID = modelID
	}
	if r.MaxCost > 0 {
		if failed.RouteEstimatedCost == nil {
			return Request{}, false
		}
		remaining := r.MaxCost - *failed.RouteEstimatedCost
		if remaining < 0 {
			return Request{}, false
		}
		recovered.MaxCost = remaining
	}
	return recovered, true
}

func verifiedOutputFreeContextFailure(ctx context.Context, db *telemetry.Store, task, parent string, models []config.Model) (string, bool) {
	if ctx == nil || db == nil || !sessions.ValidEventPageID(task) || !sessions.ValidEventPageID(parent) {
		return "", false
	}
	snapshot, err := db.TaskSnapshot(ctx, task)
	if err != nil || snapshot.State != "failed" || snapshot.Sequence < 3 || snapshot.Sequence > 5 || snapshot.UncertainEffects || len(snapshot.Pending) != 0 {
		return "", false
	}
	events, err := db.Read(ctx, task, 0, 6)
	if err != nil || int64(len(events)) != snapshot.Sequence {
		return "", false
	}
	starts, routes, failures := 0, 0, 0
	model, provider, turn, attempt := "", "", "", ""
	for i, event := range events {
		if event.TaskID != task || event.Sequence != int64(i+1) {
			return "", false
		}
		switch event.Kind {
		case runtime.TaskStarted:
			if i != 0 || event.Data.Compaction != nil || event.Data.ParentTaskID != parent {
				return "", false
			}
			model, provider = event.Data.ModelID, event.Data.ProviderID
		case runtime.RouteSelected:
			routes++
			if i != 1 || routes != 1 || starts != 0 {
				return "", false
			}
		case runtime.TurnStarted:
			starts++
			if starts != 1 || event.Data.ModelID != model || event.Data.ProviderID != provider {
				return "", false
			}
			turn, attempt = event.TurnID, event.AttemptID
		case runtime.TaskFailed:
			failures++
			if i != len(events)-1 || failures != 1 || event.Data.Code != "context_overflow" || starts != 1 || event.TurnID != turn || event.AttemptID != attempt {
				return "", false
			}
		default:
			return "", false
		}
	}
	if starts != 1 || failures != 1 {
		return "", false
	}
	selected := ""
	for _, candidate := range models {
		if candidate.Model == model && candidate.Provider == provider {
			if selected != "" {
				return "", false
			}
			selected = candidate.ID
		}
	}
	return selected, selected != ""
}
