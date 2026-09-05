package v1_test

import (
	"context"
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

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"go.yaml.in/yaml/v3"
)

type sdkFixtureProfiler func(context.Context) (resources.Measurement, error)

func (f sdkFixtureProfiler) Measure(ctx context.Context) (resources.Measurement, error) {
	return f(ctx)
}
func sdkGoodMeasurement() resources.Measurement {
	return resources.Measurement{Version: 1, Snapshot: resources.Snapshot{Time: time.Now().UTC(), CPUs: 2, TotalRAM: 8 << 30, AvailableRAM: 8 << 30, Source: "sdk-fixture"}}
}
func sdkProfilerClient(t *testing.T, endpoint string, profiler resources.Profiler, mode, locality string) (*sdk.Client, string) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Mode = mode
	cfg.Hardware.AutoProfile = false
	cfg.Hardware.Concurrent = "1"
	cfg.Workers.Max = 2
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "tasks.db")
	cfg.Providers = []config.Provider{{ID: "fixture", Kind: "ollama", Endpoint: endpoint}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "chat", Provider: "fixture", Model: "fixture", Locality: locality, RAMBytes: 1, Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero}}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	client, err := sdk.New(sdk.ConfigOptions{ProjectFile: path, ResourceProfiler: profiler})
	if err != nil {
		t.Fatal(err)
	}
	return client, cfg.Telemetry.Database
}

func TestSDKProfilerManualAdmissionAndFailures(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls.Add(1)
		fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	for _, mode := range []string{"valid", "absent", "version", "error", "panic", "stale", "unavailable", "capacity"} {
		t.Run(mode, func(t *testing.T) {
			var profiler resources.Profiler = sdkFixtureProfiler(func(context.Context) (resources.Measurement, error) {
				m := sdkGoodMeasurement()
				switch mode {
				case "version":
					m.Version = 2
				case "error":
					return m, errors.New("private profiler detail")
				case "panic":
					panic("private profiler detail")
				case "stale":
					m.Snapshot.Time = time.Now().Add(-time.Hour)
				case "unavailable":
					m.Snapshot.TotalRAM = 0
					m.Snapshot.AvailableRAM = 0
				case "capacity":
					m.Snapshot.AvailableRAM = 0
				}
				return m, nil
			})
			if mode == "absent" {
				profiler = nil
			}
			client, path := sdkProfilerClient(t, server.URL, profiler, "local_only", "local")
			before := calls.Load()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "hello"})
			if mode == "valid" {
				if err != nil || result.Text != "answer" || calls.Load() != before+1 {
					t.Fatal(result, err)
				}
				return
			}
			if err == nil || result.TaskID != "" || calls.Load() != before || strings.Contains(err.Error(), "private") {
				t.Fatal(result, err)
			}
			if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("denied profile created DB", err)
			}
		})
	}
}

func TestSDKProfilerPreservesPrivacy(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "must not dispatch", 500) }))
	defer server.Close()
	for _, pair := range [][2]string{{"local_only", "cloud"}, {"cloud_only", "local"}} {
		client, path := sdkProfilerClient(t, server.URL, sdkFixtureProfiler(func(context.Context) (resources.Measurement, error) { return sdkGoodMeasurement(), nil }), pair[0], pair[1])
		out, err := client.Run(context.Background(), sdk.Request{Version: 1, ModelID: "chat", Prompt: "hello"})
		if err == nil || out.TaskID != "" || calls.Load() != 0 {
			t.Fatal(out, err)
		}
		if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("privacy denial created DB", err)
		}
	}
}

func TestSDKProfilerKeepsSharedConcurrencyCeiling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
		case <-ctx.Done():
			return
		case <-r.Context().Done():
			return
		}
		fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	defer cancel()
	client, _ := sdkProfilerClient(t, server.URL, sdkFixtureProfiler(func(context.Context) (resources.Measurement, error) { return sdkGoodMeasurement(), nil }), "local_only", "local")
	done := make(chan error, 1)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		_, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "first"})
		done <- err
	}()
	defer func() {
		cancel()
		select {
		case <-workerDone:
		case <-time.After(time.Second):
			t.Error("first SDK run did not join")
		}
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatal("first did not dispatch", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	second, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "second"})
	if err == nil || second.TaskID != "" || calls.Load() != 1 {
		t.Fatal("injected profiler bypassed ceiling", second, err)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	third, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "third"})
	if err != nil || third.Text != "answer" || calls.Load() != 2 {
		t.Fatal("reservation not released", third, err)
	}
}
