package v1_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"go.yaml.in/yaml/v3"
)

func TestSDKEventsGuardsNoCreate(t *testing.T) {
	ctx := context.Background()
	for _, client := range []*sdk.Client{nil, {}} {
		page, err := client.ReadEvents(ctx, "task", 0, 2)
		if !errors.Is(err, sdk.ErrAdmission) || len(page.Events) != 0 || page.TaskID != "" {
			t.Fatal(page, err)
		}
	}
	path := filepath.Join(t.TempDir(), "absent.db")
	client, err := sdk.New(sdk.ConfigOptions{Overrides: map[string]string{"telemetry.database": path}})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, tc := range []struct {
		ctx   context.Context
		task  string
		after int64
		limit int
	}{
		{nil, "task", 0, 2}, {canceled, "task", 0, 2}, {ctx, "", 0, 2}, {ctx, "bad/task", 0, 2}, {ctx, "task", -1, 2}, {ctx, "task", 0, 0}, {ctx, "task", 0, 101}, {ctx, "task", 0, 2},
	} {
		page, err := client.ReadEvents(tc.ctx, tc.task, tc.after, tc.limit)
		if err == nil || len(page.Events) != 0 || page.TaskID != "" {
			t.Fatal(page, err)
		}
		if tc.ctx == canceled && !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation identity", err)
		}
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("database created", err)
	}
}

func TestSDKEventsPaginationRestartRedactionAndCorruption(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	var calls atomic.Int32
	const secret = "sdk-event-fixture-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
			return
		}
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": "answer " + secret}, "done": true, "done_reason": "stop"})
	}))
	defer server.Close()
	defer cancel()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Hardware.AutoProfile = false
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "events.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: server.URL, APIKeyEnv: "SDK_EVENT_SECRET"}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero}}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	options := sdk.ConfigOptions{ProjectFile: path, LookupSecret: func(name string) string {
		if name == "SDK_EVENT_SECRET" {
			return secret
		}
		return ""
	}, ResourceProfiler: sdkFixtureProfiler(func(context.Context) (resources.Measurement, error) { return sdkGoodMeasurement(), nil })}
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.ReadEvents(ctx, result.TaskID, 0, 2)
	if err != nil || first.Validate() != nil || len(first.Events) != 2 || first.FromSequence != 0 || first.NextSequence != 2 || !first.HasMore || first.State != "completed" {
		t.Fatal(first, err)
	}
	restarted, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	sequence := int64(0)
	page := first
	for {
		if page.Validate() != nil || page.TaskID != result.TaskID || page.HeadSequence != first.HeadSequence || page.State != "completed" {
			t.Fatal(page)
		}
		encoded, err := json.Marshal(page)
		if err != nil || strings.Contains(string(encoded), secret) {
			t.Fatal("unredacted page", err)
		}
		for _, event := range page.Events {
			sequence++
			if event.Sequence != sequence {
				t.Fatal("out of order", event.Sequence, sequence)
			}
		}
		if !page.HasMore {
			break
		}
		page, err = restarted.ReadEvents(ctx, result.TaskID, page.NextSequence, 2)
		if err != nil {
			t.Fatal(err)
		}
	}
	if sequence != first.HeadSequence {
		t.Fatal(sequence, first.HeadSequence)
	}
	tail, err := restarted.ReadEvents(ctx, result.TaskID, sequence, 2)
	if err != nil || tail.Validate() != nil || len(tail.Events) != 0 || tail.HasMore || tail.NextSequence != sequence {
		t.Fatal(tail, err)
	}
	bad, err := restarted.ReadEvents(ctx, result.TaskID, sequence+1, 2)
	if !errors.Is(err, sessions.ErrEventCursor) || len(bad.Events) != 0 || bad.TaskID != "" {
		t.Fatal(bad, err)
	}
	if calls.Load() != 1 {
		t.Fatal("replay dispatched inference", calls.Load())
	}
	raw, err := sql.Open("sqlite", cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err = raw.ExecContext(ctx, "UPDATE events SET body=json_set(body,'$.sequence',99) WHERE task_id=? AND sequence=2", result.TaskID); err != nil {
		t.Fatal(err)
	}
	bad, err = restarted.ReadEvents(ctx, result.TaskID, 0, 2)
	if err == nil || len(bad.Events) != 0 || bad.TaskID != "" {
		t.Fatal("partial corrupted page returned", bad, err)
	}
	if calls.Load() != 1 {
		t.Fatal("corruption caused inference", calls.Load())
	}
}
