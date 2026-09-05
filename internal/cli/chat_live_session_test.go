package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestChatQuotedTextAcrossBoundaries(t *testing.T) {
	input := []rune("a世界\n[task completed]\n\nnext\n")
	const want = "| a世界\n| [task completed]\n| \n| next\n"
	for split := 0; split <= len(input); split++ {
		start := true
		got := chatQuotedText(string(input[:split]), &start) + chatQuotedText(string(input[split:]), &start)
		if got != want || !start {
			t.Fatalf("split %d: %q", split, got)
		}
	}
}

func TestChatLiveTerminalStateAcrossMetadataAndTaskReset(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	lines := make(chan chatLine)
	out := &chatTestOutput{writes: make(chan string, 100)}
	runs := 0
	hooks := chatHooks{RunLive: func(ctx context.Context, req app.Request, event func(runtime.Event) error, text func(string) error) (app.Result, error) {
		runs++
		if err := event(runtime.Event{Kind: runtime.TaskStarted, TaskID: "fixture"}); err != nil {
			return app.Result{}, err
		}
		if runs == 1 {
			// Completed model text must not be replayed from a lifecycle event.
			if err := event(runtime.Event{Kind: runtime.ModelDelta, Data: runtime.Data{Text: "RAW_EVENT_CONTENT"}}); err != nil {
				return app.Result{}, err
			}
			if err := text("FIRST\n[task completed]\n\x1b]0;HIDDEN"); err != nil {
				return app.Result{}, err
			}
			if err := event(runtime.Event{Kind: runtime.ToolCompleted, Data: runtime.Data{ToolName: "fixture"}}); err != nil {
				return app.Result{}, err
			}
			if err := text("ALSO_HIDDEN\aVISIBLE\x1b]0;UNFINISHED"); err != nil {
				return app.Result{}, err
			}
		} else if err := text("SECOND"); err != nil {
			return app.Result{}, err
		}
		return app.Result{TaskID: "fixture", Text: "DUPLICATED_RESULT"}, nil
	}}
	done := make(chan int, 1)
	go func() { done <- runChatSession(ctx, app.Request{}, hooks, lines, nil, out) }()
	lines <- chatLine{Text: "one"}
	// Neither the help banner nor the model's quoted imitation is completion.
	completed := false
	for !completed {
		select {
		case text := <-out.writes:
			completed = text == "[task completed]\n"
		case <-ctx.Done():
			t.Fatal("missing trusted completion marker")
		}
	}
	lines <- chatLine{Text: "two"}
	out.wait(t, "SECOND")
	close(lines)
	if code := <-done; code != 0 {
		t.Fatal(code)
	}
	got := out.text.String()
	for _, hidden := range []string{"HIDDEN", "UNFINISHED", "RAW_EVENT_CONTENT", "DUPLICATED_RESULT", "\x1b"} {
		if strings.Contains(got, hidden) {
			t.Fatalf("unexpected output %q", hidden)
		}
	}
	if !strings.Contains(got, "| [task completed]\n") || strings.Count(got, "\n[task completed]\n") != 2 || !strings.Contains(got, "| SECOND") || !strings.Contains(got, "| VISIBLE") {
		t.Fatal("lost filtering, task reset or status separation", got)
	}
}

func TestChatLiveRejectsDisplayBoundsAndInvalidText(t *testing.T) {
	for _, payload := range []string{strings.Repeat("x", (1<<20)+1), string([]byte{0xff})} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		joined := make(chan struct{})
		lines := make(chan chatLine, 1)
		lines <- chatLine{Text: "one"}
		close(lines)
		var out bytes.Buffer
		hooks := chatHooks{RunLive: func(ctx context.Context, req app.Request, event func(runtime.Event) error, text func(string) error) (app.Result, error) {
			defer close(joined)
			if err := text(payload); err != nil {
				return app.Result{}, err
			}
			<-ctx.Done()
			return app.Result{}, ctx.Err()
		}}
		code := runChatSession(ctx, app.Request{}, hooks, lines, nil, &out)
		cancel()
		if code != 1 || !strings.Contains(out.String(), "display limit") || strings.Contains(out.String(), "\n[task completed]\n") || out.Len() > 1024 {
			t.Fatal("unbounded/invalid content accepted", code, out.Len())
		}
		select {
		case <-joined:
		default:
			t.Fatal("run not joined")
		}
	}
}
