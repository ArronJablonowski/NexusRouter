package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"go.yaml.in/yaml/v3"
)

type fixtureProfiler struct{}

func (fixtureProfiler) Measure(context.Context) (resources.Measurement, error) {
	return resources.Measurement{Version: 1, Snapshot: resources.Snapshot{Time: time.Now().UTC(), CPUs: 2, TotalRAM: 8 << 30, AvailableRAM: 8 << 30, Source: "remote-fixture"}}, nil
}

func TestRemoteSDKDispatchResultEventsAndQueuedCancellation(t *testing.T) {
	remoteSDKLifecycle(t, false, false)
}

func TestRemoteSDKSSHInterruptedResponse(t *testing.T) {
	if os.Getenv("NEXUS_REMOTE_SSH_NATIVE") != "1" {
		t.Skip("requires native SSH qualification")
	}
	remoteSDKLifecycle(t, true, false)
}

func TestRemoteSDKNativeHarnessSSH(t *testing.T) {
	if os.Getenv("NEXUS_PI_NATIVE") != "1" || os.Getenv("NEXUS_REMOTE_SSH_NATIVE") != "1" {
		t.Skip("requires installed Pi and isolated SSH qualification")
	}
	remoteSDKLifecycle(t, true, true)
}

func remoteSDKLifecycle(t *testing.T, interruptedSSH, native bool, registrations ...config.NativeHarness) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var calls atomic.Int32
	blocked := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/tags" {
			fmt.Fprint(w, `{"models":[{"name":"fixture"}]}`)
			return
		}
		calls.Add(1)
		data, _ := io.ReadAll(r.Body)
		if strings.Contains(string(data), "block-request") {
			close(blocked)
			<-r.Context().Done()
			return
		}
		fmt.Fprintln(w, `{"model":"fixture","message":{"role":"assistant","content":"remote result"},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":4}`)
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
	task := testTask()
	if native {
		var registration config.NativeHarness
		if len(registrations) == 0 {
			executable, err := exec.LookPath("pi")
			if err != nil {
				t.Fatal(err)
			}
			bytes, err := os.ReadFile(executable)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(bytes)
			registration = config.NativeHarness{ID: "pi-fixture", Kind: "pi", ModelID: "chat", Executable: executable, ExecutableSHA256: hex.EncodeToString(sum[:]), ModelRevision: "fixture-v1", MaxOutputTokens: 1024, OverheadRAMBytes: 64 << 20, Prices: &config.NativeHarnessPrices{}}
		} else {
			registration = registrations[0]
		}
		cfg.Tools.Enabled = false
		cfg.NativeHarnessEvidenceDir = filepath.Join(t.TempDir(), "evidence")
		cfg.NativeHarnesses = []config.NativeHarness{registration}
		if registration.Kind == "openhands" {
			cfg.Models[0].ContextTokens = 16384
		}
		task.ContextTokens = cfg.Models[0].ContextTokens
		task.HarnessID = cfg.NativeHarnesses[0].ID
		task.HarnessDifficulty = "hard"
	}
	body, e := yaml.Marshal(cfg)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	if e = os.WriteFile(path, body, 0600); e != nil {
		t.Fatal(e)
	}
	var ledger *harness.EvidenceStore
	if native {
		ledger, e = harness.OpenEvidenceStore(cfg.NativeHarnessEvidenceDir)
		if e != nil {
			t.Fatal(e)
		}
		defer ledger.Close()
	}
	sdkClient, e := sdk.New(sdk.ConfigOptions{ProjectFile: path, ResourceProfiler: fixtureProfiler{}, HarnessEvidence: ledger})
	if e != nil {
		t.Fatal(e)
	}
	f := setup(t)
	f.http.Close()
	backend := &SDKBackend{Client: sdkClient, Models: []Model{{EstimatedCost: &zero, ID: "chat", Local: true, ContextTokens: cfg.Models[0].ContextTokens}}}
	if native {
		backend.Harnesses = []Harness{{ID: task.HarnessID, ModelID: "chat", Kind: cfg.NativeHarnesses[0].Kind, ModelRevision: "fixture-v1"}}
		authorized := f.clientPeer
		authorized.Harnesses = []string{task.HarnessID}
		writeRegistry(t, f.serverTrust, authorized)
	}
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
	var dropped, droppedCancel atomic.Bool
	if interruptedSSH {
		original := hs.Handler
		hs.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" && r.URL.Path == "/v1/remote/tasks/request-sdk-00001" && dropped.CompareAndSwap(false, true) {
				original.ServeHTTP(&disconnectResponse{ResponseWriter: w}, r)
				return
			}
			if r.Method == "POST" && r.URL.Path == "/v1/remote/tasks/request-cancel-001/cancel" && droppedCancel.CompareAndSwap(false, true) {
				original.ServeHTTP(&disconnectResponse{ResponseWriter: w}, r)
				return
			}
			original.ServeHTTP(w, r)
		})
	}
	go hs.ServeTLS(ln, "", "")
	defer hs.Close()
	peer := f.serverPeer
	peer.Endpoint = "https://" + ln.Addr().String()
	if native {
		peer.Harnesses = []string{task.HarnessID}
	}
	if interruptedSSH {
		sshConfig := nativeSSHServer(t)
		peer.Transport = "ssh"
		peer.SSH = &sshConfig
	}
	writeRegistry(t, f.clientTrust, peer)
	request := "request-sdk-00001"
	first, e := f.client.Dispatch(ctx, "node-a", request, task)
	if interruptedSSH {
		if e == nil || !dropped.Load() {
			t.Fatal("connection loss not observed", first, e)
		}
		committed, lookupErr := f.journal.lookup(ctx, "node-b", request)
		if lookupErr != nil || committed == "" {
			t.Fatal("network cut preceded durable binding", committed, lookupErr)
		}
		queued, lookupErr := sdkClient.SubmissionStatus(ctx, committed)
		if lookupErr != nil || queued.State != "queued" || calls.Load() != 0 {
			t.Fatal("durable intake missing", queued, lookupErr)
		}
		// Intake committed before the network cut. Reuse the original key/payload.
		// The recovered response must name that already committed submission.
		first, e = f.client.Dispatch(ctx, "node-a", request, task)
		if first.ID != committed {
			t.Fatal("retry created another submission", first, committed, e)
		}
	}
	if e != nil || first.State != "queued" {
		t.Fatal(first, e)
	}
	retry, e := f.client.Dispatch(ctx, "node-a", request, task)
	if e != nil || retry.ID != first.ID {
		t.Fatal(retry, e)
	}
	stopped, e := f.client.Dispatch(ctx, "node-a", "request-cancel-001", task)
	if e != nil {
		t.Fatal(e)
	}
	stopped, e = f.client.Cancel(ctx, "node-a", "request-cancel-001")
	if interruptedSSH {
		if e == nil || !droppedCancel.Load() {
			t.Fatal("cancellation response not interrupted", stopped, e)
		}
		status, statusErr := f.client.Status(ctx, "node-a", "request-cancel-001")
		if statusErr != nil || status.State != "canceled" || !status.CancelRequested {
			t.Fatal("lost cancellation commit", status, statusErr)
		}
		stopped, e = f.client.Cancel(ctx, "node-a", "request-cancel-001")
	}

	if e != nil || stopped.State != "canceled" {
		t.Fatal(stopped, e)
	}
	service, e := app.NewServiceWithProfiler(cfg, nil, fixtureProfiler{})
	if e != nil {
		t.Fatal(e)
	}
	service.ConfigureHarnessEvidence(ledger)
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
			if native && status.Result.HarnessEvidenceStatus != "recorded" {
				t.Fatal("missing actual harness evidence", status)
			}
			page, e := f.client.Events(ctx, "node-a", request, status.TaskIDs[0], 0)
			if e != nil || page.Validate() != nil || len(page.Events) == 0 {
				t.Fatal(page, e)
			}
			if native {
				actual, err := runtime.ValidateHarnessOutcome(page.Events, status.TaskIDs[0])
				if err != nil || actual.Actual.Harness != cfg.NativeHarnesses[0].Kind || actual.Actual.Model != "fixture" || actual.Actual.ModelRevision != "fixture-v1" || actual.Task.Difficulty != "hard" {
					t.Fatal(actual, err)
				}
			}
			retry, e = f.client.Dispatch(ctx, "node-a", request, task)
			if e != nil || retry.ID != first.ID || retry.State != "succeeded" {
				t.Fatal(retry, e)
			}
			break
		}
		if status.State == "failed" {
			if len(status.TaskIDs) > 0 {
				page, err := f.client.Events(ctx, "node-a", request, status.TaskIDs[0], 0)
				t.Logf("failed fixture events: %+v (error %v)", page, err)
			}
			t.Fatalf("failed submission: %+v result: %+v", status, status.Result)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	blocking := task
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
