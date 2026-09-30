package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestChatFeedbackRealServiceImmutableHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
			return
		}
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": "finished answer"}, "done": true, "done_reason": "stop"})
	}))
	defer provider.Close()
	defer cancel()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "feedback.db")
	cfg.Providers = []config.Provider{{ID: "fixture", Kind: "ollama", Endpoint: provider.URL}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "chat", Provider: "fixture", Model: "fixture", Locality: "local", Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero, RAMBytes: 1}}
	svc, err := app.NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer writer.Close()
	out := &chatTestOutput{writes: make(chan string, 100)}
	tasks := make(chan string, 1)
	hooks := chatHooks{
		Run: func(ctx context.Context, r app.Request, emit func(runtime.Event) error) (app.Result, error) {
			return svc.RunStream(ctx, r, func(e runtime.Event) error {
				if e.Kind == runtime.TaskStarted {
					tasks <- e.TaskID
				}
				return emit(e)
			})
		},
		Steer: svc.SteerTask,
		Feedback: func(ctx context.Context, task string, accepted bool, cost float64) error {
			return app.RecordFeedback(ctx, cfg.Telemetry.Database, task, accepted, cost)
		},
		FeedbackHistory: func(ctx context.Context, task string) ([]evaluation.Record, error) {
			return app.FeedbackHistory(ctx, cfg.Telemetry.Database, task)
		},
		ReviseFeedback: func(ctx context.Context, task, expected string, accepted bool) error {
			return app.ReviseFeedback(ctx, cfg.Telemetry.Database, task, expected, accepted)
		},
	}
	done := make(chan int, 1)
	go func() {
		done <- runChatIO(ctx, app.Request{ModelID: "chat", Domain: "creative"}, hooks, input, out, nil)
	}()
	joined := false
	defer func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("chat did not join")
			}
		}
	}()
	command := func(line, reply string) {
		t.Helper()
		if _, err := io.WriteString(writer, line+"\n"); err != nil {
			t.Fatal(err)
		}
		out.wait(t, reply)
	}
	command("a creative prompt", "finished answer")
	var task string
	select {
	case task = <-tasks:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	command("/feedback-show", "No feedback history.")
	command("/feedback rejected 0", "Feedback recorded.")
	command("/feedback rejected 0", "Feedback recorded.")
	history, err := app.FeedbackHistory(ctx, cfg.Telemetry.Database, task)
	if err != nil || len(history) != 1 || history[0].Checks[0].Passed {
		t.Fatal(history, err)
	}
	prior := history[0].ID
	command("/feedback-show", prior+" rejected")
	command("/feedback-revise "+prior+" accepted", "Feedback revised.")
	command("/feedback-revise "+prior+" accepted", "Feedback revised.")
	command("/feedback-revise "+prior+" rejected", "Feedback operation failed.")
	history, err = app.FeedbackHistory(ctx, cfg.Telemetry.Database, task)
	if err != nil || len(history) != 2 || history[0].Checks[0].Passed || !history[1].Checks[0].Passed {
		t.Fatal(history, err)
	}
	command("/feedback-show", history[1].ID+" accepted")
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		joined = true
		if code != 0 {
			t.Fatal(code)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if calls.Load() != 1 {
		t.Fatal("feedback dispatched inference", calls.Load())
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if history[0].Key.Model != "fixture" || history[0].Key.Provider != "fixture" || history[0].Key.Domain != "creative" || history[0].Key.Profile != "default" {
		t.Fatal("wrong persisted model attribution", history[0].Key)
	}
	fitness, err := db.Fitness(ctx, history[0].Key)
	if err != nil || fitness.Samples != 1 || fitness.Quality != 1 || fitness.Reliability != 1 {
		t.Fatal(fitness, err)
	}
}
