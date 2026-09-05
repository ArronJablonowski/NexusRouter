//go:build darwin || linux

package cli

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/daemon"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
)

type daemonStartFixture struct{ Address, Mode, Marker string }

// The owned test executable accepts production-shaped serve arguments only in
// this explicit child mode. No real daemon, database or provider is involved.
func init() {
	if os.Getenv("DARWIN_TEST_START_CHILD") != "1" || len(os.Args) < 2 || os.Args[1] != "serve" {
		return
	}
	if len(os.Args) != 6 || os.Args[2] != "--config" || os.Args[4] != "--instance-id" || !filepath.IsAbs(os.Args[3]) || !daemon.ValidInstanceID(os.Args[5]) {
		os.Exit(81)
	}
	file, err := os.Open(os.Args[3])
	if err != nil {
		os.Exit(82)
	}
	var fixture daemonStartFixture
	err = json.NewDecoder(file).Decode(&fixture)
	file.Close()
	if err != nil {
		os.Exit(83)
	}
	for _, arg := range os.Args {
		if strings.Contains(arg, os.Getenv("DARWIN_API_TOKEN")) {
			os.Exit(84)
		}
	}
	if fixture.Mode == "exit" {
		os.Exit(85)
	}
	listener, err := net.Listen("tcp", fixture.Address)
	if err != nil {
		os.Exit(86)
	}
	if os.WriteFile(fixture.Marker, []byte("started"), 0600) != nil {
		os.Exit(87)
	}
	if fixture.Mode == "graceful" {
		termination := make(chan os.Signal, 1)
		signal.Notify(termination, syscall.SIGTERM)
		go func() {
			<-termination
			_ = os.WriteFile(fixture.Marker, []byte("gracefully-stopped"), 0600)
			os.Exit(0)
		}()
	}
	if fixture.Mode == "ignore-term" {
		signal.Ignore(syscall.SIGTERM)
	}
	// A test failure cannot leave a persistent fixture process behind.
	time.AfterFunc(10*time.Second, func() { os.Exit(88) })
	instance := os.Args[5]
	if fixture.Mode == "wrong" || fixture.Mode == "graceful" || fixture.Mode == "ignore-term" {
		instance = strings.Repeat("0", 64)
	}
	status := daemon.Status{Version: 1, InstanceID: instance, State: "ready", StartedAt: time.Now().UTC()}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+os.Getenv("DARWIN_API_TOKEN") || fixture.Mode == "timeout" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/v1/daemon/stop" {
			var request struct {
				InstanceID string `json:"instance_id"`
			}
			if json.NewDecoder(r.Body).Decode(&request) != nil || request.InstanceID != instance {
				w.WriteHeader(409)
				return
			}
			response := status
			response.State = "stopping"
			_ = json.NewEncoder(w).Encode(response)
			time.AfterFunc(30*time.Millisecond, func() { os.Exit(0) })
			return
		}
		if r.URL.Path != "/v1/daemon/status" {
			w.WriteHeader(404)
			return
		}
		response := status
		if fixture.Mode == "degraded" || (fixture.Mode == "warming" && time.Since(status.StartedAt) < 300*time.Millisecond) {
			response.State = "degraded"
		}
		_ = json.NewEncoder(w).Encode(response)
	})
	_ = http.Serve(listener, handler)
	os.Exit(89)
}

func daemonStartFixtureConfig(t *testing.T, mode string) (config.Settings, string, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	marker := filepath.Join(t.TempDir(), "started")
	path := filepath.Join(t.TempDir(), "fixture.json")
	body, _ := json.Marshal(daemonStartFixture{Address: address, Mode: mode, Marker: marker})
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Daemon.Listen = address
	return cfg, path, marker
}

func TestManagedDaemonStartOwnedSubprocess(t *testing.T) {
	t.Setenv("DARWIN_TEST_START_CHILD", "1")
	token := strings.Repeat("test-token-", 4)
	t.Setenv("DARWIN_API_TOKEN", token)
	cfg, path, marker := daemonStartFixtureConfig(t, "ready")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	status, err := runDaemonStart(ctx, cfg, path, token)
	cancel()
	if err != nil || status.Validate() != nil || status.State != "ready" {
		t.Fatal("managed start failed", err)
	}
	client, err := daemonClient(cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defer func() { _, _ = client.Stop(context.Background(), status.InstanceID) }()
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("child not started")
	}
	// Canceling the completed start operation must not terminate the daemon.
	again, err := client.Status(context.Background())
	if err != nil || again.InstanceID != status.InstanceID {
		t.Fatal("successful child killed when start returned", err)
	}
	if _, err := runDaemonStart(context.Background(), cfg, path, token); err == nil {
		t.Fatal("duplicate listener admitted")
	}
	again, err = client.Status(context.Background())
	if err != nil || again.InstanceID != status.InstanceID {
		t.Fatal("duplicate start disturbed existing process")
	}
	if _, err := client.Stop(context.Background(), status.InstanceID); err != nil {
		t.Fatal("fixture stop failed", err)
	}
}

func TestManagedDaemonStartFailureReapsOnlyOwnedChild(t *testing.T) {
	t.Setenv("DARWIN_TEST_START_CHILD", "1")
	token := strings.Repeat("test-token-", 4)
	t.Setenv("DARWIN_API_TOKEN", token)
	for _, mode := range []string{"exit", "wrong", "timeout", "graceful", "ignore-term", "degraded"} {
		t.Run(mode, func(t *testing.T) {
			cfg, path, marker := daemonStartFixtureConfig(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			started := time.Now()
			status, err := runDaemonStart(ctx, cfg, path, token)
			if err == nil || status.InstanceID != "" {
				t.Fatal("failed start admitted")
			}
			if mode == "degraded" && time.Since(started) < 1800*time.Millisecond {
				t.Fatal("matching degraded instance rejected before readiness deadline")
			}
			if mode != "exit" {
				if _, err := os.Stat(marker); err != nil {
					t.Fatal("test never reached launched listener", err)
				}
			}
			if mode == "graceful" {
				body, err := os.ReadFile(marker)
				if err != nil || string(body) != "gracefully-stopped" {
					t.Fatal("failed start skipped cooperative shutdown", err)
				}
			}
			listener, err := net.Listen("tcp", cfg.Daemon.Listen)
			if err != nil {
				t.Fatal("failed owned process still listening", err)
			}
			listener.Close()
		})
	}
}

func TestManagedDaemonStartWaitsForMatchingDegradedInstance(t *testing.T) {
	t.Setenv("DARWIN_TEST_START_CHILD", "1")
	token := strings.Repeat("test-token-", 4)
	t.Setenv("DARWIN_API_TOKEN", token)
	cfg, path, _ := daemonStartFixtureConfig(t, "warming")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status, err := runDaemonStart(ctx, cfg, path, token)
	if err != nil || status.State != "ready" {
		t.Fatal("matching degraded instance did not reach readiness", err)
	}
	client, err := daemonClient(cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defer func() { _, _ = client.Stop(context.Background(), status.InstanceID) }()
	if time.Since(status.StartedAt) < 300*time.Millisecond {
		t.Fatal("start returned before fixture became ready")
	}
}

func TestManagedDaemonStartRefusesBeforeLaunch(t *testing.T) {
	t.Setenv("DARWIN_TEST_START_CHILD", "1")
	token := strings.Repeat("test-token-", 4)
	t.Setenv("DARWIN_API_TOKEN", token)
	cfg, path, marker := daemonStartFixtureConfig(t, "ready")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runDaemonStart(ctx, cfg, path, token); err == nil {
		t.Fatal("pre-canceled start admitted")
	}
	if _, err := runDaemonStart(context.Background(), cfg, path, "mismatched-token"); err == nil {
		t.Fatal("non-inherited token admitted")
	}
	listener, err := net.Listen("tcp", cfg.Daemon.Listen)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err := runDaemonStart(context.Background(), cfg, path, token); err == nil {
		t.Fatal("unrelated listener admitted")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("refused start spawned fixture")
	}
}
