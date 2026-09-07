package telemetry

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// RouteExplanation reads only the fixed initial route boundary. Automatic
// tasks persist route.selected immediately after task.started.
func (s *Store) RouteExplanation(ctx context.Context, task string) (sessions.RouteExplanation, error) {
	zero := sessions.RouteExplanation{}
	page, err := s.ReadEventPage(ctx, task, 0, 2)
	if err != nil {
		return zero, err
	}
	if page.Validate() != nil || len(page.Events) < 2 || page.Events[0].Kind != runtime.TaskStarted || page.Events[1].Kind != runtime.RouteSelected {
		return zero, sessions.ErrRouteExplanation
	}
	event := page.Events[1]
	if event.Data.Route == nil || event.Data.RoutePolicy == nil {
		return zero, sessions.ErrRouteExplanation
	}
	out := sessions.RouteExplanation{
		Version: 1, TaskID: task, SessionID: event.SessionID, RouteID: event.RouteID,
		Sequence: event.Sequence, RecordedAt: event.Time, ConfigID: event.Data.ConfigID,
		Domain: event.Data.Domain, Profile: event.Data.Profile, Model: event.Data.ModelID,
		Provider: event.Data.ProviderID, Candidates: event.Data.RouteCandidates,
		Policy: *event.Data.RoutePolicy, Selection: *event.Data.Route,
	}
	if out.Validate() != nil {
		return zero, sessions.ErrRouteExplanation
	}
	return out, nil
}
