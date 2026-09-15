package codexbridge

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

func rolloverFixture(t *testing.T) (*Session, providers.Request, providers.Request) {
	t.Helper()
	s, _, request := newSessionFixture(t, nil)
	request.Messages = append(request.Messages, providers.Message{Role: "assistant", Content: "completed"})
	s.started = true
	s.finished = true
	s.thread = "thread-1"
	s.turn = "turn-1"
	completed := cloneRolloverRequest(t, request)
	s.completedRequest = &completed
	replacement := cloneRolloverRequest(t, request)
	replacement.Messages = []providers.Message{
		{Role: "system", Content: "compact context"},
		{Role: "user", Content: "continue from the compact context"},
	}
	return s, request, replacement
}

func cloneRolloverRequest(t *testing.T, request providers.Request) providers.Request {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var clone providers.Request
	if err = json.Unmarshal(body, &clone); err != nil {
		t.Fatal(err)
	}
	if request.JSONSchema == nil {
		clone.JSONSchema = nil
	}
	return clone
}

func TestSessionCheckContextRolloverAcceptsCompletedBoundaryWithoutMutation(t *testing.T) {
	s, current, replacement := rolloverFixture(t)
	beforeCurrent, _ := json.Marshal(current)
	beforeReplacement, _ := json.Marshal(replacement)
	beforeCompleted := *s.completedRequest

	if err := s.CheckContextRollover(context.Background(), current, replacement); err != nil {
		t.Fatal(err)
	}
	afterCurrent, _ := json.Marshal(current)
	afterReplacement, _ := json.Marshal(replacement)
	if string(afterCurrent) != string(beforeCurrent) || string(afterReplacement) != string(beforeReplacement) ||
		!reflect.DeepEqual(*s.completedRequest, beforeCompleted) || !s.started || !s.finished || s.pending != nil {
		t.Fatal("rollover inspection mutated session or caller-owned requests")
	}
}

func TestSessionCheckContextRolloverRejectsInProgressAndToolBoundaries(t *testing.T) {
	t.Run("in progress", func(t *testing.T) {
		s, current, replacement := rolloverFixture(t)
		s.finished = false
		if err := s.CheckContextRollover(context.Background(), current, replacement); err == nil {
			t.Fatal("accepted an in-progress native turn")
		}
	})
	t.Run("tool boundary", func(t *testing.T) {
		s, current, replacement := rolloverFixture(t)
		pending, _ := fixture(t)
		s.pending = pending
		if err := s.CheckContextRollover(context.Background(), current, replacement); err == nil {
			t.Fatal("accepted a paused tool boundary")
		}
	})
}

func TestSessionCheckContextRolloverRequiresExactCompletedRequest(t *testing.T) {
	for name, mutate := range map[string]func(*providers.Request){
		"message": func(r *providers.Request) { r.Messages[1].Content = "changed" },
		"model":   func(r *providers.Request) { r.Model = "changed" },
		"tools":   func(r *providers.Request) { r.Tools[0].Description = "changed" },
		"schema":  func(r *providers.Request) { r.JSONSchema = json.RawMessage(`{}`) },
	} {
		t.Run(name, func(t *testing.T) {
			s, current, replacement := rolloverFixture(t)
			mutate(&current)
			if err := s.CheckContextRollover(context.Background(), current, replacement); err == nil {
				t.Fatal("accepted a request other than the exact completed request")
			}
		})
	}
}

func TestSessionCheckContextRolloverRejectsInvalidProspectiveImport(t *testing.T) {
	for name, mutate := range map[string]func(*providers.Request){
		"ends in assistant": func(r *providers.Request) { r.Messages[len(r.Messages)-1].Role = "assistant" },
		"orphan tool": func(r *providers.Request) {
			r.Messages = []providers.Message{{Role: "tool", ToolCallID: "missing"}, {Role: "user", Content: "continue"}}
		},
		"changed catalog": func(r *providers.Request) { r.Tools[0].Description = "changed" },
		"changed schema":  func(r *providers.Request) { r.JSONSchema = json.RawMessage(`{}`) },
		"changed limit":   func(r *providers.Request) { r.MaxOutputTokens = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			s, current, replacement := rolloverFixture(t)
			mutate(&replacement)
			if err := s.CheckContextRollover(context.Background(), current, replacement); err == nil {
				t.Fatal("accepted invalid prospective replacement")
			}
		})
	}
}

func TestSessionCheckContextRolloverFailsClosedWhenSessionBusy(t *testing.T) {
	s, current, replacement := rolloverFixture(t)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.CheckContextRollover(context.Background(), current, replacement); err == nil {
		t.Fatal("accepted rollover while the session lock was held")
	}
}
