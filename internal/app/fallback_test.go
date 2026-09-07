package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
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

func TestHybridFallbackMayCrossLocalityOnlyWhenPolicyAllows(t *testing.T) {
	for _, localRequired := range []bool{false, true} {
		t.Run(fmt.Sprint("local-required-", localRequired), func(t *testing.T) {
			svc, _ := autoFixture(t)
			svc.settings.Mode = "hybrid"
			svc.settings.Models[0].FailureDomain = "local-host"
			svc.settings.Models[1].Locality = "cloud"
			svc.settings.Models[1].FailureDomain = "cloud-provider"
			calls := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"z"}]}`)
					return
				}
				var request struct {
					Model string `json:"model"`
				}
				if json.NewDecoder(r.Body).Decode(&request) != nil {
					t.Fatal("invalid provider request")
				}
				calls = append(calls, request.Model)
				if request.Model == "a" {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				fmt.Fprintln(w, `{"message":{"content":"cloud fallback"},"done":true,"done_reason":"stop"}`)
			}))
			defer server.Close()
			svc.settings.Providers[0].Endpoint = server.URL

			out, err := svc.Run(context.Background(), Request{Prompt: "hello", LocalRequired: localRequired})
			if localRequired {
				if err == nil || len(calls) != 1 || calls[0] != "a" || len(out.PreviousTaskIDs) != 0 {
					t.Fatalf("private task crossed locality: %+v %v calls=%v", out, err, calls)
				}
				return
			}
			if err != nil || out.Text != "cloud fallback" || len(calls) != 2 || calls[0] != "a" || calls[1] != "z" || len(out.PreviousTaskIDs) != 1 {
				t.Fatalf("hybrid failover unavailable: %+v %v calls=%v", out, err, calls)
			}
		})
	}
}

func TestAutomaticTraversesBoundedFallbackChain(t *testing.T) {
	svc, cfg := autoFixture(t)
	middle := svc.settings.Models[0]
	middle.ID, middle.Model, middle.FailureDomain = "m", "m", "middle-domain"
	svc.settings.Models = append(svc.settings.Models, middle)
	calls := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"m"},{"name":"z"}]}`)
			return
		}
		var request struct {
			Model string `json:"model"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid provider request")
			return
		}
		calls = append(calls, request.Model)
		if request.Model != "z" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, `{"message":{"content":"third route answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL

	out, err := svc.Run(context.Background(), Request{Prompt: "hello"})
	if err != nil || out.Text != "third route answer" || fmt.Sprint(calls) != "[a m z]" || len(out.PreviousTaskIDs) != 2 {
		t.Fatalf("%+v %v calls=%v", out, err, calls)
	}
	db, err := telemetry.OpenReadOnly(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	chain := append(append([]string(nil), out.PreviousTaskIDs...), out.TaskID)
	for i, task := range chain {
		history, err := sessions.Replay(context.Background(), db, task)
		if err != nil {
			t.Fatal(err)
		}
		wantState := "failed"
		if i == len(chain)-1 {
			wantState = "completed"
		}
		if history.State != wantState {
			t.Fatal("wrong fallback state", i, history.State)
		}
		if i > 0 && history.RetryOfTaskID != chain[i-1] {
			t.Fatal("noncontiguous retry lineage", i, history.RetryOfTaskID, chain[i-1])
		}
	}
}

func TestAutomaticFallbackChainStopsAfterIntermediatePartialOutput(t *testing.T) {
	svc, _ := autoFixture(t)
	middle := svc.settings.Models[0]
	middle.ID, middle.Model, middle.FailureDomain = "m", "m", "middle-domain"
	svc.settings.Models = append(svc.settings.Models, middle)
	calls := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"m"},{"name":"z"}]}`)
			return
		}
		var request struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		calls = append(calls, request.Model)
		if request.Model == "a" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if request.Model == "m" {
			fmt.Fprintln(w, `{"message":{"content":"partial private output"},"done":false}`)
			return
		}
		t.Error("unsafe third route dispatched")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL

	out, err := svc.Run(context.Background(), Request{Prompt: "hello"})
	if err == nil || out.retryable || fmt.Sprint(calls) != "[a m]" || len(out.PreviousTaskIDs) != 1 || out.TaskID == "" {
		t.Fatalf("unsafe chain result: %+v %v calls=%v", out, err, calls)
	}
}

func TestAutomaticFallbackChainSkipsCandidateThatBecomesIneligible(t *testing.T) {
	svc, _ := autoFixture(t)
	middle := svc.settings.Models[0]
	middle.ID, middle.Model, middle.FailureDomain = "m", "m", "middle-domain"
	svc.settings.Models = append(svc.settings.Models, middle)
	calls := []string{}
	tagCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			tagCalls++
			if tagCalls == 1 {
				fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"m"},{"name":"z"}]}`)
			} else {
				fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"z"}]}`)
			}
			return
		}
		var request struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		calls = append(calls, request.Model)
		if request.Model == "a" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, `{"message":{"content":"eligible fallback"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL

	out, err := svc.Run(context.Background(), Request{Prompt: "hello"})
	if err != nil || out.Text != "eligible fallback" || fmt.Sprint(calls) != "[a z]" || len(out.PreviousTaskIDs) != 1 || tagCalls < 2 {
		t.Fatalf("%+v %v calls=%v tags=%d", out, err, calls, tagCalls)
	}
}

func TestAutomaticFallbackChainHasHardAttemptBound(t *testing.T) {
	svc, _ := autoFixture(t)
	zero := 0.0
	svc.settings.Models = nil
	inventory := struct {
		Models []map[string]string `json:"models"`
	}{}
	for i := 0; i < sessions.MaxTerminalRouteAttempts+2; i++ {
		id := fmt.Sprintf("m%02d", i)
		svc.settings.Models = append(svc.settings.Models, config.Model{ID: id, Model: id, Provider: "local", Locality: "local", Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero, RAMBytes: 100})
		inventory.Models = append(inventory.Models, map[string]string{"name": id})
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_ = json.NewEncoder(w).Encode(inventory)
			return
		}
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL

	out, err := svc.Run(context.Background(), Request{Prompt: "hello"})
	if err == nil || calls != sessions.MaxTerminalRouteAttempts || len(out.PreviousTaskIDs) != sessions.MaxTerminalRouteAttempts-1 {
		t.Fatalf("unbounded chain: calls=%d previous=%d err=%v", calls, len(out.PreviousTaskIDs), err)
	}
}
