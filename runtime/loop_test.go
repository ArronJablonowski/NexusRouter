package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"darwinrouter/internal/telemetry"
	"darwinrouter/providers"
	"darwinrouter/runtime"
)

type model func(context.Context, providers.Request, func(providers.Chunk) error) error

func (m model) Stream(c context.Context, r providers.Request, e func(providers.Chunk) error) error {
	return m(c, r, e)
}
func (m model) Models(context.Context) ([]string, error) { return []string{"fixture"}, nil }

type executor func(context.Context, providers.ToolCall) (runtime.ToolResult, error)

func (e executor) Execute(c context.Context, t providers.ToolCall) (runtime.ToolResult, error) {
	return e(c, t)
}

type journal func(context.Context, int64, runtime.Event) error

func (j journal) Append(c context.Context, s int64, e runtime.Event) error { return j(c, s, e) }

func runRequest() runtime.RunRequest {
	return runtime.RunRequest{TaskID: "task", SessionID: "session", ProviderID: "fixture", Inference: providers.Request{Model: "fixture", Messages: []providers.Message{{Role: "user", Content: "hello"}}}, MaxTurns: 3, MaxOutputBytes: 4096}
}
func store(t *testing.T) (*telemetry.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.db")
	s, err := telemetry.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}
func emitCall(e func(providers.Chunk) error) error {
	if err := e(providers.Chunk{ToolCall: &providers.ToolCall{ID: "call1", Name: "lookup", Arguments: json.RawMessage(`{}`)}}); err != nil {
		return err
	}
	return e(providers.Chunk{Done: true, FinishReason: "tool_calls"})
}

func TestDurableToolLoop(t *testing.T) {
	s, path := store(t)
	turns := 0
	executions := 0
	l := runtime.Loop{Journal: s, Provider: model(func(_ context.Context, r providers.Request, e func(providers.Chunk) error) error {
		turns++
		if turns == 1 {
			return emitCall(e)
		}
		if len(r.Messages) != 3 || r.Messages[2].ToolCallID != "call1" || r.Messages[2].Content != "found" {
			t.Error("tool pairing lost")
		}
		if err := e(providers.Chunk{Text: "answer"}); err != nil {
			return err
		}
		return e(providers.Chunk{Done: true, FinishReason: "stop"})
	}), Tools: executor(func(ctx context.Context, _ providers.ToolCall) (runtime.ToolResult, error) {
		executions++
		events, err := s.Read(ctx, "task", 0, 100)
		if err != nil || events[len(events)-1].Kind != runtime.ToolStarted {
			t.Fatal("dispatch preceded durable intent")
		}
		return runtime.ToolResult{Content: "found", Effect: runtime.NoEffect}, nil
	})}
	result, err := l.Run(context.Background(), runRequest())
	if err != nil || result.Text != "answer" || result.Turns != 2 || executions != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := telemetry.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	events, err := reopened.Read(context.Background(), "task", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.ToolStarted, runtime.ToolCompleted, runtime.TurnStarted, runtime.ModelDelta, runtime.TurnCompleted, runtime.TaskCompleted}
	if len(events) != len(want) {
		t.Fatalf("events: %+v", events)
	}
	for i, e := range events {
		if e.Kind != want[i] || e.Sequence != int64(i+1) {
			t.Fatalf("event %d: %+v", i, e)
		}
	}
	if len(events[0].Data.Messages) != 1 || len(events[2].Data.ToolCalls) != 1 {
		t.Fatal("replay content missing")
	}
}

func TestLoopFailureBoundaries(t *testing.T) {
	for _, name := range []string{"truncated", "denied", "uncertain", "last_turn", "cancel", "persistence"} {
		t.Run(name, func(t *testing.T) {
			s, _ := store(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			executions := 0
			l := runtime.Loop{Journal: s, Provider: model(func(_ context.Context, _ providers.Request, e func(providers.Chunk) error) error {
				if name == "cancel" {
					cancel()
					return ctx.Err()
				}
				if name == "truncated" {
					return e(providers.Chunk{Text: "partial"})
				}
				return emitCall(e)
			})}
			if name != "denied" {
				l.Tools = executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
					executions++
					return runtime.ToolResult{Effect: runtime.UncertainEffect}, errors.New("private backend error")
				})
			}
			if name == "persistence" {
				l.Journal = journal(func(c context.Context, n int64, e runtime.Event) error {
					if e.Kind == runtime.ToolStarted {
						return errors.New("disk full")
					}
					return s.Append(c, n, e)
				})
			}
			r := runRequest()
			if name == "last_turn" {
				r.MaxTurns = 1
			}
			_, err := l.Run(ctx, r)
			if err == nil {
				t.Fatal("failure reported success")
			}
			if (name == "uncertain" && executions != 1) || (name != "uncertain" && executions != 0) {
				t.Fatalf("unsafe execution count %d", executions)
			}
			events, err := s.Read(context.Background(), "task", 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			last := events[len(events)-1].Kind
			if name == "cancel" {
				if last != runtime.TaskCanceled {
					t.Fatal(last)
				}
			} else if name == "persistence" {
				if last != runtime.TurnCompleted {
					t.Fatal(last)
				}
			} else if last != runtime.TaskFailed {
				t.Fatal(last)
			}
		})
	}
}

func TestDuplicateTaskDoesNotDispatch(t *testing.T) {
	s, _ := store(t)
	dispatches := 0
	l := runtime.Loop{Journal: s, Provider: model(func(_ context.Context, _ providers.Request, e func(providers.Chunk) error) error {
		dispatches++
		return e(providers.Chunk{Done: true, FinishReason: "stop"})
	})}
	if _, err := l.Run(context.Background(), runRequest()); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Run(context.Background(), runRequest()); !errors.Is(err, runtime.ErrPersistence) {
		t.Fatal(err)
	}
	if dispatches != 1 {
		t.Fatal("duplicate dispatched")
	}
}
