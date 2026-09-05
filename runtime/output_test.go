package runtime_test

import (
	"context"
	"errors"
	"testing"

	"darwinrouter/providers"
	"darwinrouter/runtime"
)

func TestRequiredFinalTextRejectsBlank(t *testing.T) {
	for _, text := range []string{"", " \n\t", "\u2003"} {
		s, _ := store(t)
		l := runtime.Loop{Journal: s, Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			if text != "" {
				if err := emit(providers.Chunk{Text: text}); err != nil {
					return err
				}
			}
			return emit(providers.Chunk{Done: true, FinishReason: "stop"})
		})}
		r := runRequest()
		r.RequireText = true
		if _, err := l.Run(context.Background(), r); !errors.Is(err, runtime.ErrEmptyOutput) {
			t.Fatal(err)
		}
		events, err := s.Read(context.Background(), r.TaskID, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		last := events[len(events)-1]
		if last.Kind != runtime.TaskFailed || last.Data.Code != "empty_output" {
			t.Fatalf("%+v", last)
		}
		for _, e := range events {
			if e.Kind == runtime.TaskCompleted {
				t.Fatal("blank answer marked complete")
			}
		}
		assessment := events[len(events)-2]
		if assessment.Kind != runtime.EvaluationRecorded || assessment.Data.Accepted == nil || *assessment.Data.Accepted || assessment.Data.Code != "deterministic.nonempty_text.v1" {
			t.Fatalf("missing deterministic evidence: %+v", assessment)
		}
	}
}

func TestTextRequirementAllowsToolOnlyIntermediateTurn(t *testing.T) {
	s, _ := store(t)
	turns := 0
	l := runtime.Loop{Journal: s, Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		turns++
		if turns == 1 {
			return emitCall(emit)
		}
		if err := emit(providers.Chunk{Text: "answer"}); err != nil {
			return err
		}
		return emit(providers.Chunk{Done: true, FinishReason: "stop"})
	}), Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
		return runtime.ToolResult{Content: "found", Effect: runtime.NoEffect}, nil
	})}
	r := runRequest()
	r.RequireText = true
	out, err := l.Run(context.Background(), r)
	if err != nil || out.Text != "answer" || turns != 2 {
		t.Fatalf("%+v %v", out, err)
	}
	events, err := s.Read(context.Background(), r.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	checks := 0
	for _, e := range events {
		if e.Kind == runtime.EvaluationRecorded {
			checks++
			if e.Data.Accepted == nil || !*e.Data.Accepted || e.Data.Code != "deterministic.nonempty_text.v1" {
				t.Fatal("invalid positive check", e)
			}
		}
	}
	if checks != 1 {
		t.Fatal("tool-only turn counted as final-output evidence", checks)
	}
}
