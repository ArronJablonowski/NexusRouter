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

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestLiveTaskStreamDurabilityAndDisconnect(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(fmt.Sprint(disconnect), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			release := make(chan struct{})
			started := make(chan struct{})
			stopped := make(chan struct{})
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				close(started)
				defer close(stopped)
				select {
				case <-ctx.Done():
					return
				case <-r.Context().Done():
					return
				case <-release:
					fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", token+" answer")
				}
			}))
			defer provider.Close()
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
			db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			h, err := New(token, 1, Services{Run: svc.Run, RunStream: svc.RunStream,
				Inspect: func(ctx context.Context, id string) (sessions.Snapshot, error) { return sessions.Replay(ctx, db, id) },
				Health:  func(context.Context) error { return nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(h)
			defer server.Close()
			req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/tasks/stream", strings.NewReader(`{"model_id":"chat","prompt":"hello"}`))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			response, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
				t.Fatal(response.Status)
			}
			scanner := bufio.NewScanner(response.Body)
			scanner.Buffer(make([]byte, 4096), 2<<20)
			var first runtime.Event
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "data: ") {
					if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &first); err != nil {
						t.Fatal(err)
					}
					break
				}
			}
			if first.Kind != runtime.TaskStarted {
				t.Fatal("no live task start", first, scanner.Err())
			}
			stored, err := db.Read(ctx, first.TaskID, 0, 1)
			if err != nil || len(stored) != 1 || stored[0].ID != first.ID {
				t.Fatal("event not durable", stored, err)
			}
			// Provider cannot finish until the client has received a durable event.
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("provider never started")
			}
			if disconnect {
				response.Body.Close()
				select {
				case <-stopped:
				case <-ctx.Done():
					t.Fatal("disconnect did not cancel provider")
				}
				for {
					snapshot, err := sessions.Replay(ctx, db, first.TaskID)
					if err == nil && snapshot.State == "canceled" {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal("missing durable cancellation", snapshot, err)
					case <-time.After(10 * time.Millisecond):
					}
				}
				return
			}
			close(release)
			var output strings.Builder
			for scanner.Scan() {
				output.WriteString(scanner.Text())
				output.WriteByte('\n')
			}
			if scanner.Err() != nil {
				t.Fatal(scanner.Err())
			}
			if strings.Contains(output.String(), token) || !strings.Contains(output.String(), "event: result") || !strings.Contains(output.String(), "[REDACTED] answer") {
				t.Fatal(output.String())
			}
			snapshot, err := sessions.Replay(ctx, db, first.TaskID)
			if err != nil || snapshot.State != "completed" {
				t.Fatal(snapshot, err)
			}
		})
	}
}
