package remote

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/remoteconfig"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/submissions"
	"go.yaml.in/yaml/v3"
)

// This opt-in test owns only a temporary host directory, synthetic provider and
// reverse loopback tunnel. The supplied SSH identity must already be trusted.
func TestPhysicalTwoHostHTTPSAndSSH(t *testing.T) {
	if os.Getenv("NEXUS_REMOTE_TWO_HOST") != "1" {
		t.Skip("requires explicit physical two-host fixture")
	}
	address, user := os.Getenv("NEXUS_REMOTE_TEST_HOST"), os.Getenv("NEXUS_REMOTE_TEST_USER")
	ip, err := netip.ParseAddr(address)
	if err != nil || !ip.IsPrivate() || ip.IsLoopback() || !id(user) {
		t.Fatal("explicit private remote IP/user required")
	}
	key, known, binary, digest := os.Getenv("NEXUS_REMOTE_TEST_KEY"), os.Getenv("NEXUS_REMOTE_TEST_KNOWN_HOSTS"), os.Getenv("NEXUS_REMOTE_TEST_BINARY"), os.Getenv("NEXUS_REMOTE_TEST_BINARY_SHA256")
	for _, p := range []string{key, known, binary} {
		if !filepath.IsAbs(p) {
			t.Fatal("absolute fixture paths required")
		}
	}
	if len(digest) != 64 {
		t.Fatal("binary hash required")
	}
	ssh := []string{"-F", "/dev/null", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none", "-o", "UserKnownHostsFile=" + known, "-o", "ConnectTimeout=8", "-i", key}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	admin := func(ctx context.Context, code string, input []byte) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "ssh", append(append([]string{}, ssh...), user+"@"+address, "python3 -c "+quote(code))...)
		cmd.Stdin = bytes.NewReader(input)
		return cmd.CombinedOutput()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	var calls atomic.Int32
	blocked := make(chan struct{}, 2)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			fmt.Fprint(w, `{"models":[{"name":"fixture"}]}`)
			return
		}
		calls.Add(1)
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		body, _ := json.Marshal(request)
		if strings.Contains(string(body), "block-request") {
			blocked <- struct{}{}
			<-r.Context().Done()
			return
		}
		fmt.Fprintln(w, `{"model":"fixture","message":{"role":"assistant","content":"physical fixture result"},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":4}`)
	}))
	defer provider.Close()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	if os.Getenv("NEXUS_REMOTE_TEST_ADVERTISE_FROM_CONFIG") == "1" {
		cfg.RemoteAdvertisement = remoteconfig.Advertisement{Enabled: true, Interface: os.Getenv("NEXUS_REMOTE_TEST_ADVERTISE_INTERFACE"), Name: "node-a", SSHPort: 22}
	}
	cfg.Tools.Enabled = false
	cfg.Hardware.AutoProfile = true
	cfg.Workers.Max = 1
	cfg.Hardware.Concurrent = "1"
	cfg.Telemetry.Database = "FIXTURE_DIRECTORY/tasks.db"
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:FIXTURE_PORT"}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero}}
	registration := physicalHarnessRegistration(t)
	if registration != nil {
		cfg.NativeHarnesses = []config.NativeHarness{*registration}
		if registration.Kind == "openhands" {
			cfg.Models[0].ContextTokens = 16384
		}
		cfg.NativeHarnessEvidenceDir = "FIXTURE_DIRECTORY/evidence"
	}
	ca := newCA(t)
	sc, sp := ca.leaf(t, "node-a")
	cc, cp := ca.leaf(t, "node-b")
	files := map[string]string{}
	for name, path := range map[string]string{"cert.pem": sc.CertificateFile, "key.pem": sc.KeyFile, "ca.pem": sc.CAFile, "caller-cert.pem": cc.CertificateFile, "caller-key.pem": cc.KeyFile} {
		b, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		files[name] = base64.StdEncoding.EncodeToString(b)
	}
	files["proxy.py"] = base64.StdEncoding.EncodeToString([]byte(twoHostFaultProxy))
	cfgBody, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	files["config.yaml"] = base64.StdEncoding.EncodeToString(cfgBody)
	caller := testPeer("node-b", cp, "https://127.0.0.1:443")
	if registration != nil {
		caller.Harnesses = []string{registration.ID}
	}
	trustBody, _ := json.Marshal(Registry{Version: 1, Peers: []Peer{caller}})
	files["trust.json"] = base64.StdEncoding.EncodeToString(trustBody)
	payload, _ := json.Marshal(map[string]any{"files": files, "address": address, "binary": binary, "sha256": digest})
	data, err := admin(ctx, twoHostPrepare, payload)
	if err != nil {
		t.Fatalf("prepare %v: %s", err, data)
	}
	var host struct {
		Directory    string `json:"directory"`
		Port         int    `json:"port"`
		ProxyPort    int    `json:"proxy_port"`
		ProviderPort int    `json:"provider_port"`
	}
	if json.Unmarshal(data, &host) != nil || !strings.HasPrefix(host.Directory, "/tmp/nexus-two-host-") || host.Port < 1 || host.ProxyPort < 1 || host.ProviderPort < 1 {
		t.Fatalf("invalid fixture response %s", data)
	}
	cleanup := func() {
		c, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		b, _ := json.Marshal(map[string]string{"directory": host.Directory, "binary": binary})
		out, e := admin(c, twoHostCleanup, b)
		if e != nil {
			t.Errorf("fixture cleanup failed: %v %s", e, out)
		}
	}
	defer cleanup()
	_, localPort, _ := net.SplitHostPort(strings.TrimPrefix(provider.URL, "http://"))
	advertisedInterface := os.Getenv("NEXUS_REMOTE_TEST_ADVERTISE_INTERFACE")
	if os.Getenv("NEXUS_REMOTE_TEST_ADVERTISE_FROM_CONFIG") == "1" {
		advertisedInterface = ""
	}
	runInput, _ := json.Marshal(map[string]any{"directory": host.Directory, "binary": binary, "address": address, "port": host.Port, "proxy_port": host.ProxyPort, "advertise_interface": advertisedInterface, "fixture_path": os.Getenv("NEXUS_REMOTE_TEST_PATH")})
	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	args := append(append([]string{}, ssh...), "-o", "ExitOnForwardFailure=yes", "-R", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%s", host.ProviderPort, localPort), user+"@"+address, "python3 -c "+quote(twoHostRun))
	command := exec.CommandContext(runCtx, "ssh", args...)
	command.Stdin = bytes.NewReader(runInput)
	var logs bytes.Buffer
	command.Stdout = &logs
	command.Stderr = &logs
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	defer func() { stopRun(); <-done }()
	local := t.TempDir()
	if err := os.Chmod(local, 0700); err != nil {
		t.Fatal(err)
	}
	trust := filepath.Join(local, "peers.json")
	destination := testPeer("node-a", sp, fmt.Sprintf("https://%s:%d", address, host.Port))
	if registration != nil {
		destination.Harnesses = []string{registration.ID}
	}
	writeRegistry(t, trust, destination)
	client := &Client{Trust: TrustFile(trust), Credentials: cc}
	deadline := time.Now().Add(15 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		probe, c := context.WithTimeout(ctx, time.Second)
		_, e := client.Info(probe, "node-a")
		c()
		if e == nil {
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatal("remote host did not become ready")
	}
	if browse := os.Getenv("NEXUS_REMOTE_TEST_BROWSE_INTERFACE"); browse != "" {
		before, err := os.ReadFile(trust)
		if err != nil {
			t.Fatal(err)
		}
		candidates, err := DiscoverUnpaired(ctx, browse, 3*time.Second)
		if err != nil {
			t.Fatal("physical discovery", err)
		}
		found := false
		for _, candidate := range candidates {
			if candidate.Instance != "node-a" {
				continue
			}
			if candidate.Verified || candidate.Endpoint != destination.Endpoint || candidate.ServerName != "node-a" || candidate.ClaimedCertificateSHA256 != sp || candidate.SSHPort != 22 || !candidate.ExpiresAt.After(time.Now()) {
				t.Fatal("invalid physical discovery claim", candidate)
			}
			found = true
		}
		after, err := os.ReadFile(trust)
		if !found || err != nil || !bytes.Equal(before, after) || calls.Load() != 0 {
			t.Fatal("physical discovery missing, mutated trust or dispatched", found, err)
		}
		t.Log("physical IPv4 DNS-SD discovery returned exact unverified host/certificate/SSH hints without trust mutation or inference")
	}
	automatic := os.Getenv("NEXUS_REMOTE_TEST_AUTOMATIC") == "1"
	if automatic && registration == nil {
		t.Fatal("automatic fixture requires pinned harness")
	}
	for _, transport := range []string{"https", "ssh"} {
		t.Run(transport, func(t *testing.T) {
			destination.Transport = transport
			if transport == "ssh" {
				destination.SSH = &SSH{User: user, Port: 22, IdentityFile: key, KnownHostsFile: known}
			}
			writeRegistry(t, trust, destination)
			task := testTask()
			if registration != nil {
				task.HarnessID = registration.ID
				task.HarnessDifficulty = "hard"
				task.ContextTokens = cfg.Models[0].ContextTokens
				expected, e := client.HarnessIdentity(ctx, "node-a", HarnessIdentityRequest{ModelID: task.ModelID, HarnessID: task.HarnessID, ContextTokens: task.ContextTokens})
				if e != nil {
					t.Fatal(e)
				}
				task.ExpectedHarnessIdentity = &expected.Identity
			}
			request := "physical-" + transport + "-success-001"
			routes, err := OpenRouteStore(filepath.Join(local, "routes-"+transport))
			if err != nil {
				t.Fatal(err)
			}
			first, err := client.DispatchRecorded(ctx, routes, "node-a", request, task)
			if err != nil {
				t.Fatal(err)
			}
			wait := func(key, want string) submissions.Status {
				t.Helper()
				until := time.Now().Add(15 * time.Second)
				for time.Now().Before(until) {
					s, e := client.Status(ctx, "node-a", key)
					if e != nil {
						t.Fatal(e)
					}
					if s.State == want {
						return s
					}
					if s.State == "failed" {
						t.Fatalf("task failed %+v", s)
					}
					time.Sleep(50 * time.Millisecond)
				}
				t.Fatal("task did not reach " + want)
				return submissions.Status{}
			}
			result := wait(request, "succeeded")
			if result.Result == nil || result.Result.Text != "physical fixture result" || len(result.TaskIDs) != 1 {
				t.Fatal(result)
			}
			page, err := client.Events(ctx, "node-a", request, result.TaskIDs[0], 0)
			if err != nil || len(page.Events) == 0 {
				t.Fatal("missing durable events", err)
			}
			if registration != nil {
				actual, e := runtime.ValidateHarnessOutcome(page.Events, result.TaskIDs[0])
				if e != nil || actual.Actual != *task.ExpectedHarnessIdentity || actual.Actual.Harness != registration.Kind || actual.Actual.ModelRevision != registration.ModelRevision || actual.Task.Difficulty != "hard" {
					t.Fatal("physical harness identity mismatch", actual, e)
				}
			}
			if automatic {
				physicalAutomaticDispatch(t, ctx, client, local, transport, task, wait)
			}
			routes, err = OpenRouteStore(filepath.Join(local, "routes-"+transport))
			if err != nil {
				t.Fatal(err)
			}
			retry, err := client.DispatchRecorded(ctx, routes, "node-a", request, task)
			if err != nil || retry.ID != first.ID || retry.State != "succeeded" {
				t.Fatal("duplicate/reconnect", retry, err)
			}
			directEndpoint := destination.Endpoint
			destination.Endpoint = fmt.Sprintf("https://%s:%d", address, host.ProxyPort)
			writeRegistry(t, trust, destination)
			lostKey := "physical-" + transport + "-lost-001"
			if _, err := client.DispatchRecorded(ctx, routes, "node-a", lostKey, task); err == nil {
				t.Fatal("fault proxy did not interrupt committed response")
			}
			receiptInput, _ := json.Marshal(map[string]string{"directory": host.Directory, "binary": binary, "key": lostKey})
			receiptBody, err := admin(ctx, twoHostFaultReceipt, receiptInput)
			var committed submissions.Status
			if err != nil || json.Unmarshal(receiptBody, &committed) != nil || committed.ID == "" {
				t.Fatalf("missing independently captured committed receipt: %v %s", err, receiptBody)
			}
			routes, err = OpenRouteStore(filepath.Join(local, "routes-"+transport))
			if err != nil {
				t.Fatal(err)
			}
			recovered, err := client.DispatchRecorded(ctx, routes, "node-a", lostKey, task)
			if err != nil || recovered.ID != committed.ID {
				t.Fatal("response-loss retry duplicated submission", recovered, committed, err)
			}
			lostResult := wait(lostKey, "succeeded")
			if lostResult.Result == nil || lostResult.Result.Text != "physical fixture result" {
				t.Fatal("lost-response result missing", lostResult)
			}
			destination.Endpoint = directEndpoint
			writeRegistry(t, trust, destination)
			task.Prompt = "block-request"
			blockedKey := "physical-" + transport + "-cancel-001"
			if _, err := client.DispatchRecorded(ctx, routes, "node-a", blockedKey, task); err != nil {
				t.Fatal(err)
			}
			select {
			case <-blocked:
			case <-time.After(15 * time.Second):
				t.Fatal("provider did not start")
			}
			if _, err := client.Cancel(ctx, "node-a", blockedKey); err != nil {
				t.Fatal(err)
			}
			wait(blockedKey, "canceled")
		})
	}
	if t.Failed() {
		return
	}
	restartTask := testTask()
	if registration != nil {
		restartTask.HarnessID = registration.ID
		restartTask.HarnessDifficulty = "hard"
		restartTask.ContextTokens = cfg.Models[0].ContextTokens
		preview, e := client.HarnessIdentity(ctx, "node-a", HarnessIdentityRequest{ModelID: restartTask.ModelID, HarnessID: restartTask.HarnessID, ContextTokens: restartTask.ContextTokens})
		if e != nil {
			t.Fatal(e)
		}
		restartTask.ExpectedHarnessIdentity = &preview.Identity
	}
	physicalRestartRecovery(t, ctx, client, local, blocked, restartTask, func() {
		input, _ := json.Marshal(map[string]any{"directory": host.Directory, "binary": binary, "check_harness_children": registration != nil})
		data, err := admin(ctx, twoHostRestart, input)
		if err != nil || !validPhysicalRestart(data) {
			t.Fatalf("restart: %v %s", err, data)
		}
	})
	expectedCalls := int32(8)
	if automatic {
		expectedCalls += 2
	}
	if calls.Load() != expectedCalls {
		t.Fatal("duplicate or missing provider calls", calls.Load())
	}
	// Revoke the caller using the normal host CLI and an expected current digest.
	revokeInput, _ := json.Marshal(map[string]string{"directory": host.Directory, "binary": binary, "digest": (Registry{Version: 1, Peers: []Peer{caller}}).Digest()})
	out, err := admin(ctx, twoHostRevoke, revokeInput)
	if err != nil {
		t.Fatalf("revoke %v: %s", err, out)
	}
	if _, err := client.Info(ctx, "node-a"); err == nil {
		t.Fatal("revoked caller retained access")
	}
	t.Logf("physical HTTPS and SSH results/events, running cancellation, reopened caller-store deduplication and revocation passed; %d synthetic provider calls including response-loss and host-restart recovery", calls.Load())
}
