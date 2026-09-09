package app

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestPresentationSinkReceivesOnlyPostCommitRedactedTopLevelText(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	service, path := streamService(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `{"message":{"content":"before fixture-secret after"},"done":true,"done_reason":"stop"}`)
	})
	type observed struct{ task, session, text string }
	var got []observed
	terminal := false
	if err := installEventSink(service, runtime.EventSinkFunc(func(_ context.Context, event runtime.Event) error {
		if event.Kind == runtime.TaskCompleted {
			terminal = true
		}
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := InstallPresentationTextSink(service, func(task, session, text string) {
		if terminal {
			t.Fatal("completion flush reached provisional presentation sink")
		}
		db, err := telemetry.OpenReadOnly(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		events, err := db.Read(ctx, task, 0, 100)
		if err != nil || len(events) == 0 || events[len(events)-1].Kind != runtime.ModelDelta {
			t.Fatal("presentation arrived before matching durable lifecycle", events, err)
		}
		got = append(got, observed{task, session, text})
	}); err != nil {
		t.Fatal(err)
	}
	result, err := service.Run(ctx, Request{ModelID: "chat", Prompt: "hello"})
	if err != nil || len(got) == 0 || got[0].task != result.TaskID || got[0].session != result.TaskID {
		t.Fatal("presentation sink not attached to top-level run", result, got, err)
	}
	var text strings.Builder
	for _, item := range got {
		text.WriteString(item.text)
	}
	if text.Len() == 0 || !strings.HasPrefix("before [REDACTED] after", text.String()) || strings.Contains(text.String(), "fixture-secret") || !terminal {
		t.Fatal("presentation sink escaped redaction", text.String())
	}
}

func TestCompletionTailIsNotReplayedToFreshPresentationObserver(t *testing.T) {
	service, _ := streamService(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `{"message":{"content":"safe fixture-"},"done":true,"done_reason":"stop"}`)
	})
	var provisional []string
	if err := InstallPresentationTextSink(service, func(_, _, text string) { provisional = append(provisional, text) }); err != nil {
		t.Fatal(err)
	}
	result, err := service.Run(context.Background(), Request{ModelID: "chat", Prompt: "hello"})
	if err != nil || result.Text != "safe fixture-" || len(provisional) != 1 || provisional[0] != "safe " {
		t.Fatal("terminal redactor tail became reconnectable provisional text", result, provisional, err)
	}
}

func TestPresentationSinkPanicCannotFailCommittedTask(t *testing.T) {
	service, _ := streamService(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
	})
	if err := InstallPresentationTextSink(service, func(string, string, string) { panic("observer failure") }); err != nil {
		t.Fatal(err)
	}
	result, err := service.Run(context.Background(), Request{ModelID: "chat", Prompt: "hello"})
	if err != nil || result.Text != "answer" {
		t.Fatal("presentation observer changed task outcome", result, err)
	}
}
