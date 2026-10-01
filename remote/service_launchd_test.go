package remote

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"go.yaml.in/yaml/v3"
)

// Explicit opt-in: registers only a unique disposable user agent, serving the
// production remote host on loopback with test certificates and isolated state.
// Multicast requires an explicit interface; no production service or inference is involved.
func TestNativeLaunchdRemoteHostLifecycle(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Getenv("NEXUS_REMOTE_LAUNCHD") != "1" {
		t.Skip("requires explicit native launchd fixture qualification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 85*time.Second)
	defer cancel()
	domain := "gui/" + strconv.Itoa(os.Getuid())
	command := func(args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "/bin/launchctl", args...).CombinedOutput()
	}
	if _, err := command("print", domain); err != nil {
		t.Fatalf("required user launchd domain unavailable: %v", err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "nexus")
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", binary, "../cmd/nexus").CombinedOutput(); err != nil {
		t.Fatalf("build %v: %s", err, out)
	}
	var posts atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			posts.Add(1)
			http.Error(w, "inference forbidden", 403)
			return
		}
		fmt.Fprint(w, `{"models":[{"name":"fixture"}]}`)
	}))
	defer provider.Close()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Tools.Enabled = false
	cfg.Hardware.AutoProfile = false
	cfg.Workers.Max = 1
	cfg.Hardware.Concurrent = "1"
	cfg.Telemetry.Database = filepath.Join(dir, "tasks.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero}}
	configPath := filepath.Join(dir, "runtime.yaml")
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	ca := newCA(t)
	serverCreds, serverPin := ca.leaf(t, "node-a")
	clientCreds, clientPin := ca.leaf(t, "node-b")
	listener, err := net.Listen("tcp", net.JoinHostPort(serviceFixtureAddress(t), "0"))
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	instance := "qualification-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	serverTrust := filepath.Join(dir, "server.json")
	clientTrust := filepath.Join(dir, "client.json")
	caller := testPeer("node-b", clientPin, "https://127.0.0.1:443")
	caller.Operations = []string{"info"}
	destination := testPeer(instance, serverPin, "https://"+address)
	destination.ServerName = "node-a"
	destination.Operations = []string{"info"}
	writeRegistry(t, serverTrust, caller)
	writeRegistry(t, clientTrust, destination)
	client := &Client{Trust: TrustFile(clientTrust), Credentials: clientCreds}
	spec := ServiceTemplateSpec{Platform: "launchd", Executable: binary, WorkingDirectory: dir, OwnerDirectory: filepath.Join(dir, "owners"), Instance: instance, Listen: address, Config: configPath, Journal: filepath.Join(dir, "journal"), Trust: serverTrust, Certificate: serverCreds.CertificateFile, Key: serverCreds.KeyFile, CA: serverCreds.CAFile}
	if iface := os.Getenv("NEXUS_REMOTE_SERVICE_INTERFACE"); iface != "" {
		spec.AdvertiseInterface = iface
		spec.AdvertiseName = "node-a"
		spec.AdvertiseSSHPort = 22
	}
	body, err = RenderServiceTemplate(spec)
	if err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(dir, "remote.plist")
	if err := os.WriteFile(plist, body, 0600); err != nil {
		t.Fatal(err)
	}
	target := domain + "/com.nexusrouter.remote." + instance
	// Install cleanup before bootstrap, including ambiguous bootstrap failures.
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanup, "/bin/launchctl", "bootout", target).Run()
	})
	if out, err := command("bootstrap", domain, plist); err != nil {
		t.Fatalf("bootstrap %v: %s", err, out)
	}
	pid := func() string {
		out, err := command("print", target)
		if err != nil {
			return ""
		}
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "pid = ") {
				return strings.TrimPrefix(line, "pid = ")
			}
		}
		return ""
	}
	ready := func(previous string) string {
		t.Helper()
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) && ctx.Err() == nil {
			current := pid()
			probe, cancel := context.WithTimeout(ctx, time.Second)
			info, err := client.Info(probe, instance)
			cancel()
			if err == nil && info.Instance == instance && current != "" && current != previous {
				return current
			}
			time.Sleep(100 * time.Millisecond)
		}
		out, _ := command("print", target)
		t.Fatalf("host never became ready: %s", out)
		return ""
	}
	first := ready("")
	serviceFixtureDiscovery(t, ctx, spec, serverPin, true)
	if out, err := command("kill", "SIGKILL", target); err != nil {
		t.Fatalf("fixture crash %v: %s", err, out)
	}
	second := ready(first)
	serviceFixtureDiscovery(t, ctx, spec, serverPin, true)
	if second == first {
		t.Fatal("host did not restart")
	}
	if out, err := command("bootout", target); err != nil {
		t.Fatalf("bootout %v: %s", err, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := command("print", target); err != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := command("print", target); err == nil {
		t.Fatal("fixture remains registered")
	}
	probe, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if _, err := client.Info(probe, instance); err == nil {
		t.Fatal("remote host still reachable after bootout")
	}
	serviceFixtureDiscovery(t, ctx, spec, serverPin, false)
	if posts.Load() != 0 {
		t.Fatal("unexpected inference", posts.Load())
	}
	t.Log("production remote host started, restarted with new PID, and stopped; zero provider POSTs")
}
