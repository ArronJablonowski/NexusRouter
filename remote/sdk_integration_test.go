package remote

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/resources"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"go.yaml.in/yaml/v3"
)

type fixtureProfiler struct{}

func (fixtureProfiler) Measure(context.Context) (resources.Measurement, error) {
	return resources.Measurement{Version: 1, Snapshot: resources.Snapshot{Time: time.Now().UTC(), CPUs: 2, TotalRAM: 8 << 30, AvailableRAM: 8 << 30, Source: "remote-fixture"}}, nil
}

func TestRemoteSDKDispatchResultEventsAndQueuedCancellation(t *testing.T) {
	owners, e := os.MkdirTemp("", "nexus-owners-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(owners) })
	t.Setenv("DARWIN_PROCESS_OWNER_DIR", owners)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var calls atomic.Int32
	blocked := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		data, _ := io.ReadAll(r.Body)
		if strings.Contains(string(data), "block-request") {
			close(blocked)
			<-r.Context().Done()
			return
		}
		fmt.Fprintln(w, `{"message":{"content":"remote result"},"done":true,"done_reason":"stop"}`)
	}))
	defer provider.Close()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Hardware.AutoProfile = false
	cfg.Workers.Max = 1
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "tasks.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero}}
	body, e := yaml.Marshal(cfg)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	if e = os.WriteFile(path, body, 0600); e != nil {
		t.Fatal(e)
	}
	sdkClient, e := sdk.New(sdk.ConfigOptions{ProjectFile: path, ResourceProfiler: fixtureProfiler{}})
	if e != nil {
		t.Fatal(e)
	}
	f := setup(t)
	f.http.Close()
	backend := &SDKBackend{Client: sdkClient, Models: []Model{{EstimatedCost: &zero, ID: "chat", Local: true, ContextTokens: 8192}}}
	server, e := NewServer("node-a", TrustFile(f.serverTrust), f.journal, backend)
	if e != nil {
		t.Fatal(e)
	}
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	hs, e := server.HTTPServer(ln.Addr().String(), f.serverCreds)
	if e != nil {
		t.Fatal(e)
	}
	go hs.ServeTLS(ln, "", "")
	defer hs.Close()
	peer := f.serverPeer
	peer.Endpoint = "https://" + ln.Addr().String()
	writeRegistry(t, f.clientTrust, peer)
	request := "request-sdk-00001"
	first, e := f.client.Dispatch(ctx, "node-a", request, testTask())
	if e != nil || first.State != "queued" {
		t.Fatal(first, e)
	}
	retry, e := f.client.Dispatch(ctx, "node-a", request, testTask())
	if e != nil || retry.ID != first.ID {
		t.Fatal(retry, e)
	}
	stopped, e := f.client.Dispatch(ctx, "node-a", "request-cancel-001", testTask())
	if e != nil {
		t.Fatal(e)
	}
	stopped, e = f.client.Cancel(ctx, "node-a", "request-cancel-001")
	if e != nil || stopped.State != "canceled" {
		t.Fatal(stopped, e)
	}
	service, e := app.NewServiceWithProfiler(cfg, nil, fixtureProfiler{})
	if e != nil {
		t.Fatal(e)
	}
	dispatcher, e := app.StartDispatcher(ctx, service)
	if e != nil {
		t.Fatal(e)
	}
	defer dispatcher.Close()
	for {
		status, e := f.client.Status(ctx, "node-a", request)
		if e != nil {
			t.Fatal(e)
		}
		if status.State == "succeeded" {
			if status.Result == nil || status.Result.Text != "remote result" || len(status.TaskIDs) != 1 {
				t.Fatal(status)
			}
			page, e := f.client.Events(ctx, "node-a", request, status.TaskIDs[0], 0)
			if e != nil || page.Validate() != nil || len(page.Events) == 0 {
				t.Fatal(page, e)
			}
			retry, e = f.client.Dispatch(ctx, "node-a", request, testTask())
			if e != nil || retry.ID != first.ID || retry.State != "succeeded" {
				t.Fatal(retry, e)
			}
			break
		}
		if status.State == "failed" {
			t.Fatal(status)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	blocking := testTask()
	blocking.Prompt = "block-request"
	running, e := f.client.Dispatch(ctx, "node-a", "request-running-01", blocking)
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-blocked:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	running, e = f.client.Cancel(ctx, "node-a", "request-running-01")
	if e != nil || !running.CancelRequested {
		t.Fatal(running, e)
	}
	for running.State != "canceled" {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
		running, e = f.client.Status(ctx, "node-a", "request-running-01")
		if e != nil {
			t.Fatal(e)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("duplicate or canceled inference: %d", calls.Load())
	}
}
