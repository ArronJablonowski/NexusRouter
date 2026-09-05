package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/daemon"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
)

func TestDaemonLifecycleAcrossCLIProcesses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dir := t.TempDir()
	binary := filepath.Join(dir, "darwin")
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", binary, "../../cmd/darwin").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	l.Close()
	configuration := filepath.Join(dir, "daemon.yaml")
	if err := os.WriteFile(configuration, []byte(fmt.Sprintf("daemon:\n  listen: %q\ntelemetry:\n  database: %q\n", address, filepath.Join(dir, "tasks.db"))), 0600); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("lifecycle-fixture-", 3)
	run := func(action string) (daemon.Status, error) {
		cmd := exec.CommandContext(ctx, binary, "daemon", action, "--config", configuration)
		cmd.Env = append(os.Environ(), "DARWIN_API_TOKEN="+token)
		var out, diagnostic bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &diagnostic
		err := cmd.Run()
		if strings.Contains(out.String()+diagnostic.String(), token) {
			t.Error("token leaked")
		}
		var status daemon.Status
		if err == nil {
			err = json.Unmarshal(out.Bytes(), &status)
		}
		return status, err
	}
	first, err := run("start")
	if err != nil || first.Validate() != nil || first.State != "ready" {
		t.Fatal("start failed", err, first)
	}
	liveID := first.InstanceID
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		cfg := config.Defaults()
		cfg.Daemon.Listen = address
		client, err := daemonClient(cfg, token)
		if err == nil {
			defer client.Close()
			_, _ = client.Stop(cleanup, liveID)
		}
	}()
	status, err := run("status")
	if err != nil || status.InstanceID != first.InstanceID {
		t.Fatal("status identity", status, err)
	}
	if _, err := run("start"); err == nil {
		t.Fatal("duplicate start accepted")
	}
	status, err = run("status")
	if err != nil || status.InstanceID != first.InstanceID {
		t.Fatal("duplicate disturbed live daemon", status, err)
	}
	stopped, err := run("stop")
	if err != nil || stopped.InstanceID != first.InstanceID || stopped.State != "stopping" {
		t.Fatal("stop failed", stopped, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err != nil {
			break
		}
		conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("stopped listener still open")
		}
		time.Sleep(20 * time.Millisecond)
	}
	second, err := run("start")
	if err == nil {
		liveID = second.InstanceID
	}
	if err != nil || second.InstanceID == first.InstanceID || second.State != "ready" {
		t.Fatal("restart failed", second, err)
	}
}
