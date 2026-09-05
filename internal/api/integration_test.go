package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestHTTPTaskToProviderAndDurableInspection(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Error("unexpected provider path")
		}
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", token+" answer")
	}))
	defer provider.Close()
	s := config.Defaults()
	s.Mode = "local_only"
	s.Telemetry.Database = filepath.Join(t.TempDir(), "http.db")
	s.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	s.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}}}
	db, err := telemetry.Open(context.Background(), s.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, err := New(token, 1, Services{
		FeedbackHistory: func(ctx context.Context, task string) ([]evaluation.Record, error) {
			return app.FeedbackHistory(ctx, s.Telemetry.Database, task)
		},
		ReviseFeedback: func(ctx context.Context, task, expected string, accepted bool) error {
			return app.ReviseFeedback(ctx, s.Telemetry.Database, task, expected, accepted)
		},
		Run: func(ctx context.Context, r app.Request) (app.Result, error) {
			return app.RunExplicit(ctx, s, r, func(name string) string {
				if name == "DARWIN_API_TOKEN" {
					return token
				}
				return ""
			})
		},
		Inspect: func(ctx context.Context, id string) (sessions.Snapshot, error) { return sessions.Replay(ctx, db, id) },
		Health:  func(context.Context) error { return nil },
		Feedback: func(ctx context.Context, task string, accepted bool, cost float64) error {
			return app.RecordFeedback(ctx, s.Telemetry.Database, task, accepted, cost)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	r, err := http.NewRequest("POST", server.URL+"/v1/tasks", strings.NewReader(`{"model_id":"chat","prompt":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 201 || strings.Contains(string(body), token) {
		t.Fatal(response.StatusCode, string(body), err)
	}
	var result struct {
		Task string `json:"task_id"`
		Text string `json:"text"`
	}
	if json.Unmarshal(body, &result) != nil || result.Task == "" || result.Text != "[REDACTED] answer" {
		t.Fatal(string(body))
	}
	r, err = http.NewRequest("GET", server.URL+"/v1/tasks/"+result.Task, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	response, err = server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || strings.Contains(string(body), token) {
		t.Fatal(response.StatusCode, string(body), err)
	}
	var snapshot sessions.Snapshot
	if json.Unmarshal(body, &snapshot) != nil || snapshot.State != "completed" || len(snapshot.Messages) != 2 {
		t.Fatal(string(body))
	}
	for i, outcome := range []string{"accepted", "accepted", "rejected"} {
		payload, _ := json.Marshal(map[string]any{"task_id": result.Task, "outcome": outcome, "attempt_cost": 0})
		r, _ = http.NewRequest("POST", server.URL+"/v1/feedback", strings.NewReader(string(payload)))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		response, err = server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		want := 200
		if i == 2 {
			want = 409
		}
		if response.StatusCode != want {
			t.Fatalf("feedback status=%d want=%d", response.StatusCode, want)
		}
	}
	fitness, err := db.Fitness(context.Background(), routing.Key{Model: "fixture", Provider: "local", Domain: "general", Profile: "default"})
	if err != nil || fitness.Samples != 1 || fitness.Quality != 1 {
		t.Fatalf("%+v %v", fitness, err)
	}
	r, _ = http.NewRequest("GET", server.URL+"/v1/feedback/"+result.Task, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	response, err = server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	var history []evaluation.Record
	err = json.NewDecoder(response.Body).Decode(&history)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || len(history) != 1 {
		t.Fatalf("%+v %v", history, err)
	}
	for i := 0; i < 2; i++ {
		payload, _ := json.Marshal(map[string]string{"task_id": result.Task, "expected_id": history[0].ID, "outcome": "rejected"})
		r, _ = http.NewRequest("POST", server.URL+"/v1/feedback/revisions", strings.NewReader(string(payload)))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		response, err = server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatal(response.StatusCode)
		}
	}
	fitness, err = db.Fitness(context.Background(), routing.Key{Model: "fixture", Provider: "local", Domain: "general", Profile: "default"})
	if err != nil || fitness.Samples != 1 || fitness.Quality != 0 {
		t.Fatalf("revised %+v %v", fitness, err)
	}
}
