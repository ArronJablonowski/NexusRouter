package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestChatFeedbackNeverTargetsHiddenPriorAnswer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	lines := make(chan chatLine)
	out := &chatTestOutput{writes: make(chan string, 100)}
	release := make(chan struct{})
	called := make(chan string, 4)
	runs := 0
	hooks := chatHooks{
		Run: func(ctx context.Context, r app.Request, emit func(runtime.Event) error) (app.Result, error) {
			runs++
			if err := emit(runtime.Event{Kind: runtime.TaskStarted, TaskID: "task"}); err != nil {
				return app.Result{}, err
			}
			if runs == 2 {
				select {
				case <-release:
				case <-ctx.Done():
				}
				return app.Result{}, errors.New("failed")
			}
			return app.Result{TaskID: "task", Text: "answer"}, nil
		},
		Feedback: func(ctx context.Context, task string, accepted bool, cost float64) error {
			called <- task
			return nil
		},
	}
	done := make(chan int, 1)
	go func() { done <- runChatSession(ctx, app.Request{ContinueTaskID: "initial"}, hooks, lines, nil, out) }()
	defer func() { cancel(); <-done }()
	lines <- chatLine{Text: "/feedback accepted 0"}
	out.wait(t, "requires a successful answer")
	lines <- chatLine{Text: "first"}
	out.wait(t, "answer")
	lines <- chatLine{Text: "/feedback accepted 0"}
	out.wait(t, "Feedback recorded")
	if task := <-called; task != "task" {
		t.Fatal(task)
	}
	lines <- chatLine{Text: "second"}
	out.wait(t, "[task task]")
	lines <- chatLine{Text: "/feedback rejected 0"}
	out.wait(t, "while a task is active")
	close(release)
	out.wait(t, "did not complete successfully")
	lines <- chatLine{Text: "/feedback rejected 0"}
	out.wait(t, "requires a successful answer")
	lines <- chatLine{Text: "third"}
	out.wait(t, "answer")
	lines <- chatLine{Text: "/new"}
	out.wait(t, "New conversation")
	lines <- chatLine{Text: "/feedback accepted 0"}
	out.wait(t, "requires a successful answer")
	select {
	case task := <-called:
		t.Fatal("unexpected attribution", task)
	default:
	}
}
