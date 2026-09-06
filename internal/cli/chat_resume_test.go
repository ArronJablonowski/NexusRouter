package cli

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func chatResumeStatus(id string) sessions.ContinuationStatus {
	return sessions.ContinuationStatus{Version: 1, TaskID: id, Sequence: 4, State: "completed", HistoryEligible: true, Reason: "completed"}
}

func chatResumeRequest(t *testing.T, requests <-chan app.Request) app.Request {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(3 * time.Second):
		t.Fatal("next chat request was not dispatched")
		return app.Request{}
	}
}

func startResumeChat(t *testing.T, base app.Request, hooks chatHooks) (chan chatLine, *chatTestOutput, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	lines := make(chan chatLine)
	out := &chatTestOutput{writes: make(chan string, 100)}
	done := make(chan int, 1)
	joined := false
	go func() { done <- runChatSession(ctx, base, hooks, lines, nil, out) }()
	t.Cleanup(func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("chat did not join")
			}
		}
	})
	finish := func() {
		close(lines)
		select {
		case code := <-done:
			joined = true
			if code != 0 {
				t.Fatal("chat exit", code)
			}
		case <-ctx.Done():
			t.Fatal("chat timeout")
		}
	}
	return lines, out, finish
}

func TestChatResumeEligibleSelectionDefersExecutionAndClearsCompaction(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "recovered"}[recovered], func(t *testing.T) {
			requests := make(chan app.Request, 1)
			var inspections atomic.Int64
			hooks := chatHooks{Continuation: func(ctx context.Context, id string) (sessions.ContinuationStatus, error) {
				inspections.Add(1)
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 5*time.Second || id != "selected" {
					t.Error("unbounded or foreign inspection")
				}
				status := chatResumeStatus(id)
				if recovered {
					status.State, status.Reason = "failed", "recovered_delegation"
				}
				return status, nil
			}, Run: func(_ context.Context, r app.Request, _ func(runtime.Event) error) (app.Result, error) {
				requests <- r
				return app.Result{TaskID: "new-task", Text: "resumed answer"}, nil
			}}
			base := app.Request{ContinueTaskID: "previous", SummaryAttemptID: "old-summary", Compaction: &sessions.CompactionRequest{Keep: 2}, ModelID: "brain"}
			lines, out, finish := startResumeChat(t, base, hooks)
			lines <- chatLine{Text: "/resume selected"}
			out.wait(t, "Saved context selected: selected")
			select {
			case <-requests:
				t.Fatal("resume command executed a task")
			default:
			}
			if inspections.Load() != 1 {
				t.Fatal("inspection not exactly once")
			}
			lines <- chatLine{Text: "continue with this prompt"}
			r := chatResumeRequest(t, requests)
			if r.ContinueTaskID != "selected" || r.Compaction != nil || r.SummaryAttemptID != "" || r.Prompt != "continue with this prompt" || r.ModelID != "brain" {
				t.Fatal("resume request changed authority or retained old compaction", r)
			}
			out.wait(t, "resumed answer")
			finish()
		})
	}
}

func TestChatResumeRejectedSelectionPreservesContextAndCompaction(t *testing.T) {
	for _, mode := range []string{"empty", "invalid_id", "extra_argument", "missing_hook", "error", "foreign", "invalid_status", "ineligible"} {
		t.Run(mode, func(t *testing.T) {
			requests := make(chan app.Request, 1)
			var inspections atomic.Int64
			hooks := chatHooks{Continuation: func(_ context.Context, id string) (sessions.ContinuationStatus, error) {
				inspections.Add(1)
				status := chatResumeStatus(id)
				switch mode {
				case "error":
					return status, errors.New("private-backend-failure")
				case "foreign":
					status.TaskID = "foreign"
				case "invalid_status":
					status.Version = 2
				case "ineligible":
					status.State, status.Reason, status.HistoryEligible = "running", "task_running", false
				}
				return status, nil
			}, Run: func(_ context.Context, r app.Request, _ func(runtime.Event) error) (app.Result, error) {
				requests <- r
				return app.Result{TaskID: "next", Text: "preserved answer"}, nil
			}}
			command := "/resume selected"
			switch mode {
			case "empty":
				command = "/resume"
			case "invalid_id":
				command = "/resume private:invalid-id"
			case "extra_argument":
				command = "/resume selected extra"
			case "missing_hook":
				hooks.Continuation = nil
			}
			base := app.Request{ContinueTaskID: "previous", SummaryAttemptID: "old-summary", Compaction: &sessions.CompactionRequest{Keep: 2}}
			lines, out, finish := startResumeChat(t, base, hooks)
			lines <- chatLine{Text: command}
			out.wait(t, "Saved context not selected")
			select {
			case <-requests:
				t.Fatal("denied resume executed")
			default:
			}
			wantCalls := int64(1)
			if mode == "empty" || mode == "invalid_id" || mode == "extra_argument" || mode == "missing_hook" {
				wantCalls = 0
			}
			if inspections.Load() != wantCalls {
				t.Fatal("invalid input reached inspection", inspections.Load())
			}
			lines <- chatLine{Text: "use unchanged context"}
			r := chatResumeRequest(t, requests)
			if r.ContinueTaskID != base.ContinueTaskID || r.SummaryAttemptID != base.SummaryAttemptID || !reflect.DeepEqual(r.Compaction, base.Compaction) {
				t.Fatal("rejected resume discarded prior context", r)
			}
			out.wait(t, "preserved answer")
			finish()
			if strings.Contains(out.text.String(), "private-backend") || strings.Contains(out.text.String(), "private:invalid-id") {
				t.Fatal("resume disclosed raw errors or invalid input")
			}
		})
	}
}

func TestChatResumeClearsPriorFeedbackTarget(t *testing.T) {
	var feedback atomic.Int64
	hooks := chatHooks{Run: func(context.Context, app.Request, func(runtime.Event) error) (app.Result, error) {
		return app.Result{TaskID: "old-answer", Text: "first answer"}, nil
	}, Continuation: func(_ context.Context, id string) (sessions.ContinuationStatus, error) {
		return chatResumeStatus(id), nil
	}, Feedback: func(context.Context, string, bool, float64) error { feedback.Add(1); return nil }}
	lines, out, finish := startResumeChat(t, app.Request{}, hooks)
	lines <- chatLine{Text: "initial prompt"}
	out.wait(t, "first answer")
	lines <- chatLine{Text: "/feedback accepted 0"}
	out.wait(t, "Feedback recorded")
	lines <- chatLine{Text: "/resume selected"}
	out.wait(t, "Saved context selected")
	lines <- chatLine{Text: "/feedback accepted 0"}
	out.wait(t, "Feedback requires a successful answer")
	finish()
	if feedback.Load() != 1 {
		t.Fatal("resume retained stale feedback authority", feedback.Load())
	}
}

func TestChatResumeWhileActiveDoesNotInspectOrChangeContext(t *testing.T) {
	requests := make(chan app.Request, 2)
	release := make(chan struct{})
	var runs, inspections atomic.Int64
	hooks := chatHooks{Continuation: func(_ context.Context, id string) (sessions.ContinuationStatus, error) {
		inspections.Add(1)
		return chatResumeStatus(id), nil
	}, Run: func(ctx context.Context, r app.Request, emit func(runtime.Event) error) (app.Result, error) {
		requests <- r
		if runs.Add(1) == 1 {
			if err := emit(runtime.Event{Kind: runtime.TaskStarted, TaskID: "active"}); err != nil {
				return app.Result{}, err
			}
			select {
			case <-release:
			case <-ctx.Done():
				return app.Result{}, ctx.Err()
			}
		}
		return app.Result{TaskID: "completed-active", Text: "active answer"}, nil
	}}
	lines, out, finish := startResumeChat(t, app.Request{ContinueTaskID: "previous"}, hooks)
	lines <- chatLine{Text: "first prompt"}
	chatResumeRequest(t, requests)
	out.wait(t, "[task active]")
	lines <- chatLine{Text: "/resume selected"}
	out.wait(t, "Cannot select saved context while a task is active")
	if inspections.Load() != 0 {
		t.Fatal("active selection inspected history")
	}
	close(release)
	out.wait(t, "active answer")
	lines <- chatLine{Text: "follow up"}
	r := chatResumeRequest(t, requests)
	if r.ContinueTaskID != "completed-active" {
		t.Fatal("active resume changed continuation", r)
	}
	out.wait(t, "active answer")
	finish()
}

func TestChatResumeNewClearsSelectedSource(t *testing.T) {
	requests := make(chan app.Request, 1)
	hooks := chatHooks{Continuation: func(_ context.Context, id string) (sessions.ContinuationStatus, error) {
		return chatResumeStatus(id), nil
	}, Run: func(_ context.Context, r app.Request, _ func(runtime.Event) error) (app.Result, error) {
		requests <- r
		return app.Result{TaskID: "fresh", Text: "fresh answer"}, nil
	}}
	lines, out, finish := startResumeChat(t, app.Request{ContinueTaskID: "previous"}, hooks)
	lines <- chatLine{Text: "/resume selected"}
	out.wait(t, "Saved context selected")
	lines <- chatLine{Text: "/new"}
	out.wait(t, "New conversation")
	lines <- chatLine{Text: "fresh prompt"}
	if r := chatResumeRequest(t, requests); r.ContinueTaskID != "" {
		t.Fatal("new conversation retained resumed source", r)
	}
	out.wait(t, "fresh answer")
	finish()
}

func TestChatResumeCanceledInspectionCannotSelectEligibleResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lines := make(chan chatLine, 1)
	lines <- chatLine{Text: "/resume selected"}
	close(lines)
	out := &chatTestOutput{writes: make(chan string, 100)}
	hooks := chatHooks{Continuation: func(_ context.Context, id string) (sessions.ContinuationStatus, error) {
		cancel()
		return chatResumeStatus(id), nil
	}, Run: func(context.Context, app.Request, func(runtime.Event) error) (app.Result, error) {
		t.Error("canceled resume executed")
		return app.Result{}, errors.New("fixture")
	}}
	done := make(chan int, 1)
	go func() { done <- runChatSession(ctx, app.Request{ContinueTaskID: "previous"}, hooks, lines, nil, out) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("canceled inspection did not join")
	}
	if text := out.text.String(); !strings.Contains(text, "Saved context not selected") || strings.Contains(text, "Saved context selected:") {
		t.Fatal("canceled inspection granted selection", text)
	}
}
