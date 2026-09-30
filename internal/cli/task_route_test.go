package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func taskRouteDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "route.db")
	db, err := telemetry.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	policy := routing.Defaults()
	policy.Exploration = 0
	candidates := []routing.Candidate{{Model: "fixture", Provider: "local", Local: true, Capabilities: []string{"chat"}, ContextTokens: 8192, Healthy: true, PolicyAllowed: true, CapacityAvailable: true}}
	selection, err := routing.Select(routing.Request{Mode: "local_only", Domain: "code", Profile: "default", Capabilities: []string{"chat"}, ContextTokens: 10, MaxCost: 1}, policy, candidates, nil, time.Unix(100, 0), .5)
	if err != nil {
		t.Fatal(err)
	}
	start := runtime.Event{Version: 1, ID: "start", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 1, Time: time.Unix(100, 0), Kind: runtime.TaskStarted, Data: runtime.Data{Domain: "code", Profile: "default"}}
	route := runtime.Event{Version: 1, ID: "route-event", TaskID: "task", SessionID: "session", CorrelationID: "task", RouteID: "route", Sequence: 2, Time: time.Unix(101, 0), Kind: runtime.RouteSelected, Data: runtime.Data{ModelID: "fixture", ProviderID: "local", Domain: "code", Profile: "default", ConfigID: strings.Repeat("a", 64), RouteCandidates: candidates, RoutePolicy: &policy, Route: &selection}}
	if err := db.Append(context.Background(), 0, start); err != nil {
		t.Fatal(err)
	}
	if err := db.Append(context.Background(), 1, route); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTaskRouteCLIIsReadOnlyAndMetadataOnly(t *testing.T) {
	path := taskRouteDatabase(t)
	var out, diagnostic bytes.Buffer
	if code := Run([]string{"task", "route", "--db", path, "--task", "task"}, &out, &diagnostic, "dev"); code != 0 {
		t.Fatal(code, diagnostic.String())
	}
	var explanation sessions.RouteExplanation
	if json.Unmarshal(out.Bytes(), &explanation) != nil || explanation.Validate() != nil || explanation.TaskID != "task" || explanation.Model != "fixture" {
		t.Fatal(out.String())
	}
	if strings.Contains(out.String(), "prompt") || strings.Contains(out.String(), "raw_output") || strings.Contains(out.String(), "model_output") || strings.Contains(out.String(), "endpoint") || strings.Contains(out.String(), "credential") {
		t.Fatal("route output contains forbidden content fields", out.String())
	}
	if Run([]string{"task", "route", "--db", path, "--task", "task"}, brokenWriter{}, &diagnostic, "dev") != 1 {
		t.Fatal("output failure ignored")
	}
	absent := filepath.Join(t.TempDir(), "absent.db")
	if Run([]string{"task", "route", "--db", absent, "--task", "task"}, &out, &diagnostic, "dev") != 1 {
		t.Fatal("missing database accepted")
	}
	if _, err := os.Stat(absent); !os.IsNotExist(err) {
		t.Fatal("inspection created database", err)
	}
}
