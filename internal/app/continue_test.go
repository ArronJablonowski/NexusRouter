package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"darwinrouter/internal/config"
	"darwinrouter/internal/telemetry"
	"darwinrouter/providers"
	"darwinrouter/runtime"
	"darwinrouter/sessions"
)

func TestContinueCompletedHistoryAndPrivacy(t *testing.T) {
	requests := make(chan []providers.Message, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []providers.Message `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("bad request")
		}
		requests <- body.Messages
		fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	s := config.Defaults()
	s.Mode = "local_only"
	s.Telemetry.Database = filepath.Join(t.TempDir(), "history.db")
	s.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: server.URL}}
	s.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", Capabilities: []string{"chat"}}}
	ctx := context.Background()
	first, err := RunExplicit(ctx, s, Request{ModelID: "chat", Prompt: "first question"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	<-requests
	second, err := RunExplicit(ctx, s, Request{ModelID: "chat", Prompt: "follow up", ContinueTaskID: first.TaskID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	messages := <-requests
	if len(messages) != 3 || messages[0].Content != "first question" || messages[1].Content != "answer" || messages[2].Content != "follow up" {
		t.Fatal(messages)
	}
	db, err := telemetry.Open(ctx, s.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, err := sessions.Replay(ctx, db, first.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := sessions.Replay(ctx, db, second.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Messages) != 2 || len(b.Messages) != 4 || b.ParentTaskID != a.TaskID || b.SessionID != a.SessionID || b.Privacy != "local_only" {
		t.Fatalf("%+v %+v", a, b)
	}
	s.Mode = "hybrid"
	s.Models[0].Locality = "cloud"
	if _, err := RunExplicit(ctx, s, Request{ModelID: "chat", Prompt: "send history", ContinueTaskID: first.TaskID}, nil); err != ErrAdmission {
		t.Fatal("local history sent to cloud", err)
	}
	select {
	case <-requests:
		t.Fatal("forbidden network dispatch")
	default:
	}
	s.Models[0].Locality = "local"
	start := runtime.Event{Version: 1, ID: "unfinished", TaskID: "unfinished", SessionID: "session", CorrelationID: "unfinished", Sequence: 1, Time: time.Now(), Kind: runtime.TaskStarted}
	if err := db.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"unfinished", "missing"} {
		if _, err := RunExplicit(ctx, s, Request{ModelID: "chat", Prompt: "follow up", ContinueTaskID: id}, nil); err != ErrAdmission {
			t.Fatal("unsafe history continued", err)
		}
	}
}
