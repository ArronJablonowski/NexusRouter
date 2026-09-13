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
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestHTTPEventReplayReconnectDoesNotExecute(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		for i := 0; i < 120; i++ {
			fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":false}\n", token+" answer ")
		}
		fmt.Fprintln(w, `{"message":{"content":"done"},"done":true,"done_reason":"stop"}`)
	}))
	defer provider.Close()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "replay.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}}}
	svc, err := newAPIFixtureService(cfg, func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return token
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := svc.Run(ctx, app.Request{ModelID: "chat", Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	// The replay adapter uses a freshly opened read-only store, not the
	// execution journal's connection or any in-memory event tracking.
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before, err := db.Read(ctx, out.TaskID, 0, 1000)
	if err != nil || len(before) <= 100 {
		t.Fatal("fixture needs pagination", len(before), err)
	}
	h, err := New(token, 1, Services{Run: svc.Run, Events: db.ReadEventPage,
		Inspect: func(ctx context.Context, id string) (sessions.Snapshot, error) { return sessions.Replay(ctx, db, id) },
		Health:  func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	get := func(after int64) *http.Response {
		t.Helper()
		for {
			req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/v1/tasks/"+out.TaskID+"/events", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			if after > 0 {
				req.Header.Set("Last-Event-ID", out.TaskID+":"+strconv.FormatInt(after, 10))
			}
			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			// A closed client can reconnect before the prior handler has released
			// its slot. Retry only the explicit read-only capacity response.
			if resp.StatusCode == 503 && resp.Header.Get("Retry-After") == "1" {
				resp.Body.Close()
				select {
				case <-ctx.Done():
					t.Fatal("replay capacity did not recover")
				case <-time.After(time.Second):
				}
				continue
			}
			if resp.StatusCode != 200 {
				resp.Body.Close()
				t.Fatal(resp.Status)
			}
			return resp
		}
	}
	response := get(0)
	scanner := bufio.NewScanner(response.Body)
	var observed []runtime.Event
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event runtime.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		observed = append(observed, event)
		if len(observed) == 5 {
			break
		}
	}
	response.Body.Close()
	if len(observed) != 5 {
		t.Fatal("interrupted replay missing prefix", scanner.Err())
	}
	after := observed[len(observed)-1].Sequence
	for {
		response = get(after)
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || strings.Contains(string(body), token) {
			t.Fatal("unsafe replay", err)
		}
		frames := parseStreamFrames(t, string(body))
		if len(frames) == 0 || len(frames) > 101 {
			t.Fatal("unbounded or empty page", len(frames))
		}
		for _, frame := range frames[:len(frames)-1] {
			var event runtime.Event
			if json.Unmarshal([]byte(frame.data), &event) != nil || event.Sequence != after+1 || event.TaskID != out.TaskID || frame.id != out.TaskID+":"+strconv.FormatInt(event.Sequence, 10) {
				t.Fatal("replay gap or wrong attribution", frame)
			}
			observed = append(observed, event)
			after = event.Sequence
		}
		last := frames[len(frames)-1]
		var checkpoint struct {
			TaskID                     string `json:"task_id"`
			NextSequence, HeadSequence int64
			HasMore                    bool
		}
		// Use the public metadata schema rather than assuming completion from
		// the HTTP status or the end of this one bounded response.
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(last.data), &fields) != nil {
			t.Fatal(last)
		}
		_ = json.Unmarshal(fields["task_id"], &checkpoint.TaskID)
		_ = json.Unmarshal(fields["next_sequence"], &checkpoint.NextSequence)
		_ = json.Unmarshal(fields["head_sequence"], &checkpoint.HeadSequence)
		_ = json.Unmarshal(fields["has_more"], &checkpoint.HasMore)
		if last.event != "checkpoint" || last.id != "" || checkpoint.TaskID != out.TaskID || checkpoint.NextSequence != after || checkpoint.HeadSequence != int64(len(before)) {
			t.Fatal("invalid checkpoint", last)
		}
		if !checkpoint.HasMore {
			break
		}
	}
	if !reflect.DeepEqual(observed, before) {
		t.Fatal("reconnect changed or duplicated durable history")
	}
	response = get(after)
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || len(parseStreamFrames(t, string(body))) != 1 {
		t.Fatal("caught-up replay repeated events", err)
	}
	unchanged, err := db.Read(ctx, out.TaskID, 0, 1000)
	if err != nil || !reflect.DeepEqual(before, unchanged) || calls.Load() != 1 {
		t.Fatal("replay mutated or re-executed task", err, calls.Load())
	}
}
