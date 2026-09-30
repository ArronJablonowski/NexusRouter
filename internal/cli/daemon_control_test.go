package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/daemon"
	"github.com/ArronJablonowski/NexusRouter/internal/api"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func TestDaemonControlArgumentsAndBoundaries(t *testing.T) {
	for _, args := range [][]string{{}, {"kill"}, {"stop"}, {"status", "--config=a", "--config=b"}, {"status", "--config=a", "-config=b"}, {"status", "--config=a", "private-value"}, {"stop", "--config=a", "--pid=1"}} {
		var out, diagnostic bytes.Buffer
		if code := runDaemonControl(args, &out, &diagnostic); code != 2 || out.Len() != 0 || strings.Contains(diagnostic.String(), "private-value") {
			t.Fatal(code, diagnostic.String())
		}
	}
	for _, address := range []string{"example.com:80", "192.168.1.1:80", "0.0.0.0:80", "127.0.0.1:0", "127.0.0.1:65536", "[::1%lo0]:80"} {
		cfg := config.Defaults()
		cfg.Daemon.Listen = address
		if c, err := daemonClient(cfg, strings.Repeat("t", 32)); err == nil {
			c.Close()
			t.Fatal("unsafe destination accepted")
		}
	}
	cfg := config.Defaults()
	cfg.Daemon.Listen = "localhost:8080"
	c, err := daemonClient(cfg, strings.Repeat("t", 32))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.base != "http://127.0.0.1:8080" {
		t.Fatal("localhost not pinned")
	}
	if _, err := daemonClient(cfg, "short"); err == nil {
		t.Fatal("weak token accepted")
	}
}

func TestDaemonControlClientStrictResponsesAndCancellation(t *testing.T) {
	good := daemon.Status{Version: 1, InstanceID: strings.Repeat("a", 64), State: "ready", StartedAt: time.Now().UTC()}
	raw, _ := json.Marshal(good)
	for _, mode := range []string{"valid", "unknown", "duplicate", "wrongcase", "oversize", "trailing", "invalid", "redirect", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("t", 32) || r.Method != "GET" || r.URL.Path != "/v1/daemon/status" {
					t.Error("unexpected request")
				}
				body := string(raw)
				switch mode {
				case "unknown":
					body = strings.TrimSuffix(body, "}") + `,"secret":"private"}`
				case "duplicate":
					body = strings.TrimSuffix(body, "}") + `,"version":1}`
				case "wrongcase":
					body = strings.Replace(body, `"version"`, `"Version"`, 1)
				case "oversize":
					body = strings.Repeat(" ", 4097)
				case "trailing":
					body += " {}"
				case "invalid":
					body = strings.Replace(body, `"ready"`, `"unknown"`, 1)
				case "redirect":
					w.Header().Set("Location", "http://192.168.1.1/private")
					w.WriteHeader(302)
					return
				}
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			cfg := config.Defaults()
			cfg.Daemon.Listen = strings.TrimPrefix(server.URL, "http://")
			client, err := daemonClient(cfg, strings.Repeat("t", 32))
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			got, err := client.Status(ctx)
			if mode == "valid" {
				if err != nil || got.InstanceID != good.InstanceID {
					t.Fatal(got, err)
				}
			} else if err == nil {
				t.Fatal("invalid response accepted")
			}
			if calls.Load() > 1 {
				t.Fatal("redirect followed")
			}
		})
	}
}

func TestDaemonControlCLIStopBindsObservedInstanceWithoutFiles(t *testing.T) {
	token := strings.Repeat("t", 32)
	t.Setenv("DARWIN_API_TOKEN", token)
	good := daemon.Status{Version: 1, InstanceID: strings.Repeat("a", 64), State: "ready", StartedAt: time.Now().UTC()}
	var gets, posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing auth")
		}
		status := good
		if r.Method == "GET" && r.URL.Path == "/v1/daemon/status" {
			gets.Add(1)
		} else if r.Method == "POST" && r.URL.Path == "/v1/daemon/stop" {
			posts.Add(1)
			var body struct {
				InstanceID string `json:"instance_id"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.InstanceID != good.InstanceID {
				t.Error("unbound stop")
			}
			status.State = "stopping"
		} else {
			t.Error("unexpected endpoint")
		}
		json.NewEncoder(w).Encode(status)
	}))
	defer server.Close()
	cfg := config.Defaults()
	cfg.Daemon.Listen = strings.TrimPrefix(server.URL, "http://")
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "must-not-exist.db")
	path := writeDeprecationCLIConfig(t, cfg)
	for _, verb := range []string{"status", "stop"} {
		var out, diagnostic bytes.Buffer
		if code := Run([]string{"daemon", verb, "--config", path}, &out, &diagnostic, "test"); code != 0 {
			t.Fatal(code, diagnostic.String())
		}
		var status daemon.Status
		if json.Unmarshal(out.Bytes(), &status) != nil || status.Validate() != nil {
			t.Fatal("bad output")
		}
	}
	if gets.Load() != 2 || posts.Load() != 1 {
		t.Fatal(gets.Load(), posts.Load())
	}
	if _, err := os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
		t.Fatal("control touched database")
	}
}

func TestDaemonStopRejectsChangedInstanceAndNeverRetries(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(daemon.Status{Version: 1, InstanceID: strings.Repeat("b", 64), State: "stopping", StartedAt: time.Now().UTC()})
	}))
	defer server.Close()
	cfg := config.Defaults()
	cfg.Daemon.Listen = strings.TrimPrefix(server.URL, "http://")
	client, err := daemonClient(cfg, strings.Repeat("t", 32))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Stop(context.Background(), strings.Repeat("a", 64)); err == nil || calls.Load() != 1 {
		t.Fatal("changed instance accepted or stop retried")
	}
}

func TestDaemonCLIStopsDegradedAuthenticatedInstance(t *testing.T) {
	token := strings.Repeat("d", 32)
	t.Setenv("DARWIN_API_TOKEN", token)
	id := strings.Repeat("c", 64)
	var callbacks, stops atomic.Int32
	control, err := daemon.New(id, func() { callbacks.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	handler, err := api.New(token, 1, api.Services{
		Run: func(context.Context, app.Request) (app.Result, error) {
			t.Error("unexpected inference")
			return app.Result{}, daemon.ErrControl
		},
		Inspect: func(context.Context, string) (sessions.Snapshot, error) {
			t.Error("unexpected task read")
			return sessions.Snapshot{}, daemon.ErrControl
		},
		Health: func(context.Context) error { return daemon.ErrControl },
		DaemonStatus: func(ctx context.Context) (daemon.Status, error) {
			status, err := control.Current(ctx)
			if status.State == "ready" {
				status.State = "degraded"
			}
			return status, err
		},
		StopDaemon: func(ctx context.Context, observed string) (daemon.Status, error) {
			stops.Add(1)
			if observed != id {
				t.Error("stop not bound to observed degraded instance")
			}
			return control.Stop(ctx, observed)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	cfg := config.Defaults()
	cfg.Daemon.Listen = strings.TrimPrefix(server.URL, "http://")
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "uncreated.db")
	path := writeDeprecationCLIConfig(t, cfg)
	var out, diagnostic bytes.Buffer
	if code := Run([]string{"daemon", "stop", "--config", path}, &out, &diagnostic, "test"); code != 0 {
		t.Fatal(code, diagnostic.String())
	}
	var status daemon.Status
	if json.Unmarshal(out.Bytes(), &status) != nil || status.InstanceID != id || status.State != "stopping" || stops.Load() != 1 || callbacks.Load() != 1 {
		t.Fatal("degraded daemon was not stopped exactly once", stops.Load(), callbacks.Load())
	}
	if _, err := os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
		t.Fatal("control created database")
	}
}
