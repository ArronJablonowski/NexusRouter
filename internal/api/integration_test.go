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

	"darwinrouter/internal/app"
	"darwinrouter/internal/config"
	"darwinrouter/internal/telemetry"
	"darwinrouter/sessions"
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
	s.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", Capabilities: []string{"chat"}}}
	db, err := telemetry.Open(context.Background(), s.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, err := New(token, 1, Services{
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
}
