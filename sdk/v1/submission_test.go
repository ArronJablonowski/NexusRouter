package v1_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
	"go.yaml.in/yaml/v3"
)

func TestSDKSubmissionGuardsNoStorageCreation(t *testing.T) {
	ctx := context.Background()
	for _, client := range []*sdk.Client{nil, {}} {
		_, a := client.Submit(ctx, "0123456789abcdef", sdk.Request{Version: 1})
		_, b := client.SubmissionStatus(ctx, "id")
		_, c := client.CancelSubmission(ctx, "id")
		_, d := client.ListSubmissions(ctx, submissions.ListOptions{Limit: 25})
		for _, err := range []error{a, b, c, d} {
			if !errors.Is(err, sdk.ErrAdmission) {
				t.Fatal(err)
			}
		}
	}
	path := filepath.Join(t.TempDir(), "absent.db")
	client, err := sdk.New(sdk.ConfigOptions{Overrides: map[string]string{"telemetry.database": path}})
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []int{0, 2} {
		if _, err = client.Submit(ctx, "0123456789abcdef", sdk.Request{Version: version, ModelID: "chat", Prompt: "private prompt"}); !errors.Is(err, sdk.ErrAdmission) {
			t.Fatal(err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, badctx := range []context.Context{nil, canceled} {
		_, a := client.Submit(badctx, "0123456789abcdef", sdk.Request{Version: 1, ModelID: "chat", Prompt: "private prompt"})
		_, b := client.SubmissionStatus(badctx, "id")
		_, c := client.CancelSubmission(badctx, "id")
		_, d := client.ListSubmissions(badctx, submissions.ListOptions{Limit: 25})
		for _, err := range []error{a, b, c, d} {
			if err == nil {
				t.Fatal("context accepted")
			}
		}
	}
	if _, err = client.SubmissionStatus(ctx, "missing"); err == nil {
		t.Fatal("missing status accepted")
	}
	if _, err = client.CancelSubmission(ctx, "missing"); err == nil {
		t.Fatal("missing cancel accepted")
	}
	page, err := client.ListSubmissions(ctx, submissions.ListOptions{Limit: 25})
	if err != nil || len(page.Items) != 0 {
		t.Fatal(page, err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created database", err)
	}
}

func TestSDKSubmissionRestartAndDispatcherInteroperate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
			return
		}
		calls.Add(1)
		fmt.Fprintln(w, `{"message":{"content":"queued answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer provider.Close()
	defer cancel()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Hardware.AutoProfile = false
	cfg.Workers.Max = 1
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "queued.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero}}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	profiler := sdkFixtureProfiler(func(context.Context) (resources.Measurement, error) { return sdkGoodMeasurement(), nil })
	client, err := sdk.New(sdk.ConfigOptions{ProjectFile: path, ResourceProfiler: profiler})
	if err != nil {
		t.Fatal(err)
	}
	request := sdk.Request{Version: 1, ModelID: "chat", Prompt: "private queued prompt"}
	first, err := client.Submit(ctx, "0123456789abcdef", request)
	if err != nil || first.State != "queued" || first.ID == "" || calls.Load() != 0 {
		t.Fatal(first, err)
	}
	restarted, err := sdk.New(sdk.ConfigOptions{ProjectFile: path, ResourceProfiler: profiler})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := restarted.Submit(ctx, "0123456789abcdef", request)
	if err != nil || retry.ID != first.ID {
		t.Fatal(retry, err)
	}
	changed := request
	changed.Prompt = "different prompt"
	if _, err = restarted.Submit(ctx, "0123456789abcdef", changed); !errors.Is(err, submissions.ErrConflict) {
		t.Fatal(err)
	}
	canceled, err := restarted.Submit(ctx, "fedcba9876543210", changed)
	if err != nil {
		t.Fatal(err)
	}
	canceled, err = restarted.CancelSubmission(ctx, canceled.ID)
	if err != nil || canceled.State != "canceled" {
		t.Fatal(canceled, err)
	}
	page, err := restarted.ListSubmissions(ctx, submissions.ListOptions{Limit: 25})
	if err != nil || len(page.Items) != 2 {
		t.Fatal(page, err)
	}
	metadata, err := json.Marshal(page)
	if err != nil || strings.Contains(string(metadata), request.Prompt) || strings.Contains(string(metadata), changed.Prompt) {
		t.Fatal("request leaked in metadata", err)
	}
	queued, err := restarted.SubmissionStatus(ctx, first.ID)
	if err != nil || queued.State != "queued" || calls.Load() != 0 {
		t.Fatal(queued, err)
	}
	service, err := app.NewServiceWithProfiler(cfg, nil, profiler)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := app.StartDispatcher(ctx, service)
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := restarted.SubmissionStatus(ctx, first.ID)
		if err != nil {
			t.Fatal(err)
		}
		if status.State == "succeeded" {
			if status.Result == nil || status.Result.Text != "queued answer" || len(status.TaskIDs) != 1 {
				t.Fatal(status)
			}
			break
		}
		if status.State == "failed" || status.State == "canceled" {
			t.Fatal(status)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	if err = dispatcher.Close(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("not exactly one execution", calls.Load())
	}
	canceled, err = restarted.SubmissionStatus(ctx, canceled.ID)
	if err != nil || canceled.State != "canceled" || len(canceled.TaskIDs) != 0 {
		t.Fatal(canceled, err)
	}
}
