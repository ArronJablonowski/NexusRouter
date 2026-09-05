package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"darwinrouter/internal/app"
	"darwinrouter/internal/config"
	"darwinrouter/providers"
	"darwinrouter/runtime"
	"go.yaml.in/yaml/v3"
)

func TestCLISteersRunningTaskThroughDurableStorage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	entered, release := make(chan struct{}), make(chan struct{})
	requests := make(chan []providers.Message, 2)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []providers.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "invalid", 400)
			return
		}
		select {
		case requests <- request.Messages:
		case <-ctx.Done():
			return
		}
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return
			case <-r.Context().Done():
				return
			}
			fmt.Fprintln(w, `{"message":{"content":"original answer"},"done":true,"done_reason":"stop"}`)
		} else {
			fmt.Fprintln(w, `{"message":{"content":"revised answer"},"done":true,"done_reason":"stop"}`)
		}
	}))
	defer provider.Close()
	defer cancel()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "running.db")
	cfg.Providers = []config.Provider{{ID: "fixture", Kind: "ollama", Endpoint: provider.URL}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "chat", Provider: "fixture", Model: "fixture", Locality: "local", Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero, RAMBytes: 1}}
	configPath := filepath.Join(t.TempDir(), "settings.yaml")
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(configPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	svc, err := app.NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan string, 1)
	done := make(chan struct {
		result app.Result
		err    error
	}, 1)
	go func() {
		result, err := svc.RunStream(ctx, app.Request{ModelID: "chat", Prompt: "initial instruction"}, func(e runtime.Event) error {
			if e.Kind == runtime.TaskStarted {
				started <- e.TaskID
			}
			return nil
		})
		done <- struct {
			result app.Result
			err    error
		}{result, err}
	}()
	var task string
	select {
	case task = <-started:
	case result := <-done:
		t.Fatal("task did not start", result.err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("provider did not start")
	}
	const guidance = "Please revise the answer to include the operator guidance."
	var out, errout bytes.Buffer
	if code := RunWithInput([]string{"steer", "--config", configPath, "--task", task, "--key", "operator-once"}, strings.NewReader(guidance), &out, &errout, "test"); code != 0 {
		t.Fatal(code, errout.String())
	}
	var receipt runtime.SteeringReceipt
	if json.Unmarshal(out.Bytes(), &receipt) != nil || receipt.State != "pending" || receipt.ID == "" || strings.Contains(out.String(), guidance) {
		t.Fatal(out.String())
	}
	out.Reset()
	if code := RunWithInput([]string{"steering", "list", "--db", cfg.Telemetry.Database, "--task", task}, nil, &out, &errout, "test"); code != 0 {
		t.Fatal(code, errout.String())
	}
	var pending []runtime.SteeringReceipt
	if json.Unmarshal(out.Bytes(), &pending) != nil || len(pending) != 1 || pending[0].State != "pending" || strings.Contains(out.String(), guidance) {
		t.Fatal(out.String())
	}
	close(release)
	select {
	case finished := <-done:
		if finished.err != nil || finished.result.Text != "revised answer" || finished.result.Turns != 2 {
			t.Fatal(finished.result, finished.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	first, second := <-requests, <-requests
	if len(first) != 1 || first[0].Content != "initial instruction" {
		t.Fatal(first)
	}
	found := false
	for _, m := range second {
		if m.Role == "user" && m.Content == guidance {
			found = true
		}
	}
	if !found || calls.Load() != 2 {
		t.Fatal("guidance was not delivered exactly once", second, calls.Load())
	}
	out.Reset()
	if code := RunWithInput([]string{"steering", "show", "--db", cfg.Telemetry.Database, "--task", task, "--id", receipt.ID}, nil, &out, &errout, "test"); code != 0 {
		t.Fatal(code, errout.String())
	}
	var applied runtime.SteeringMessage
	if json.Unmarshal(out.Bytes(), &applied) != nil || applied.State != "applied" || applied.Text != guidance || applied.AppliedSequence == nil {
		t.Fatal(out.String())
	}
}
