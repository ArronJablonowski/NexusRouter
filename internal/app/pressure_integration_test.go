package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/resources"
)

func TestPressureWaitDoesNotShortenAdmittedProviderExecution(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(fmt.Sprint(automatic), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			firstEntered, secondEntered := make(chan struct{}), make(chan struct{})
			firstRelease, secondRelease := make(chan struct{}), make(chan struct{})
			secondProfile := make(chan struct{})
			var calls, profiles atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"z"}]}`)
					return
				}
				if r.URL.Path != "/api/chat" {
					t.Error("unexpected provider operation")
					http.Error(w, "unexpected", 500)
					return
				}
				_, _ = io.Copy(io.Discard, r.Body)
				var released <-chan struct{}
				switch calls.Add(1) {
				case 1:
					close(firstEntered)
					released = firstRelease
				case 2:
					close(secondEntered)
					released = secondRelease
				default:
					t.Error("duplicate provider execution")
					return
				}
				select {
				case <-released:
					fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
				case <-r.Context().Done():
				case <-ctx.Done():
				}
			}))
			defer func() { cancel(); provider.Close() }()
			_, cfg := autoFixture(t)
			cfg.Providers[0].Endpoint = provider.URL
			cfg.Hardware.Concurrent = "1"
			cfg.Hardware.LocalPressurePolicy = "wait"
			cfg.Hardware.LocalQueueTimeout = "1s"
			cfg.Workers.Max = 2
			s, err := NewService(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			s.profile = func(context.Context) (resources.Snapshot, error) {
				if profiles.Add(1) == 2 {
					close(secondProfile)
				}
				return resources.Snapshot{Time: time.Now(), CPUs: 4, TotalRAM: 1000, AvailableRAM: 1000}, nil
			}
			type outcome struct {
				result Result
				err    error
			}
			first, second := make(chan outcome, 1), make(chan outcome, 1)
			go func() {
				result, err := s.Run(ctx, Request{ModelID: "a", Prompt: "first"})
				first <- outcome{result, err}
			}()
			select {
			case <-firstEntered:
			case <-ctx.Done():
				t.Fatal("first task did not execute")
			}
			model := "a"
			if automatic {
				model = "auto"
			}
			go func() {
				result, err := s.Run(ctx, Request{ModelID: model, Prompt: "second"})
				second <- outcome{result, err}
			}()
			select {
			case <-secondProfile:
			case out := <-second:
				t.Fatal("second task did not reach capacity admission", out.err)
			case <-ctx.Done():
				t.Fatal("second admission did not run")
			}
			if calls.Load() != 1 {
				t.Fatal("second task bypassed occupied capacity")
			}
			close(firstRelease)
			if out := <-first; out.err != nil {
				t.Fatal(out.err)
			}
			select {
			case <-secondEntered:
			case out := <-second:
				t.Fatal("waiting task failed before provider dispatch", out.err)
			case <-ctx.Done():
				t.Fatal("waiting task was not admitted")
			}
			// Stay in inference past the complete queue allowance, not merely
			// its remaining time. Only the original caller context should apply.
			timer := time.NewTimer(1100 * time.Millisecond)
			defer timer.Stop()
			select {
			case out := <-second:
				t.Fatal("queue deadline ended admitted inference", out.err)
			case <-timer.C:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			close(secondRelease)
			out := <-second
			if out.err != nil || out.result.Text != "answer" || calls.Load() != 2 {
				t.Fatal(out, calls.Load())
			}
			counts, err := s.Metrics(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, group := range counts.Groups {
				if group.Name == "tasks" {
					for _, count := range group.Counts {
						want := int64(0)
						if count.State == "completed" {
							want = 2
						}
						if count.Value != want {
							t.Fatal("pressure retry created extra durable tasks", count)
						}
					}
				}
			}
		})
	}
}
