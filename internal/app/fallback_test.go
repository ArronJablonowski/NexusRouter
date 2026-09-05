package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestAutomaticSafeFallbackPreservesFailedHistory(t *testing.T) {
	for _, mode := range []string{"retryable", "partial", "empty", "explicit", "budget"} {
		t.Run(mode, func(t *testing.T) {
			svc, cfg := autoFixture(t)
			calls := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"z"}]}`)
					return
				}
				var request struct {
					Model string `json:"model"`
				}
				json.NewDecoder(r.Body).Decode(&request)
				calls = append(calls, request.Model)
				if request.Model == "a" {
					switch mode {
					case "partial":
						fmt.Fprintln(w, `{"message":{"content":"partial"},"done":false}`)
					case "empty":
						fmt.Fprintln(w, `{"message":{"content":""},"done":true,"done_reason":"stop"}`)
					default:
						w.WriteHeader(503)
					}
					return
				}
				fmt.Fprintln(w, `{"message":{"content":"fallback answer"},"done":true,"done_reason":"stop"}`)
			}))
			defer server.Close()
			svc.settings.Providers[0].Endpoint = server.URL
			request := Request{Prompt: "hello"}
			if mode == "budget" {
				cost := .6
				for i := range svc.settings.Models {
					svc.settings.Models[i].EstimatedCost = &cost
				}
				request.MaxCost = 1
			}
			if mode == "explicit" {
				request.ModelID = "a"
			}
			out, err := svc.Run(context.Background(), request)
			if mode != "retryable" {
				if err == nil || len(calls) != 1 || len(out.PreviousTaskIDs) != 0 {
					t.Fatalf("unsafe retry: %+v %v calls=%v", out, err, calls)
				}
				return
			}
			if err != nil || out.Text != "fallback answer" || len(calls) != 2 || calls[1] != "z" || len(out.PreviousTaskIDs) != 1 {
				t.Fatalf("%+v %v calls=%v", out, err, calls)
			}
			db, err := telemetry.OpenReadOnly(context.Background(), cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			first, err := sessions.Replay(context.Background(), db, out.PreviousTaskIDs[0])
			if err != nil || first.State != "failed" {
				t.Fatalf("%+v %v", first, err)
			}
			events, err := db.Read(context.Background(), out.TaskID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			if events[0].Kind != runtime.TaskStarted || events[0].Data.RetryOfTaskID != first.TaskID {
				t.Fatal("retry lineage missing")
			}
			second, err := sessions.Replay(context.Background(), db, out.TaskID)
			if err != nil || second.RetryOfTaskID != first.TaskID {
				t.Fatal("replayed retry lineage missing", err)
			}
		})
	}
}
