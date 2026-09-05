package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"darwinrouter/internal/config"
	"darwinrouter/internal/telemetry"
	"darwinrouter/runtime"
	"darwinrouter/sessions"
)

func TestBlankApplicationAnswerCannotReceiveSuccessFeedback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":"  "},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "events.db")
	cfg.Providers = []config.Provider{{ID: "p", Kind: "ollama", Endpoint: server.URL}}
	cfg.Models = []config.Model{{ID: "m", Model: "m", Provider: "p", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}}}
	out, err := RunExplicit(context.Background(), cfg, Request{ModelID: "m", Prompt: "answer"}, nil)
	if !errors.Is(err, runtime.ErrEmptyOutput) || out.TaskID == "" {
		t.Fatalf("%+v %v", out, err)
	}
	db, err := telemetry.OpenReadOnly(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	snapshot, err := sessions.Replay(context.Background(), db, out.TaskID)
	if err != nil || snapshot.State != "failed" || snapshot.InterruptedTurn {
		t.Fatalf("%+v %v", snapshot, err)
	}
	if err := RecordFeedback(context.Background(), cfg.Telemetry.Database, out.TaskID, true, 0); !errors.Is(err, ErrAdmission) {
		t.Fatal("blank task got positive feedback", err)
	}
}
