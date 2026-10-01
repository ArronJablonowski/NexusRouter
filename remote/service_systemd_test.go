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

// Explicit opt-in: registers only a unique disposable user unit, serving the
// production remote host on loopback with test certificates and isolated state.
// Multicast requires a separately selected interface; no real inference occurs.
func TestNativeSystemdRemoteHostLifecycle(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("NEXUS_REMOTE_SYSTEMD") != "1" {
		t.Skip("requires explicit native systemd fixture qualification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 85*time.Second)
	defer cancel()
	command := func(args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	}
	if _, err := command("show-environment"); err != nil {
		t.Fatalf("required user systemd manager unavailable: %v", err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	binary := os.Getenv("NEXUS_REMOTE_TEST_BINARY")
	info, err := os.Stat(binary)
	if err != nil || !filepath.IsAbs(binary) || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		t.Fatal("explicit prebuilt native nexus binary required")
	}
	working := filepath.Join(dir, "work space $HOME %n \"quoted\"")
	if err := os.Mkdir(working, 0700); err != nil {
		t.Fatal(err)
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
	configPath := filepath.Join(dir, "runtime $HOME %n \"quoted\".yaml")
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
	spec := ServiceTemplateSpec{Platform: "systemd", Executable: binary, WorkingDirectory: working, OwnerDirectory: filepath.Join(dir, "owners $HOME %n"), Instance: instance, Listen: address, Config: configPath, Journal: filepath.Join(dir, "journal"), Trust: serverTrust, Certificate: serverCreds.CertificateFile, Key: serverCreds.KeyFile, CA: serverCreds.CAFile}
	if iface := os.Getenv("NEXUS_REMOTE_SERVICE_INTERFACE"); iface != "" {
		spec.AdvertiseInterface = iface
		spec.AdvertiseName = "node-a"
		spec.AdvertiseSSHPort = 22
	}
	body, err = RenderServiceTemplate(spec)
	if err != nil {
		t.Fatal(err)
	}
	unit := "nexus-remote-" + instance + ".service"
	unitPath := filepath.Join(dir, unit)
	if err := os.WriteFile(unitPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(ctx, "systemd-analyze", "--user", "verify", unitPath).CombinedOutput(); err != nil {
		t.Fatalf("native unit verification %v: %s", err, out)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, args := range [][]string{{"stop", unit}, {"disable", unit}, {"reset-failed", unit}, {"daemon-reload"}} {
			_ = exec.CommandContext(cleanup, "systemctl", append([]string{"--user"}, args...)...).Run()
		}
	})
	for _, args := range [][]string{{"link", unitPath}, {"daemon-reload"}, {"start", unit}} {
		if out, err := command(args...); err != nil {
			t.Fatalf("service setup %v: %s", err, out)
		}
	}
	pid := func() string {
		out, err := command("show", unit, "--property=MainPID", "--value")
		if err != nil {
			return ""
		}
		value := strings.TrimSpace(string(out))
		if value == "0" {
			return ""
		}
		return value
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
		out, _ := command("status", unit)
		t.Fatalf("host never became ready: %s", out)
		return ""
	}
	first := ready("")
	serviceFixtureDiscovery(t, ctx, spec, serverPin, true)
	cwd, err := os.Readlink("/proc/" + first + "/cwd")
	if err != nil || cwd != working {
		t.Fatalf("working directory changed: %q %v", cwd, err)
	}
	env, err := os.ReadFile("/proc/" + first + "/environ")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range strings.Split(string(env), "\x00") {
		if entry == "DARWIN_PROCESS_OWNER_DIR="+spec.OwnerDirectory {
			found = true
		}
	}
	if !found {
		t.Fatal("shared admission path changed")
	}

	if out, err := command("kill", "--signal=SIGKILL", "--kill-whom=main", unit); err != nil {
		t.Fatalf("fixture crash %v: %s", err, out)
	}
	second := ready(first)
	serviceFixtureDiscovery(t, ctx, spec, serverPin, true)
	if second == first {
		t.Fatal("host did not restart")
	}
	if out, err := command("stop", unit); err != nil {
		t.Fatalf("stop %v: %s", err, out)
	}
	if current := pid(); current != "" {
		t.Fatal("fixture still has running PID", current)
	}
	probe, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if _, err := client.Info(probe, instance); err == nil {
		t.Fatal("remote host still reachable after service stop")
	}
	serviceFixtureDiscovery(t, ctx, spec, serverPin, false)
	if posts.Load() != 0 {
		t.Fatal("unexpected inference", posts.Load())
	}
	t.Log("production remote host started with literal paths, restarted with new PID, and stopped; zero provider POSTs")
}
