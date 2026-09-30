//go:build darwin

package cli

import (
	"bufio"
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

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"go.yaml.in/yaml/v3"
)

func TestChatReplaceApprovalRealPTY(t *testing.T) {
	for _, mode := range []string{"approve", "deny", "pipe_input", "pipe_output"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if calls.Add(1) == 1 {
					fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"replace_file","arguments":{"path":"existing.txt","expected_content":"original\n","content":"replacement\n"}}}]},"done":true,"done_reason":"tool_calls"}`)
				} else {
					fmt.Fprintln(w, `{"message":{"content":"replacement fixture finished"},"done":true,"done_reason":"stop"}`)
				}
			}))
			defer server.Close()
			root := t.TempDir()
			target := filepath.Join(root, "existing.txt")
			if err := os.WriteFile(target, []byte("original\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := config.Defaults()
			cfg.Mode = "local_only"
			cfg.Telemetry.Database = filepath.Join(t.TempDir(), "events.db")
			cfg.Tools.Enabled, cfg.Tools.ReplaceEnabled = true, true
			cfg.Tools.ReadRoot, cfg.Tools.ReplaceRoot = root, root
			cfg.Providers = []config.Provider{{ID: "fixture", Kind: "ollama", Endpoint: server.URL}}
			zero := 0.0
			cfg.Models = []config.Model{{ID: "chat", Provider: "fixture", Model: "fixture", Locality: "local", RAMBytes: 1, ContextTokens: 16384, Capabilities: []string{"chat"}, EstimatedCost: &zero}}
			body, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			env := []string{}
			for _, v := range os.Environ() {
				if !strings.HasPrefix(v, "DARWIN_") && !strings.HasPrefix(v, "GORACE=") {
					env = append(env, v)
				}
			}
			env = append(env, "DARWIN_PROCESS_OWNER_DIR="+filepath.Join(t.TempDir(), "owners"), "DARWIN_CHAT_LIVE_PROCESS_CHILD=1", "DARWIN_CHAT_LIVE_PROCESS_CONFIG="+path, "GORACE=atexit_sleep_ms=0")
			if strings.HasPrefix(mode, "pipe_") {
				cmd := exec.CommandContext(ctx, "/usr/bin/script", "-q", "/dev/null", os.Args[0], "-test.run=^TestChatCreateApprovalPipeChild$")
				cmd.Env = append(env, "DARWIN_CREATE_PIPE_SIDE="+mode)
				cmd.WaitDelay = time.Second
				out, err := cmd.CombinedOutput()
				if err != nil || !strings.Contains(string(out), "PIPE_REJECTED") || calls.Load() != 0 {
					t.Fatal(string(out), err, calls.Load())
				}
				if _, err := os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
					t.Fatal("pipe rejection touched DB", err)
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
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			_, _ = io.WriteString(input, "replace fixture file\n")
			scanner := bufio.NewScanner(io.LimitReader(output, 1<<20))
			scanner.Buffer(make([]byte, 4096), 1<<20)
			pending := regexp.MustCompile(`\[approval pending ([A-Za-z0-9_-]+)\]`)
			id := ""
			reviewed, finished := false, false
			var transcript strings.Builder
			for scanner.Scan() {
				line := scanner.Text()
				transcript.WriteString(line + "\n")
				if match := pending.FindStringSubmatch(line); len(match) == 2 {
					id = match[1]
				}
				// Decide only after the entire old/new preview and exact-ID command
				// instructions have been delivered to the real terminal.
				if id != "" && !reviewed && strings.Contains(line, "for this request only.") {
					reviewed = true
					_, _ = fmt.Fprintf(input, "/%s %s\n", mode, id)
				}
				if strings.TrimSpace(line) == "[task completed]" || strings.Contains(line, "Task did not complete successfully.") {
					finished = true
					_, _ = io.WriteString(input, "/quit\n")
				}
			}
			err = cmd.Wait()
			waited = true
			if err != nil || scanner.Err() != nil || !reviewed || !finished {
				t.Fatal("replacement PTY failed", err, scanner.Err(), transcript.String())
			}
			for _, preview := range []string{`"original\n"`, `"replacement\n"`, "external writers not fenced"} {
				if !strings.Contains(transcript.String(), preview) {
					t.Fatal("incomplete terminal preview", preview)
				}
			}
			got, err := os.ReadFile(target)
			want := "original\n"
			if mode == "approve" {
				want = "replacement\n"
			}
			if err != nil || string(got) != want {
				t.Fatal("wrong replacement result", string(got), err)
			}
		})
	}
}
