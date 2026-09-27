package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

type steeringFixture struct {
	events         []runtime.Event
	pending        []*runtime.SteeringMessage
	gateCompletion bool
	gateFailure    bool
	failedAppend   bool
	panicSource    bool
}

func (s *steeringFixture) queue(text string) {
	s.pending = append(s.pending, &runtime.SteeringMessage{Version: 1, ID: fmt.Sprint("guidance", len(s.events), "_", len(s.pending)), TaskID: "task", Text: text, State: "pending", CreatedAt: time.Now().UTC()})
}
func (s *steeringFixture) NextSteering(context.Context, string) (*runtime.SteeringMessage, error) {
	if s.panicSource {
		panic("private error")
	}
	if len(s.pending) == 0 {
		return nil, nil
	}
	copy := *s.pending[0]
	return &copy, nil
}
func (s *steeringFixture) Append(_ context.Context, seq int64, e runtime.Event) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if e.Kind == runtime.TaskCompleted && s.gateCompletion {
		s.gateCompletion = false
		s.queue("late guidance")
		return runtime.ErrSteeringPending
	}
	if e.Kind == runtime.TaskFailed && (e.Data.Code == "provider_retryable_no_output" || e.Data.Code == "provider_failed_before_tools") && s.gateFailure {
		s.gateFailure = false
		s.queue("late failure guidance")
		return runtime.ErrSteeringPending
	}
	if e.Kind == runtime.SteeringApplied {
		if s.failedAppend {
			return errors.New("ambiguous persistence")
		}
		if len(s.pending) == 0 || s.pending[0].ID != e.Data.SteeringID || e.TurnID != "" || e.AttemptID != "" {
			return errors.New("invalid applied event")
		}
		s.pending = s.pending[1:]
	}
	if seq != int64(len(s.events)) {
		return errors.New("bad sequence")
	}
	s.events = append(s.events, e)
	return nil
}

func TestSteeringBeforeTurnAndAfterAnswerDurableBeforeDispatch(t *testing.T) {
	for _, when := range []string{"before", "answer", "completion_race"} {
		t.Run(when, func(t *testing.T) {
			journal := &steeringFixture{gateCompletion: when == "completion_race"}
			if when == "before" {
				journal.queue("initial guidance")
			}
			calls := 0
			loop := runtime.Loop{Journal: journal, Steering: journal, Provider: model(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
				calls++
				if when == "before" || calls == 2 {
					found := false
					for _, e := range journal.events {
						if e.Kind == runtime.SteeringApplied {
							found = true
						}
					}
					if !found || r.Messages[len(r.Messages)-1].Role != "user" {
						t.Fatal("provider preceded durable guidance", r.Messages)
					}
					if calls == 2 && (len(r.Messages) < 3 || r.Messages[len(r.Messages)-2].Content != "draft") {
						t.Fatal("assistant draft lost", r.Messages)
					}
				}
				if when == "answer" && calls == 1 {
					journal.queue("revise the draft")
				}
				text := "final"
				if when != "before" && calls == 1 {
					text = "draft"
				}
				return emit(providers.Chunk{Text: text, Done: true, FinishReason: "stop"})
			})}
			r := runRequest()
			r.RequireText = true
			out, err := loop.Run(context.Background(), r)
			if err != nil || out.Text != "final" || len(journal.pending) != 0 {
				t.Fatal(out, err)
			}
			if when != "before" && calls != 2 {
				t.Fatal(calls)
			}
		})
	}
}

func TestSteeringDoesNotSplitToolResults(t *testing.T) {
	journal := &steeringFixture{}
	calls := 0
	tools := 0
	loop := runtime.Loop{Journal: journal, Steering: journal, Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
		tools++
		if tools == 1 {
			journal.queue("after tools")
		}
		return runtime.ToolResult{Content: "tool result", Effect: runtime.ConfirmedEffect}, nil
	}), Provider: model(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
		calls++
		if calls == 1 {
			for _, id := range []string{"one", "two"} {
				if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: id, Name: "lookup", Arguments: []byte(`{}`)}}); err != nil {
					return err
				}
			}
			return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
		}
		if len(r.Messages) != 5 || r.Messages[1].Role != "assistant" || r.Messages[2].Role != "tool" || r.Messages[3].Role != "tool" || r.Messages[4].Role != "user" {
			t.Fatal(r.Messages)
		}
		for i, e := range journal.events {
			if e.Kind == runtime.SteeringApplied && (i == 0 || journal.events[i-1].Kind != runtime.ToolCompleted) {
				t.Fatal("tool batch split", journal.events)
			}
		}
		return emit(providers.Chunk{Text: "answer", Done: true, FinishReason: "stop"})
	})}
	if _, err := loop.Run(context.Background(), runRequest()); err != nil {
		t.Fatal(err)
	}
}

func TestSteeringLastTurnBudgetAndSourceFailure(t *testing.T) {
	for _, mode := range []string{"last", "panic", "persistence", "too_many"} {
		t.Run(mode, func(t *testing.T) {
			journal := &steeringFixture{panicSource: mode == "panic", failedAppend: mode == "persistence"}
			if mode == "persistence" {
				journal.queue("guidance")
			}
			if mode == "too_many" {
				for i := 0; i < 33; i++ {
					journal.queue("guidance")
				}
			}
			calls := 0
			loop := runtime.Loop{Journal: journal, Steering: journal, Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				calls++
				journal.queue("late")
				return emit(providers.Chunk{Text: "draft", Done: true, FinishReason: "stop"})
			})}
			r := runRequest()
			r.MaxTurns = 1
			_, err := loop.Run(context.Background(), r)
			if err == nil {
				t.Fatal("ignored steering boundary failure")
			}
			if mode == "persistence" {
				if !errors.Is(err, runtime.ErrPersistence) || len(journal.events) != 1 {
					t.Fatal(journal.events, err)
				}
			} else if journal.events[len(journal.events)-1].Kind != runtime.TaskFailed {
				t.Fatal(journal.events, err)
			}
			if mode != "last" && calls != 0 {
				t.Fatal("dispatch after invalid guidance")
			}
		})
	}
}

func TestSteeringDisablesRetryableFallback(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprint(late), func(t *testing.T) {
			journal := &steeringFixture{gateFailure: late}
			if !late {
				journal.queue("guidance")
			}
			loop := runtime.Loop{Journal: journal, Steering: journal, Provider: model(func(context.Context, providers.Request, func(providers.Chunk) error) error {
				return &providers.Failure{Retryable: true}
			})}
			out, err := loop.Run(context.Background(), runRequest())
			if err == nil || out.Retryable || journal.events[len(journal.events)-1].Data.Code != "execution_failed" {
				t.Fatal(out, err, journal.events)
			}
		})
	}
}

func TestSteeringDisablesStreamRecovery(t *testing.T) {
	for _, late := range []bool{false, true} {
		journal := &steeringFixture{gateFailure: late}
		if !late {
			journal.queue("guidance")
		}
		loop := runtime.Loop{Journal: journal, Steering: journal, Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			if err := emit(providers.Chunk{Text: "partial"}); err != nil {
				return err
			}
			return &providers.Failure{Code: "invalid_stream", Partial: true}
		})}
		out, err := loop.Run(context.Background(), runRequest())
		if err == nil || out.Retryable || journal.events[len(journal.events)-1].Data.Code != "execution_failed" {
			t.Fatal(out, err, journal.events)
		}
	}
}
