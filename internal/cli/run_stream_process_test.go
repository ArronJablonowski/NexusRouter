package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

	"darwinrouter/internal/config"
	"darwinrouter/internal/telemetry"
	"darwinrouter/runtime"
	"darwinrouter/sessions"
	"go.yaml.in/yaml/v3"
)

func TestJSONRunProcessLiveOutputAndBrokenPipe(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "darwin")
	buildCtx, stopBuild := context.WithTimeout(context.Background(), time.Minute)
	defer stopBuild()
	build := exec.CommandContext(buildCtx, "go", "build", "-o", binary, "../../cmd/darwin")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	for _, mode := range []string{"complete", "closed", "stalled"} {
		t.Run(mode, func(t *testing.T) {
			broken := mode != "complete"
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			release, started, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
			secret := "json-stream-fixture-credential"
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				close(started)
				defer close(stopped)
				select {
				case <-release:
				case <-ctx.Done():
					return
				case <-r.Context().Done():
					return
				}
				if mode == "stalled" {
					fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", strings.Repeat("large output ", 20000))
					return
				}
				if broken {
					fmt.Fprintln(w, `{"message":{"content":"partial"},"done":false}`)
					w.(http.Flusher).Flush()
					select {
					case <-r.Context().Done():
					case <-ctx.Done():
					}
					return
				}
				fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", secret+" answer")
			}))
			defer provider.Close()
			// Unblock all fixture handlers before waiting on server cleanup.
			defer cancel()
			cfg := config.Defaults()
			cfg.Mode = "local_only"
			cfg.Telemetry.Database = filepath.Join(t.TempDir(), "task.db")
			cfg.Providers = []config.Provider{{ID: "fixture", Kind: "ollama", Endpoint: provider.URL}}
			zero := 0.0
			cfg.Models = []config.Model{{ID: "chat", Provider: "fixture", Model: "fixture", Locality: "local", Capabilities: []string{"chat"}, EstimatedCost: &zero}}
			configPath := filepath.Join(t.TempDir(), "run.yaml")
			data, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(configPath, data, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, binary, "run", "--config", configPath, "--model", "chat", "--json")
			cmd.Env = append(os.Environ(), "DARWIN_API_TOKEN="+secret)
			cmd.Stdin = strings.NewReader("hello")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			pipe, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer pipe.Close()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			scanner := bufio.NewScanner(pipe)
			scanner.Buffer(make([]byte, 4096), 2<<20)
			if !scanner.Scan() {
				waitErr := cmd.Wait()
				t.Fatal("no live event", scanner.Err(), waitErr, stderr.String())
			}
			var first struct {
				Version int           `json:"version"`
				Type    string        `json:"type"`
				Event   runtime.Event `json:"event"`
			}
			if json.Unmarshal(scanner.Bytes(), &first) != nil || first.Version != 1 || first.Type != "event" || first.Event.Kind != runtime.TaskStarted {
				t.Fatal(scanner.Text())
			}
			db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			stored, err := db.Read(ctx, first.Event.TaskID, 0, 1)
			if err != nil || len(stored) != 1 || stored[0].ID != first.Event.ID {
				t.Fatal("emitted before commit", stored, err)
			}
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("provider not started")
			}
			if mode == "closed" {
				_ = pipe.Close()
			}
			close(release)
			if mode == "stalled" {
				// Stop reading while a large durable turn fills stdout. Observe
				// the commit independently, then cancel the real child process.
				for {
					events, readErr := db.Read(ctx, first.Event.TaskID, 0, 100)
					if readErr != nil {
						t.Fatal(readErr)
					}
					found := false
					for _, event := range events {
						if event.Kind == runtime.TurnCompleted {
							found = true
						}
					}
					if found {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal("large turn never committed")
					case <-time.After(10 * time.Millisecond):
					}
				}
				if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			}
			var lines []map[string]json.RawMessage
			if !broken {
				for scanner.Scan() {
					if strings.Contains(scanner.Text(), secret) {
						t.Fatal("unredacted stream")
					}
					var line map[string]json.RawMessage
					if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
						t.Fatal("non-JSON stdout", err)
					}
					lines = append(lines, line)
				}
				if scanner.Err() != nil {
					t.Fatal(scanner.Err())
				}
			}
			err = cmd.Wait()
			if broken {
				if err == nil || cmd.ProcessState.ExitCode() != 1 {
					t.Fatal("pipe must fail normally, not SIGPIPE", err, cmd.ProcessState, stderr.String())
				}
			} else {
				if err != nil {
					t.Fatal(err, stderr.String())
				}
				if len(lines) == 0 || string(lines[len(lines)-1]["type"]) != `"result"` || !bytes.Contains(lines[len(lines)-1]["result"], []byte("[REDACTED] answer")) {
					t.Fatal(lines)
				}
			}
			select {
			case <-stopped:
			case <-ctx.Done():
				t.Fatal("provider not stopped")
			}
			snapshot, err := sessions.Replay(ctx, db, first.Event.TaskID)
			want := "completed"
			if broken {
				want = "canceled"
			}
			if err != nil || snapshot.State != want {
				t.Fatal("missing durable terminal state", snapshot, err)
			}
		})
	}
}
