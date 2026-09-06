//go:build darwin

package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"go.yaml.in/yaml/v3"
)

func TestChatCreateApprovalRealPTY(t *testing.T) {
	for _, mode := range []string{"approve", "deny", "existing", "pipe", "pipe_input", "pipe_output"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if calls.Add(1) == 1 {
					fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"create_file","arguments":{"path":"new.txt","content":"approved fixture\n"}}}]},"done":true,"done_reason":"tool_calls"}`)
				} else {
					fmt.Fprintln(w, `{"message":{"content":"fixture finished"},"done":true,"done_reason":"stop"}`)
				}
			}))
			defer server.Close()
			root := t.TempDir()
			target := filepath.Join(root, "new.txt")
			if mode == "existing" {
				if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := config.Defaults()
			cfg.Mode = "local_only"
			cfg.Telemetry.Database = filepath.Join(t.TempDir(), "events.db")
			cfg.Tools.Enabled = true
			cfg.Tools.ReadRoot = root
			cfg.Tools.CreateEnabled = true
			cfg.Tools.CreateRoot = root
			cfg.Providers = []config.Provider{{ID: "fixture", Kind: "ollama", Endpoint: server.URL}}
			zero := 0.0
			cfg.Models = []config.Model{{ID: "chat", Provider: "fixture", Model: "fixture", Locality: "local", RAMBytes: 1, ContextTokens: 16384, Capabilities: []string{"chat"}, EstimatedCost: &zero}}
			body, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err = os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			env := []string{}
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "DARWIN_") && !strings.HasPrefix(entry, "GORACE=") {
					env = append(env, entry)
				}
			}
			env = append(env, "DARWIN_PROCESS_OWNER_DIR="+filepath.Join(t.TempDir(), "owners"), "DARWIN_CHAT_LIVE_PROCESS_CHILD=1", "DARWIN_CHAT_LIVE_PROCESS_CONFIG="+path, "GORACE=atexit_sleep_ms=0")
			if mode == "pipe_input" || mode == "pipe_output" {
				cmd := exec.CommandContext(ctx, "/usr/bin/script", "-q", "/dev/null", os.Args[0], "-test.run=^TestChatCreateApprovalPipeChild$")
				cmd.Env = append(env, "DARWIN_CREATE_PIPE_SIDE="+mode)
				cmd.WaitDelay = time.Second
				output, err := cmd.CombinedOutput()
				if err != nil || !strings.Contains(string(output), "PIPE_REJECTED") || calls.Load() != 0 {
					t.Fatal(string(output), err, calls.Load())
				}
				if _, err = os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
					t.Fatal("pipe admission touched database", err)
				}
				return
			}
			if mode == "pipe" {
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestChatLiveProcessChild$")
				cmd.Env = env
				cmd.Stdin = strings.NewReader("create file\n")
				cmd.WaitDelay = time.Second
				output, err := cmd.CombinedOutput()
				if err == nil || !strings.Contains(string(output), "requires terminal input and output") || calls.Load() != 0 {
					t.Fatal(string(output), err, calls.Load())
				}
				if _, err = os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
					t.Fatal("pipe admission touched database", err)
				}
				return
			}
			cmd := exec.CommandContext(ctx, "/usr/bin/script", "-q", "/dev/null", os.Args[0], "-test.run=^TestChatLiveProcessChild$")
			cmd.Env = env
			cmd.WaitDelay = time.Second
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			output, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			_, _ = io.WriteString(input, "create fixture file\n")
			scanner := bufio.NewScanner(io.LimitReader(output, 1<<20))
			scanner.Buffer(make([]byte, 4096), 128<<10)
			pending := regexp.MustCompile(`\[approval pending ([A-Za-z0-9_-]+)\]`)
			reviewed, finished := false, false
			var transcript strings.Builder
			for scanner.Scan() {
				line := scanner.Text()
				transcript.WriteString(line + "\n")
				if m := pending.FindStringSubmatch(line); len(m) == 2 && !reviewed {
					reviewed = true
					decision := "approve"
					if mode == "deny" {
						decision = "deny"
					}
					_, _ = fmt.Fprintf(input, "/%s %s\n", decision, m[1])
				}
				if strings.TrimSpace(line) == "[task completed]" || strings.Contains(line, "Task did not complete successfully.") {
					finished = true
					_, _ = io.WriteString(input, "/quit\n")
				}
			}
			err = cmd.Wait()
			waited = true
			if err != nil || scanner.Err() != nil || !reviewed || !finished {
				t.Fatal("PTY qualification failed", err, scanner.Err(), transcript.String(), stderr.String())
			}
			data, readErr := os.ReadFile(target)
			switch mode {
			case "approve":
				if readErr != nil || string(data) != "approved fixture\n" {
					t.Fatal(string(data), readErr)
				}
			case "deny":
				if !os.IsNotExist(readErr) {
					t.Fatal("denied create wrote file", string(data), readErr)
				}
			case "existing":
				if readErr != nil || string(data) != "original" {
					t.Fatal("existing file changed", string(data), readErr)
				}
			}
		})
	}
}

func TestChatCreateApprovalPipeChild(t *testing.T) {
	mode := os.Getenv("DARWIN_CREATE_PIPE_SIDE")
	if mode != "pipe_input" && mode != "pipe_output" {
		t.Skip("owned subprocess only")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	in, out := os.Stdin, os.Stdout
	if mode == "pipe_input" {
		in = r
	} else {
		out = w
	}
	code := RunWithInput([]string{"chat", "--config", os.Getenv("DARWIN_CHAT_LIVE_PROCESS_CONFIG"), "--model", "chat"}, in, out, os.Stderr, "fixture")
	if code != 1 {
		t.Fatal("pipe was not rejected", code)
	}
	fmt.Fprintln(os.Stderr, "PIPE_REJECTED")
}
