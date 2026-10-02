package remote

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/remoteconfig"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"go.yaml.in/yaml/v3"
)

// This separately opted-in qualification uses actual destination Ollama inference.
// It owns a temporary remote service/store, never a production service or model.
func TestPhysicalTwoHostLiveModel(t *testing.T) {
	if os.Getenv("NEXUS_REMOTE_LIVE_MODEL") != "1" {
		t.Skip("requires explicit live-model qualification")
	}
	model := os.Getenv("NEXUS_REMOTE_TEST_MODEL")
	ram, err := strconv.ParseUint(os.Getenv("NEXUS_REMOTE_TEST_MODEL_RAM"), 10, 64)
	if model == "" || err != nil || ram == 0 {
		t.Fatal("explicit installed model and conservative shared RAM estimate required")
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
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
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
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:11434"}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: model, Locality: "local", RAMBytes: ram, Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero}}
	if warm := os.Getenv("NEXUS_REMOTE_TEST_WARM_RAM"); warm != "" {
		warmRAM, e := strconv.ParseUint(warm, 10, 64)
		if e != nil {
			t.Fatal(e)
		}
		cfg.Providers[0].DedicatedWarmMemory = true
		cfg.Providers[0].Endpoint = os.Getenv("NEXUS_REMOTE_TEST_PROVIDER_ENDPOINT")
		cfg.Models[0].WarmRAMBytes = warmRAM
		cfg.Models[0].ResidencyDigest = os.Getenv("NEXUS_REMOTE_TEST_MODEL_DIGEST")
		if e := cfg.Validate(); e != nil {
			t.Fatal("invalid dedicated warm fixture", e)
		}
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
	advertisedInterface := os.Getenv("NEXUS_REMOTE_TEST_ADVERTISE_INTERFACE")
	if os.Getenv("NEXUS_REMOTE_TEST_ADVERTISE_FROM_CONFIG") == "1" {
		advertisedInterface = ""
	}
	runInput, _ := json.Marshal(map[string]any{"directory": host.Directory, "binary": binary, "address": address, "port": host.Port, "proxy_port": host.ProxyPort, "advertise_interface": advertisedInterface, "fixture_path": os.Getenv("NEXUS_REMOTE_TEST_PATH")})
	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	args := append(append([]string{}, ssh...), user+"@"+address, "python3 -c "+quote(twoHostRun))
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

	for _, transport := range []string{"https", "ssh"} {
		t.Run(transport, func(t *testing.T) {
			destination.Transport = transport
			if transport == "ssh" {
				destination.SSH = &SSH{User: user, Port: 22, IdentityFile: key, KnownHostsFile: known}
			}
			writeRegistry(t, trust, destination)
			routes, err := OpenRouteStore(filepath.Join(local, "live-"+transport))
			if err != nil {
				t.Fatal(err)
			}
			task := testTask()
			task.Prompt = "Reply with only the integer that equals 17 times 23. /no_think"
			task.ContextTokens = 8192
			request := "live-" + transport + "-arithmetic"
			first, err := client.DispatchRecorded(ctx, routes, "node-a", request, task)
			if err != nil {
				t.Fatal(err)
			}
			until := time.Now().Add(3 * time.Minute)
			for {
				result, e := client.Status(ctx, "node-a", request)
				if e != nil {
					t.Fatal(e)
				}
				if result.State == "failed" || result.State == "canceled" {
					t.Fatalf("live task terminal: %+v", result)
				}
				if result.State == "succeeded" {
					if result.Result == nil || len(result.TaskIDs) != 1 {
						t.Fatalf("missing live result or task identity: %+v", result)
					}
					if strings.TrimSpace(result.Result.Text) != "391" {
						t.Errorf("live model output mismatch: got %q, want 391", result.Result.Text)
					}
					page, e := client.Events(ctx, "node-a", request, result.TaskIDs[0], 0)
					if e != nil {
						t.Fatal(e)
					}
					starts := 0
					for _, event := range page.Events {
						if event.Kind == runtime.TaskStarted {
							starts++
							if event.Data.ModelID != model || event.Data.ProviderID != "local" {
								t.Fatal("wrong durable execution identity", event.Data)
							}
						}
					}
					if starts != 1 {
						t.Fatal("expected one durable task start", starts)
					}
					reopened, e := OpenRouteStore(filepath.Join(local, "live-"+transport))
					if e != nil {
						t.Fatal(e)
					}
					retry, e := client.DispatchRecorded(ctx, reopened, "node-a", request, task)
					if e != nil || retry.ID != first.ID || retry.State != "succeeded" {
						t.Fatal("replay failed", retry, e)
					}
					task.Prompt = "Different content must not replay under the same request key."
					if _, e := client.DispatchRecorded(ctx, reopened, "node-a", request, task); !errors.Is(e, ErrConflict) {
						t.Fatal("changed payload accepted under reused request key")
					}
					t.Logf("live model %s task %s: output checked, one durable start, reopened caller replay and changed-payload rejection", model, result.TaskIDs[0])
					break
				}
				if time.Now().After(until) {
					t.Fatal("live task timeout; inspect fixture evidence")
				}
				time.Sleep(250 * time.Millisecond)
			}
			physicalLiveCancellation(t, ctx, client, transport)
		})
	}
	if cfg.Models[0].WarmRAMBytes != 0 {
		input, _ := json.Marshal(map[string]any{"directory": host.Directory, "cold": ram, "warm": cfg.Models[0].WarmRAMBytes, "digest": cfg.Models[0].ResidencyDigest})
		out, e := admin(ctx, `import sys,json,sqlite3,pathlib
p=json.load(sys.stdin);db=pathlib.Path(p['directory'])/'owners'/'host-resources.db'
c=sqlite3.connect('file:'+str(db)+'?mode=ro',uri=True)
rows=[json.loads(r[0]) for r in c.execute('select request from reservations')];c.close()
cold=warm=0
for r in rows:
 if r.get('cold_ram_bytes',0):
  assert r['cold_ram_bytes']==p['cold'] and r['ram_bytes']==p['warm'] and r['residency_digest']==p['digest'];warm+=1
 else:
  assert r['ram_bytes']==p['cold'];cold+=1
assert cold==1 and warm==3,(cold,warm)
print(json.dumps({'cold_reservations':cold,'warm_reservations':warm}))`, input)
		if e != nil {
			t.Fatalf("warm durable receipt audit: %v %s", e, out)
		}
		t.Logf("independent durable receipt audit: %s", out)
	}
}

// Wait for actual streamed output, then require a durable cancellation receipt.
func physicalLiveCancellation(t *testing.T, ctx context.Context, client *Client, transport string) {
	t.Helper()
	task := testTask()
	task.ContextTokens = 8192
	task.Prompt = "Write the integers from 1 through 10000, one per line. Do not summarize. /no_think"
	key := "live-" + transport + "-cancel-001"
	if _, err := client.Dispatch(ctx, "node-a", key, task); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(3 * time.Minute)
	streaming := false
	for time.Now().Before(until) {
		status, err := client.Status(ctx, "node-a", key)
		if err != nil {
			t.Fatal(err)
		}
		if status.State == "succeeded" || status.State == "failed" || status.State == "canceled" {
			t.Fatalf("task ended before live cancellation: %+v", status)
		}
		if len(status.TaskIDs) == 1 {
			page, err := client.Events(ctx, "node-a", key, status.TaskIDs[0], 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range page.Events {
				if event.Kind == runtime.ModelDelta {
					streaming = true
				}
			}
		}
		if streaming {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !streaming {
		t.Fatal("model never streamed before cancellation")
	}
	if _, err := client.Cancel(ctx, "node-a", key); err != nil {
		t.Fatal(err)
	}
	until = time.Now().Add(15 * time.Second)
	for time.Now().Before(until) {
		status, err := client.Status(ctx, "node-a", key)
		if err != nil {
			t.Fatal(err)
		}
		if status.State == "canceled" {
			t.Log("live streamed output canceled durably over", transport)
			return
		}
		if status.State == "succeeded" || status.State == "failed" {
			t.Fatalf("wrong cancellation terminal: %+v", status)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("live cancellation did not settle")
}
