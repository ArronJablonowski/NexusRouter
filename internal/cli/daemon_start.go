package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/internal/processaudit"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ArronJablonowski/NexusRouter/daemon"
	"github.com/ArronJablonowski/NexusRouter/internal/branding"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

var errDaemonStart = errors.New("managed daemon start unavailable")

// runDaemonStart owns only the process it starts. No PID file or externally
// supplied PID grants signal authority. A successful authenticated response
// must identify this fresh launch, not another listener that won the bind race.
func runDaemonStart(ctx context.Context, cfg config.Settings, path, token string) (daemon.Status, error) {
	bad := func() (daemon.Status, error) { return daemon.Status{}, errDaemonStart }
	if ctx == nil || ctx.Err() != nil || path == "" || token == "" || token != branding.Getenv("DARWIN_API_TOKEN") {
		return bad()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client, err := daemonClient(cfg, token)
	if err != nil {
		return bad()
	}
	defer client.Close()
	host, port, err := net.SplitHostPort(cfg.Daemon.Listen)
	if err != nil {
		return bad()
	}
	if host == "localhost" {
		host = "127.0.0.1"
	}
	probeCtx, stopProbe := context.WithTimeout(ctx, time.Second)
	connection, probeErr := (&net.Dialer{}).DialContext(probeCtx, "tcp", net.JoinHostPort(host, port))
	stopProbe()
	if probeErr == nil {
		_ = connection.Close()
		return bad()
	}
	if ctx.Err() != nil || !errors.Is(probeErr, syscall.ECONNREFUSED) {
		return bad()
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return bad()
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.Mode().IsRegular() {
		return bad()
	}
	executable, err := os.Executable()
	if err != nil {
		return bad()
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return bad()
	}
	instance := hex.EncodeToString(random[:])
	command := exec.Command(executable, "serve", "--config", absolute, "--instance-id", instance)
	if !detachDaemon(command) {
		return bad()
	}
	// Preserve cwd and environment, including config-relative storage semantics
	// and the existing token variable. Credentials never enter arguments/logs.
	command.Env = os.Environ()
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return bad()
	}
	command.Stdin, command.Stdout, command.Stderr = null, null, null
	err = processaudit.Start(command)
	_ = null.Close()
	if err != nil {
		return bad()
	}
	done := make(chan struct{})
	go func() { _ = processaudit.Wait(command); close(done) }()
	ready := false
	defer func() {
		if !ready {
			// The child may already own queued work. Give its normal shutdown
			// path time to cancel/join that work before forcing this same owned
			// process to exit. Never signal a process discovered by PID lookup.
			_ = command.Process.Signal(syscall.SIGTERM)
			grace := time.NewTimer(2 * time.Second)
			defer grace.Stop()
			select {
			case <-done:
			case <-grace.C:
				_ = command.Process.Kill()
				<-done
			}
		}
	}()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return bad()
		case <-done:
			return bad()
		default:
		}
		status, err := client.Status(ctx)
		if err == nil {
			if status.InstanceID != instance || (status.State != "ready" && status.State != "degraded") {
				return bad()
			}
			if status.State == "ready" {
				select {
				case <-ctx.Done():
					return bad()
				case <-done:
					return bad()
				default:
				}
				ready = true
				return status, nil
			}
		}
		select {
		case <-ctx.Done():
			return bad()
		case <-done:
			return bad()
		case <-ticker.C:
		}
	}
}
