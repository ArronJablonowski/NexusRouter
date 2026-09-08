package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func routeExplanationStore(t *testing.T) (*Store, routing.Selection) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "route.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	policy := routing.Defaults()
	policy.Exploration = 0
	candidates := []routing.Candidate{{Model: "model", Provider: "provider", FailureDomain: "host", Local: true, Capabilities: []string{"chat"}, ContextTokens: 8192, Healthy: true, PolicyAllowed: true, CapacityAvailable: true}}
	selection, err := routing.Select(routing.Request{Mode: "local_only", Domain: "code", Profile: "default", Capabilities: []string{"chat"}, ContextTokens: 10, MaxCost: 1}, policy, candidates, nil, time.Unix(200, 0), .5)
	if err != nil {
		t.Fatal(err)
	}
	start := event("start", 1, runtime.TaskStarted)
	start.CorrelationID = "task"
	start.Data.Domain, start.Data.Profile = "code", "default"
	route := event("route-event", 2, runtime.RouteSelected)
	route.CorrelationID, route.RouteID = "task", "route"
	route.Data = runtime.Data{ModelID: "model", ProviderID: "provider", Domain: "code", Profile: "default", ConfigID: strings.Repeat("a", 64), RouteCandidates: candidates, RoutePolicy: &policy, Route: &selection}
	if err := db.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	if err := db.Append(ctx, 1, route); err != nil {
		t.Fatal(err)
	}
	return db, selection
}

func TestRouteExplanationIsBoundedMetadata(t *testing.T) {
	db, selected := routeExplanationStore(t)
	out, err := db.RouteExplanation(context.Background(), "task")
	if err != nil || out.Validate() != nil || out.Sequence != 2 || out.Selection.Primary != selected.Primary || out.Model != "model" || out.Provider != "provider" || out.Usage == nil || out.Usage.Scope.TaskID != "task" || out.Usage.Scope.SessionID != "session" || out.Usage.Overall.Records != 0 {
		t.Fatal(out, err)
	}
	if strings.Contains(strings.ToLower(strings.Join([]string{out.ConfigID, out.Domain, out.Profile, out.Model, out.Provider}, " ")), "prompt") {
		t.Fatal("unexpected content field")
	}
}

func TestRouteExplanationRejectsAbsentAndMalformedSelection(t *testing.T) {
	ctx := context.Background()
	db, _ := routeExplanationStore(t)
	if _, err := db.db.Exec(`UPDATE events SET body=json_remove(body,'$.data.route') WHERE sequence=2`); err != nil {
		t.Fatal(err)
	}
	if out, err := db.RouteExplanation(ctx, "task"); err == nil || out.TaskID != "" {
		t.Fatal(out, err)
	}

	explicit, err := Open(ctx, filepath.Join(t.TempDir(), "explicit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer explicit.Close()
	start := event("only-start", 1, runtime.TaskStarted)
	start.CorrelationID = "task"
	if err := explicit.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	if out, err := explicit.RouteExplanation(ctx, "task"); !errors.Is(err, sessions.ErrRouteExplanation) || out.TaskID != "" {
		t.Fatal(out, err)
	}
}
