package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/accounting"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestAutomaticInvalidStreamRecoveryPreservesEvidenceAndFeedback(t *testing.T) {
	svc, cfg := autoFixture(t)
	calls := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"z"}]}`)
			return
		}
		var req struct {
			Model    string
			Messages []struct{ Role, Content string }
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("invalid request")
			return
		}
		calls = append(calls, req.Model)
		if len(req.Messages) == 0 || req.Messages[len(req.Messages)-1].Content != "preserve this request" {
			t.Error("original context lost", req.Messages)
		}
		if req.Model == "a" {
			fmt.Fprintln(w, `{"message":{"content":"untrusted incomplete answer"},"done":false}`)
			fmt.Fprintln(w, `{"error":"prediction aborted, token repeat limit reached"}`)
			return
		}
		fmt.Fprintln(w, `{"message":{"content":"recovered answer"},"done":true,"done_reason":"stop","prompt_eval_count":5,"eval_count":3}`)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	queued, err := svc.Submit(ctx, "stream-recovery-key", Request{Prompt: "preserve this request", Domain: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := StartDispatcher(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	status := awaitSubmission(t, ctx, svc, queued.ID, "succeeded")
	if err = dispatcher.Close(); err != nil {
		t.Fatal(err)
	}
	out := status.Result
	if out == nil || out.Text != "recovered answer" || fmt.Sprint(calls) != "[a z]" || len(out.PreviousTaskIDs) != 1 {
		t.Fatal("not recovered", out, calls)
	}
	db, err := telemetry.OpenReadOnly(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	histories := [][]runtime.Event{}
	for _, id := range append(append([]string{}, out.PreviousTaskIDs...), out.TaskID) {
		es, e := db.Read(context.Background(), id, 0, 100)
		if e != nil {
			t.Fatal(e)
		}
		histories = append(histories, es)
	}
	first := histories[0]
	if first[len(first)-1].Data.Code != "provider_failed_before_tools" {
		t.Fatal("missing failure boundary", first)
	}
	found := false
	for _, e := range first {
		if e.Kind == runtime.ModelDelta {
			found = true
		}
	}
	if !found {
		t.Fatal("failed stream evidence lost")
	}
	projected, err := sessions.ProjectTerminalTree(histories)
	if err != nil || projected.Result == nil || projected.Result.Text != "recovered answer" {
		t.Fatal("invalid durable lineage", projected, err)
	}
	totals, err := db.UsageTotals(context.Background(), accounting.Scope{})
	if err != nil || totals.Fallback.Records != 1 || totals.Primary.Records != 1 || totals.UnaccountedRoutedOperations != 0 || totals.Primary.UnknownUsageRecords != 1 {
		t.Fatal("incorrect usage attribution", totals, err)
	}
	if err = RecordFeedback(context.Background(), cfg.Telemetry.Database, out.PreviousTaskIDs[0], true, 0); err == nil {
		t.Fatal("failed provider became quality feedback")
	}
	if err = RecordFeedback(context.Background(), cfg.Telemetry.Database, out.TaskID, true, 0); err != nil {
		t.Fatal(err)
	}
	fitness, err := db.Fitness(context.Background(), routing.Key{Model: "z", Provider: "local", Domain: "coding", Profile: "default"})
	if err != nil || fitness.Samples != 1 || fitness.Quality != 1 {
		t.Fatal("wrong feedback attribution", fitness, err)
	}
}

func TestAutomaticStreamRecoveryLimitsAndPinning(t *testing.T) {
	for _, mode := range []string{"exhausted", "disabled", "pinned", "capabilities", "deadline", "caller-cancel", "request-timeout"} {
		t.Run(mode, func(t *testing.T) {
			svc, _ := autoFixture(t)
			var mu sync.Mutex
			calls := []string{}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"z"}]}`)
					return
				}
				var req struct{ Model string }
				_ = json.NewDecoder(r.Body).Decode(&req)
				mu.Lock()
				calls = append(calls, req.Model)
				mu.Unlock()
				if mode == "request-timeout" && req.Model == "a" || mode == "deadline" && req.Model == "z" {
					<-r.Context().Done()
					return
				}
				if mode == "caller-cancel" {
					cancel()
					return
				}
				if req.Model == "z" && mode != "exhausted" {
					fmt.Fprintln(w, `{"message":{"content":"recovered"},"done":true,"done_reason":"stop"}`)
					return
				}
				fmt.Fprintln(w, `{"message":{"content":"incomplete"},"done":false}`)
				fmt.Fprintln(w, `{"error":"prediction aborted, token repeat limit reached"}`)
			}))
			defer server.Close()
			svc.settings.Providers[0].Endpoint = server.URL
			r := Request{Prompt: "hello"}
			if mode == "pinned" {
				r.ModelID = "a"
			}
			if mode == "disabled" {
				svc.settings.Runtime.FallbackMaxAttempts = 1
			}
			if mode == "capabilities" {
				r.Capabilities = []string{"required"}
				svc.settings.Models[0].Capabilities = append(svc.settings.Models[0].Capabilities, "required")
			}
			if mode == "deadline" {
				svc.settings.Runtime.FallbackTimeout = "500ms"
			}
			if mode == "request-timeout" {
				svc.settings.Providers[0].RequestTimeout = "100ms"
			}
			started := time.Now()
			out, err := svc.Run(ctx, r)
			elapsed := time.Since(started)
			mu.Lock()
			got := fmt.Sprint(calls)
			mu.Unlock()
			want := "[a]"
			if mode == "exhausted" || mode == "deadline" || mode == "request-timeout" {
				want = "[a z]"
			}
			if got != want && !(mode == "deadline" && got == "[a]") {
				t.Fatal(mode, "dispatches", got, want, err)
			}
			if mode == "request-timeout" {
				if err != nil || out.Text != "recovered" {
					t.Fatal(out, err)
				}
			} else if err == nil {
				t.Fatal("expected terminal failure", out)
			}
			if mode == "deadline" && (elapsed > 3*time.Second || !errors.Is(err, context.DeadlineExceeded)) {
				t.Fatal("unbounded recovery deadline", elapsed, err)
			}
			if mode == "exhausted" && !errors.Is(err, ErrRecoveryExhausted) {
				t.Fatal("missing exhaustion result", err)
			}
			if mode == "caller-cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}
