package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// The provider cannot complete until the client receives actual content, not
// merely response headers or an assistant-role chunk. This catches buffered
// implementations even when their final SSE envelope is otherwise correct.
func TestOpenAILiveHTTPContentRedactionAndDisconnect(t *testing.T) {
	for _, mode := range []string{"success", "provider_failure", "disconnect"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			release := make(chan struct{})
			stopped := make(chan struct{})
			prefix := strings.Repeat("safe output ", 128) + "\n"
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(stopped)
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/x-ndjson")
				fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":false}\n", prefix)
				w.(http.Flusher).Flush()
				select {
				case <-ctx.Done():
					return
				case <-r.Context().Done():
					return
				case <-release:
				}
				if mode == "provider_failure" {
					fmt.Fprintln(w, `{"error":"fixture upstream failure"}`)
					return
				}
				// A credential divided across transport chunks must be removed
				// before either fragment reaches the HTTP consumer.
				mid := len(token) / 2
				fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":false}\n", token[:mid])
				w.(http.Flusher).Flush()
				fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", token[mid:]+" answer")
			}))
			defer func() { cancel(); provider.Close() }()
			cfg := config.Defaults()
			cfg.Mode = "local_only"
			cfg.Telemetry.Database = filepath.Join(t.TempDir(), "events.db")
			cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
			cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}}}
			svc, err := app.NewService(cfg, func(name string) string {
				if name == "DARWIN_API_TOKEN" {
					return token
				}
				return ""
			})
			if err != nil {
				t.Fatal(err)
			}
			h, err := New(token, 1, Services{Run: svc.Run, RunTextStream: svc.RunTextStream,
				Inspect: func(context.Context, string) (sessions.Snapshot, error) { return sessions.Snapshot{}, nil },
				Health:  func(context.Context) error { return nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(h)
			defer func() { cancel(); server.Close() }()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"chat","messages":[{"role":"user","content":"hello"}],"stream":true}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			response, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
				body, _ := io.ReadAll(response.Body)
				t.Fatalf("unexpected response: %s %s", response.Status, body)
			}
			scanner := bufio.NewScanner(response.Body)
			scanner.Buffer(make([]byte, 4096), 2<<20)
			var content, wire strings.Builder
			gotContent, done, failed := false, false, false
			finish := ""
			for scanner.Scan() {
				line := scanner.Text()
				wire.WriteString(line)
				wire.WriteByte('\n')
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				data := strings.TrimPrefix(line, "data: ")
				if data == "[DONE]" {
					if done {
						t.Fatal("duplicate completion sentinel")
					}
					done = true
					continue
				}
				var chunk struct {
					Error   json.RawMessage `json:"error"`
					Choices []struct {
						Delta struct {
							Content string `json:"content"`
						} `json:"delta"`
						Finish *string `json:"finish_reason"`
					} `json:"choices"`
				}
				if err := json.Unmarshal([]byte(data), &chunk); err != nil {
					t.Fatalf("invalid SSE payload %q: %v", data, err)
				}
				failed = failed || (len(chunk.Error) > 0 && string(chunk.Error) != "null")
				for _, choice := range chunk.Choices {
					if choice.Finish != nil {
						finish = *choice.Finish
					}
					content.WriteString(choice.Delta.Content)
					if choice.Delta.Content != "" && !gotContent {
						gotContent = true
						if mode == "disconnect" {
							response.Body.Close()
							select {
							case <-stopped:
							case <-ctx.Done():
								t.Fatal("client disconnect did not stop provider")
							}
							return
						}
						close(release)
					}
				}
			}
			if err := scanner.Err(); err != nil {
				t.Fatalf("stream failed (received live content=%v): %v", gotContent, err)
			}
			if !gotContent || strings.Contains(wire.String(), token[:len(token)/2]) || strings.Contains(wire.String(), token[len(token)/2:]) {
				t.Fatalf("missing live content or credential fragment exposed: %s", wire.String())
			}
			if mode == "success" {
				if !done || failed || finish != "stop" || content.String() != prefix+"[REDACTED] answer" {
					t.Fatalf("incorrect completed stream: done=%v failed=%v finish=%q text=%q", done, failed, finish, content.String())
				}
			} else if done || !failed || finish != "" {
				t.Fatalf("failed provider presented as complete: done=%v failed=%v finish=%q", done, failed, finish)
			}
			select {
			case <-stopped:
			case <-ctx.Done():
				t.Fatal("provider remained active after stream ended")
			}
		})
	}
}
