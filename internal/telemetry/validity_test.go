package telemetry

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"darwinrouter/providers"
	"darwinrouter/routing"
	"darwinrouter/runtime"
)

func validityEvents(task string, passed bool) []runtime.Event {
	kinds := []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.EvaluationRecorded, runtime.TaskCompleted}
	if !passed {
		kinds[4] = runtime.TaskFailed
	}
	out := make([]runtime.Event, len(kinds))
	for i, k := range kinds {
		e := event(fmt.Sprintf("%s-%d", task, i), int64(i+1), k)
		e.TaskID, e.TurnID, e.AttemptID = task, "turn", "attempt"
		switch k {
		case runtime.TaskStarted:
			e.Data.Domain, e.Data.Profile = "code", "default"
		case runtime.TurnStarted:
			e.Data.ModelID, e.Data.ProviderID = "candidate", "provider"
		case runtime.TurnCompleted:
			if passed {
				e.Data.Text = "answer"
			} else {
				e.Data.Text = " \n\t"
			}
		case runtime.EvaluationRecorded:
			e.Data = runtime.Data{Accepted: &passed, Code: "deterministic.nonempty_text.v1", ModelID: "candidate", ProviderID: "provider", Domain: "untrusted", Profile: "untrusted"}
		case runtime.TaskFailed:
			e.Data.Code = "empty_output"
		}
		out[i] = e
	}
	return out
}
func appendValidity(t *testing.T, s *Store, events []runtime.Event) {
	t.Helper()
	for i, e := range events {
		if err := s.Append(context.Background(), int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
}
func validityKey() routing.Key {
	return routing.Key{Model: "candidate", Provider: "provider", Domain: "code", Profile: "default"}
}

func TestOutputValidityAggregationAndBound(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "validity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key := validityKey()
	for i, passed := range []bool{true, false, true} {
		appendValidity(t, s, validityEvents(fmt.Sprintf("t%d", i), passed))
	}
	out, err := s.OutputValidity(ctx, key)
	if err != nil || out.Samples != 3 || out.Failures != 1 || out.Updated.IsZero() {
		t.Fatal(out, err)
	}
	for _, other := range []routing.Key{{Model: "other", Provider: key.Provider, Domain: key.Domain, Profile: key.Profile}, {Model: key.Model, Provider: "other", Domain: key.Domain, Profile: key.Profile}, {Model: key.Model, Provider: key.Provider, Domain: "untrusted", Profile: key.Profile}, {Model: key.Model, Provider: key.Provider, Domain: key.Domain, Profile: "untrusted"}} {
		if out, err := s.OutputValidity(ctx, other); err != nil || out.Samples != 0 {
			t.Fatal(other, out, err)
		}
	}
	for i := 0; i < 101; i++ {
		appendValidity(t, s, validityEvents(fmt.Sprintf("bounded%d", i), true))
	}
	out, err = s.OutputValidity(ctx, key)
	if err != nil || out.Samples != 100 || out.Failures != 0 {
		t.Fatal(out, err)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM fitness").Scan(&count); err != nil || count != 0 {
		t.Fatal("fitness mutated", count, err)
	}
}

func TestOutputValidityRejectsInconsistentEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]runtime.Event)
	}{
		{"verdict", func(e []runtime.Event) { wrong := false; e[3].Data.Accepted = &wrong }},
		{"blank", func(e []runtime.Event) { e[2].Data.Text = " " }},
		{"tool_calls", func(e []runtime.Event) { e[2].Data.ToolCalls = []providers.ToolCall{{ID: "tool", Name: "read"}} }},
		{"wrong_attempt", func(e []runtime.Event) { e[3].AttemptID = "other" }},
		{"wrong_turn", func(e []runtime.Event) { e[3].TurnID = "other" }},
		{"wrong_model", func(e []runtime.Event) { e[3].Data.ModelID = "other" }},
		{"wrong_provider", func(e []runtime.Event) { e[3].Data.ProviderID = "other" }},
		{"wrong_terminal", func(e []runtime.Event) { e[4].Kind = runtime.TaskFailed; e[4].Data.Code = "execution_failed" }},
		{"failure_code", func(e []runtime.Event) {
			e[2].Data.Text = " "
			no := false
			e[3].Data.Accepted = &no
			e[4].Kind = runtime.TaskFailed
			e[4].Data.Code = "execution_failed"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, err := Open(ctx, filepath.Join(t.TempDir(), "invalid.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			events := validityEvents("task", true)
			tc.mutate(events)
			appendValidity(t, s, events)
			if _, err := s.OutputValidity(ctx, validityKey()); err == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
}

func TestOutputValidityDuplicateAndRouteContext(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "validity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := validityEvents("task", true)
	route := event("route", 2, runtime.RouteSelected)
	route.TaskID, route.RouteID = "task", "route"
	route.Data = runtime.Data{ModelID: "candidate", ProviderID: "provider", Domain: "creative", Profile: "routed"}
	e = append(e[:1], append([]runtime.Event{route}, e[1:]...)...)
	for i := range e {
		e[i].Sequence = int64(i + 1)
	}
	appendValidity(t, s, e)
	key := validityKey()
	key.Domain, key.Profile = "creative", "routed"
	out, err := s.OutputValidity(ctx, key)
	if err != nil || out.Samples != 1 {
		t.Fatal(out, err)
	}
	if out, err := s.OutputValidity(ctx, validityKey()); err != nil || out.Samples != 0 {
		t.Fatal(out, err)
	}
	// Corrupt persistence with a duplicate evaluation without changing the
	// terminal event; the count check must fail rather than double-count.
	if _, err := s.db.Exec(`INSERT INTO events(id,task_id,sequence,body) SELECT 'duplicate',task_id,99,json_set(body,'$.id','duplicate','$.sequence',99) FROM events WHERE id='task-3'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OutputValidity(ctx, key); err == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestOutputValidityLegacyAndIntermediateToolTurns(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "validity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	legacy := validityEvents("legacy", true)
	legacy = append(legacy[:3], legacy[4])
	legacy[3].Sequence = 4
	appendValidity(t, s, legacy)
	if out, err := s.OutputValidity(ctx, validityKey()); err != nil || out.Samples != 0 {
		t.Fatal("legacy inferred success", out, err)
	}
	final := validityEvents("tools", true)
	started, completed := final[1], final[2]
	started.ID, completed.ID = "intermediate-start", "intermediate-complete"
	started.AttemptID, completed.AttemptID = "tool-attempt", "tool-attempt"
	started.TurnID, completed.TurnID = "tool-turn", "tool-turn"
	completed.Data.Text = ""
	completed.Data.ToolCalls = []providers.ToolCall{{ID: "call", Name: "read"}}
	events := append([]runtime.Event{final[0], started, completed}, final[1:]...)
	for i := range events {
		events[i].Sequence = int64(i + 1)
	}
	appendValidity(t, s, events)
	if out, err := s.OutputValidity(ctx, validityKey()); err != nil || out.Samples != 1 || out.Failures != 0 {
		t.Fatal("intermediate counted", out, err)
	}
}
