package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

type chatTestOutput struct {
	mu     sync.Mutex
	text   strings.Builder
	writes chan string
	fail   bool
}

type chatBrokenOutput struct {
	mode  string
	calls int
}

func (o *chatBrokenOutput) Write(b []byte) (int, error) {
	o.calls++
	if o.mode == "panic" {
		panic("private output failure")
	}
	return len(b) - 1, nil
}

func TestChatSessionLatchesShortAndPanicOutput(t *testing.T) {
	for _, mode := range []string{"short", "panic"} {
		out := &chatBrokenOutput{mode: mode}
		if code := runChatSession(context.Background(), app.Request{}, chatHooks{}, nil, nil, out); code != 1 || out.calls != 1 {
			t.Fatal(mode, code, out.calls)
		}
	}
}

func (o *chatTestOutput) Write(b []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.fail {
		return 0, errors.New("private write error")
	}
	o.text.Write(b)
	o.writes <- string(b)
	return len(b), nil
}
func (o *chatTestOutput) wait(t *testing.T, part string) {
	t.Helper()
	for {
		select {
		case text := <-o.writes:
			if strings.Contains(text, part) {
				return
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("missing output %s", part)
		}
	}
}

func TestChatSessionContinuesOnlySuccessfulTasks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lines := make(chan chatLine)
	out := &chatTestOutput{writes: make(chan string, 100)}
	requests := make(chan app.Request, 3)
	runs := 0
	hooks := chatHooks{Run: func(ctx context.Context, r app.Request, emit func(runtime.Event) error) (app.Result, error) {
		requests <- r
		runs++
		id := "success"
		if runs == 2 {
			id = "failed"
		}
		if err := emit(runtime.Event{Kind: runtime.TaskStarted, TaskID: id}); err != nil {
			return app.Result{}, err
		}
		if runs == 2 {
			return app.Result{TaskID: id}, errors.New("private provider error")
		}
		return app.Result{TaskID: id, Text: "answer"}, nil
	}}
	done := make(chan int, 1)
	go func() { done <- runChatSession(ctx, app.Request{ContinueTaskID: "initial"}, hooks, lines, nil, out) }()
	lines <- chatLine{Text: "first"}
	if r := <-requests; r.ContinueTaskID != "initial" {
		t.Fatal(r)
	}
	out.wait(t, "answer")
	lines <- chatLine{Text: "second"}
	if r := <-requests; r.ContinueTaskID != "success" {
		t.Fatal(r)
	}
	out.wait(t, "did not complete")
	lines <- chatLine{Text: "//literal"}
	if r := <-requests; r.ContinueTaskID != "success" || r.Prompt != "/literal" {
		t.Fatal(r)
	}
	out.wait(t, "answer")
	close(lines)
	if code := <-done; code != 0 {
		t.Fatal(code)
	}
	if strings.Contains(out.text.String(), "private provider") {
		t.Fatal("error leaked")
	}
}

func TestChatSessionBusySteeringAndCancelJoin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lines := make(chan chatLine)
	signals := make(chan os.Signal)
	out := &chatTestOutput{writes: make(chan string, 100)}
	joined := make(chan struct{})
	guidance := make(chan string, 1)
	hooks := chatHooks{Run: func(ctx context.Context, r app.Request, emit func(runtime.Event) error) (app.Result, error) {
		defer close(joined)
		if err := emit(runtime.Event{Kind: runtime.TaskStarted, TaskID: "task"}); err != nil {
			return app.Result{}, err
		}
		<-ctx.Done()
		return app.Result{TaskID: "task"}, ctx.Err()
	}, Steer: func(ctx context.Context, task, key, text string) (runtime.SteeringMessage, error) {
		guidance <- text
		return runtime.SteeringMessage{Version: 1, ID: "guide", TaskID: task, Text: text, State: "pending", CreatedAt: time.Now()}, nil
	}}
	done := make(chan int, 1)
	go func() { done <- runChatSession(ctx, app.Request{}, hooks, lines, signals, out) }()
	lines <- chatLine{Text: "first"}
	out.wait(t, "[task task]")
	lines <- chatLine{Text: "busy"}
	out.wait(t, "Use /steer")
	lines <- chatLine{Text: "/new"}
	out.wait(t, "Cannot reset")
	lines <- chatLine{Text: "/steer revise"}
	if text := <-guidance; text != "revise" {
		t.Fatal(text)
	}
	out.wait(t, "Guidance queued")
	signals <- os.Interrupt
	out.wait(t, "did not complete")
	select {
	case <-joined:
	default:
		t.Fatal("not joined")
	}
	lines <- chatLine{Text: "/status"}
	out.wait(t, "Idle")
	signals <- os.Interrupt
	if code := <-done; code != 0 {
		t.Fatal(code)
	}
}

func TestChatSessionEOFWaitsAndOutputFailureJoins(t *testing.T) {
	for _, mode := range []string{"eof", "output", "quit", "term"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			lines := make(chan chatLine)
			signals := make(chan os.Signal)
			out := &chatTestOutput{writes: make(chan string, 100)}
			release := make(chan struct{})
			joined := make(chan struct{})
			hooks := chatHooks{Run: func(ctx context.Context, r app.Request, emit func(runtime.Event) error) (app.Result, error) {
				defer close(joined)
				if err := emit(runtime.Event{Kind: runtime.TaskStarted, TaskID: "task"}); err != nil {
					return app.Result{}, err
				}
				select {
				case <-release:
					return app.Result{TaskID: "task", Text: "answer"}, nil
				case <-ctx.Done():
					return app.Result{}, ctx.Err()
				}
			}}
			done := make(chan int, 1)
			go func() { done <- runChatSession(ctx, app.Request{}, hooks, lines, signals, out) }()
			lines <- chatLine{Text: "first"}
			out.wait(t, "[task task]")
			switch mode {
			case "eof":
				close(lines)
				select {
				case <-done:
					t.Fatal("EOF abandoned active task")
				default:
				}
				close(release)
			case "output":
				out.mu.Lock()
				out.fail = true
				out.mu.Unlock()
				lines <- chatLine{Text: "/status"}
			case "quit":
				lines <- chatLine{Text: "/quit"}
			case "term":
				signals <- syscall.SIGTERM
			}
			select {
			case code := <-done:
				if mode == "output" && code != 1 {
					t.Fatal(code)
				}
			case <-ctx.Done():
				t.Fatal("did not stop")
			}
			select {
			case <-joined:
			default:
				t.Fatal("did not join")
			}
		})
	}
}

func TestChatSessionRejectsSteeringBeforeTaskIDAndRetainsLateControls(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lines := make(chan chatLine)
	out := &chatTestOutput{writes: make(chan string, 100)}
	starting := make(chan struct{})
	allowStart := make(chan struct{})
	late := make(chan struct{})
	finish := make(chan struct{})
	hooks := chatHooks{Run: func(ctx context.Context, r app.Request, emit func(runtime.Event) error) (app.Result, error) {
		close(starting)
		select {
		case <-allowStart:
		case <-ctx.Done():
			return app.Result{}, ctx.Err()
		}
		if err := emit(runtime.Event{Kind: runtime.TaskStarted, TaskID: "task"}); err != nil {
			return app.Result{}, err
		}
		if err := emit(runtime.Event{Kind: runtime.TaskCompleted, TaskID: "task"}); err != nil {
			return app.Result{}, err
		}
		close(late)
		select {
		case <-finish:
			return app.Result{TaskID: "task", Text: "answer"}, nil
		case <-ctx.Done():
			return app.Result{}, ctx.Err()
		}
	}, Steer: func(context.Context, string, string, string) (runtime.SteeringMessage, error) {
		return runtime.SteeringMessage{}, runtime.ErrSteeringClosed
	}}
	done := make(chan int, 1)
	go func() { done <- runChatSession(ctx, app.Request{}, hooks, lines, nil, out) }()
	lines <- chatLine{Text: "first"}
	<-starting
	lines <- chatLine{Text: "/steer early"}
	out.wait(t, "active task ID")
	close(allowStart)
	<-late
	lines <- chatLine{Text: "/status"}
	out.wait(t, "Task active: task")
	lines <- chatLine{Text: "/steer late"}
	out.wait(t, "no longer accepts steering")
	close(finish)
	out.wait(t, "answer")
	lines <- chatLine{Text: "/new"}
	out.wait(t, "New conversation")
	close(lines)
	if code := <-done; code != 0 {
		t.Fatal(code)
	}
}
