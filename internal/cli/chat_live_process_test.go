//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"go.yaml.in/yaml/v3"
)

// This isolated test executable enters the real CLI with inherited standard
// descriptors. It does not inject a service, output writer, or chat hooks.
func TestChatLiveProcessChild(t *testing.T) {
	if os.Getenv("DARWIN_CHAT_LIVE_PROCESS_CHILD") != "1" {
		t.Skip("owned child only")
	}
	path := os.Getenv("DARWIN_CHAT_LIVE_PROCESS_CONFIG")
	if !filepath.IsAbs(path) {
		os.Exit(90)
	}
	os.Exit(RunWithInput([]string{"chat", "--config", path, "--model", "chat"}, os.Stdin, os.Stdout, os.Stderr, "fixture"))
}

func TestChatLiveProcessStdoutAndBrokenPipe(t *testing.T) {
	for _, broken := range []bool{false, true} {
		name := "complete"
		if broken {
			name = "broken_pipe"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			release := make(chan struct{})
			disconnected := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				chatLiveChunk(t, w, "LIVE_PREFIX "+strings.Repeat("safe ", 1000), false)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				case <-ctx.Done():
					return
				}
				if !broken {
					chatLiveChunk(t, w, " FINAL_SUFFIX", true)
					return
				}
				chatLiveChunk(t, w, strings.Repeat("next output ", 1000), false)
				select {
				case <-r.Context().Done():
					close(disconnected)
				case <-ctx.Done():
				}
			}))
			defer server.Close()
			defer cancel()
			cfg := config.Defaults()
			cfg.Mode = "local_only"
			cfg.Telemetry.Database = filepath.Join(t.TempDir(), "chat.db")
			cfg.Providers = []config.Provider{{ID: "fixture", Kind: "ollama", Endpoint: server.URL}}
			zero := 0.0
			cfg.Models = []config.Model{{ID: "chat", Provider: "fixture", Model: "fixture", Locality: "local", RAMBytes: 1, ContextTokens: 16384, Capabilities: []string{"chat"}, EstimatedCost: &zero}}
			body, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "chat.yaml")
			if err = os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			input, inputWriter, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			defer inputWriter.Close()
			output, outputWriter, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			defer outputWriter.Close()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestChatLiveProcessChild$")
			for _, value := range os.Environ() {
				if !strings.HasPrefix(value, "DARWIN_") {
					cmd.Env = append(cmd.Env, value)
				}
			}
			cmd.Env = append(cmd.Env, "DARWIN_PROCESS_OWNER_DIR="+filepath.Join(t.TempDir(), "owners"), "DARWIN_CHAT_LIVE_PROCESS_CHILD=1", "DARWIN_CHAT_LIVE_PROCESS_CONFIG="+path)
			cmd.Stdin, cmd.Stdout = input, outputWriter
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			cmd.WaitDelay = time.Second
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = input.Close()
			_ = outputWriter.Close()
			joined := false
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			defer func() {
				cancel()
				if !joined {
					<-done
				}
			}()
			if _, err = io.WriteString(inputWriter, "write a response\n"); err != nil {
				t.Fatal(err)
			}
			if err = output.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
				t.Fatal(err)
			}
			var rendered strings.Builder
			buffer := make([]byte, 1024)
			for !strings.Contains(rendered.String(), "LIVE_PREFIX") {
				n, readErr := output.Read(buffer)
				rendered.Write(buffer[:n])
				if readErr != nil {
					t.Fatal("prefix not observable before final frame", readErr)
				}
			}
			select {
			case err := <-done:
				joined = true
				t.Fatal("child exited before provider completion", err)
			default:
			}
			if broken {
				_ = output.Close()
			}
			_ = inputWriter.Close()
			close(release)
			if !broken {
				tail, readErr := io.ReadAll(output)
				if readErr != nil {
					t.Fatal(readErr)
				}
				rendered.Write(tail)
			}
			select {
			case err := <-done:
				joined = true
				if broken {
					status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
					if err == nil || !ok || status.Signaled() || cmd.ProcessState.ExitCode() != 1 {
						t.Fatal("broken pipe did not exit normally", err, cmd.ProcessState, stderr.String())
					}
				} else if err != nil {
					t.Fatal(err, stderr.String())
				}
			case <-ctx.Done():
				t.Fatal("chat child did not join")
			}
			if broken {
				select {
				case <-disconnected:
				case <-ctx.Done():
					t.Fatal("broken stdout did not cancel provider")
				}
			} else if strings.Count(rendered.String(), "LIVE_PREFIX") != 1 || strings.Count(rendered.String(), "FINAL_SUFFIX") != 1 || strings.Count(rendered.String(), "\n[task completed]\n") != 1 {
				t.Fatal("real CLI duplicated or omitted output")
			}
		})
	}
}
