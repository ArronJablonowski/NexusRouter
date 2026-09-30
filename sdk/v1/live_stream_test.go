package v1

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"go.yaml.in/yaml/v3"
)

func TestSDKLiveStreamAdmission(t *testing.T) {
	event := func(runtime.Event) error { t.Fatal("unexpected event"); return nil }
	text := func(string) error { t.Fatal("unexpected text"); return nil }
	for _, client := range []*Client{nil, {}} {
		out, err := client.RunLiveStream(context.Background(), Request{Version: 1}, event, text)
		if !errors.Is(err, ErrAdmission) || out.Version != 1 || out.TaskID != "" {
			t.Fatal(out, err)
		}
	}
	client, err := New(ConfigOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		ctx     context.Context
		version int
		e       func(runtime.Event) error
		t       func(string) error
	}{{nil, 1, event, text}, {context.Background(), 0, event, text}, {context.Background(), 1, nil, text}, {context.Background(), 1, event, nil}} {
		out, err := client.RunLiveStream(tc.ctx, Request{Version: tc.version}, tc.e, tc.t)
		if !errors.Is(err, ErrAdmission) || out.Version != 1 || out.TaskID != "" {
			t.Fatal(out, err)
		}
	}
}

type liveSDKProfiler struct{}

func (liveSDKProfiler) Measure(context.Context) (resources.Measurement, error) {
	return resources.Measurement{Version: 1, Snapshot: resources.Snapshot{Time: time.Now().UTC(), CPUs: 2, TotalRAM: 8 << 30, AvailableRAM: 8 << 30, Source: "sdk-fixture"}}, nil
}

func TestSDKLiveStreamForwardsBeforeCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release := make(chan struct{})
	prefix := strings.Repeat("safe answer ", 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":false}\n", prefix)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		case <-ctx.Done():
			return
		}
		fmt.Fprintln(w, `{"message":{"content":"done"},"done":true,"done_reason":"stop"}`)
	}))
	defer func() { cancel(); server.Close() }()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Hardware.AutoProfile = false
	cfg.Hardware.Concurrent = "1"
	cfg.Workers.Max = 2
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "events.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: server.URL}}
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
	client, err := New(ConfigOptions{ProjectFile: path, ResourceProfiler: liveSDKProfiler{}})
	if err != nil {
		t.Fatal(err)
	}
	var last runtime.Kind
	var text strings.Builder
	completed := false
	result, err := client.RunLiveStream(ctx, Request{Version: 1, ModelID: "chat", Prompt: "hello"}, func(e runtime.Event) error {
		last = e.Kind
		if e.Kind == runtime.TaskCompleted {
			completed = true
		}
		return nil
	}, func(chunk string) error {
		if last != runtime.ModelDelta && last != runtime.TaskCompleted {
			t.Fatal("text lacked preceding lifecycle", last)
		}
		if text.Len() == 0 {
			if completed {
				t.Fatal("text deferred until completion")
			}
			close(release)
		}
		text.WriteString(chunk)
		return nil
	})
	if err != nil || !completed || result.Version != 1 || result.TaskID == "" || result.Text != prefix+"done" || text.String() != result.Text {
		t.Fatal(result, text.String(), err)
	}
}
