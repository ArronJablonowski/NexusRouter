package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPromptBlockedProcessSIGTERM(t *testing.T) {
	buildCtx, stopBuild := context.WithTimeout(context.Background(), time.Minute)
	defer stopBuild()
	binary := filepath.Join(t.TempDir(), "darwin")
	build := exec.CommandContext(buildCtx, "go", "build", "-o", binary, "../../cmd/nexus")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v %s", err, output)
	}
	for _, command := range []string{"run", "submit"} {
		t.Run(command, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			dir := t.TempDir()
			database := filepath.Join(dir, "absent.db")
			configuration := filepath.Join(dir, "config.yaml")
			if err := syscall.Mkfifo(configuration, 0600); err != nil {
				t.Fatal(err)
			}
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close() // Retain the writer through process exit: no EOF.
			args := []string{command, "--config", configuration, "--model", "fixture"}
			if command == "submit" {
				args = append(args, "--key", "prompt-process-key")
			}
			cmd := exec.CommandContext(ctx, binary, args...)
			cmd.Stdin = reader
			cmd.WaitDelay = time.Second
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "DARWIN_") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "DARWIN_PROCESS_OWNER_DIR="+filepath.Join(t.TempDir(), "owners"))
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			joined := false
			defer func() {
				cancel()
				if !joined {
					<-done
				}
			}()
			// Config is opened after signal setup. A successful nonblocking FIFO
			// writer open proves the child reached that boundary, without racing
			// process startup or relying on a guessed startup delay.
			poll := time.NewTicker(10 * time.Millisecond)
			defer poll.Stop()
			for {
				fd, openErr := syscall.Open(configuration, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
				if openErr == nil {
					configWriter := os.NewFile(uintptr(fd), "prompt-config")
					_, writeErr := fmt.Fprintf(configWriter, "telemetry:\n  database: %q\n", database)
					closeErr := configWriter.Close()
					if writeErr != nil || closeErr != nil {
						t.Fatal(writeErr, closeErr)
					}
					break
				}
				if !errors.Is(openErr, syscall.ENXIO) {
					t.Fatal(openErr)
				}
				select {
				case err := <-done:
					joined = true
					t.Fatalf("exited before configuration: %v %s", err, stderr.String())
				case <-poll.C:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
				joined = true
			case <-ctx.Done():
				t.Fatal("blocked prompt did not terminate", ctx.Err())
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				t.Fatalf("expected handled exit 1, got %v; stderr=%s", err, stderr.String())
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), "cannot read UTF-8 prompt") || strings.Contains(stderr.String(), "prompt-process-key") {
				t.Fatalf("unexpected output: stdout=%s stderr=%s", stdout.String(), stderr.String())
			}
			for _, path := range []string{database, database + "-wal", database + "-shm"} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("prompt failure created storage", err)
				}
			}
		})
	}
}
