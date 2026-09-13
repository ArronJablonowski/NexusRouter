package v1_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"github.com/ArronJablonowski/DarwinRouter/tools"
	"go.yaml.in/yaml/v3"
)

// Independent Clients intentionally share only the durable store. Overlap must
// be governed by scoped reader/writer leases, not a private Service mutex.
func TestSDKSharedReadLeasesBlockWriterUntilCallbacksJoin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []providers.Message `json:"messages"`
			Tools    []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Tools) != 1 {
			http.Error(w, "fixture", 400)
			return
		}
		for _, message := range request.Messages {
			if message.Role == "tool" {
				fmt.Fprintln(w, `{"message":{"content":"operation finished"},"done":true,"done_reason":"stop"}`)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"tool_calls": []any{map[string]any{"function": map[string]any{"name": request.Tools[0].Function.Name, "arguments": map[string]any{}}}}}, "done": true, "done_reason": "tool_calls"})
	}))
	defer server.Close()
	options, database := sdkToolOptions(t)
	body, err := os.ReadFile(options.ProjectFile)
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Settings
	if err = yaml.Unmarshal(body, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Providers[0].Endpoint = server.URL
	body, err = yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(options.ProjectFile, body, 0600); err != nil {
		t.Fatal(err)
	}
	const scope = "shared-artifact"
	var effects atomic.Int32
	newClient := func(name, scope string, reader bool, handler func(context.Context, json.RawMessage) (runtime.ToolResult, error)) *sdk.Client {
		t.Helper()
		copy := options
		behavior := sdk.BehaviorIdempotentWrite
		if reader {
			behavior = sdk.BehaviorReadOnly
		}
		copy.Tools = []sdk.Tool{{Tool: providers.Tool{Name: name, Parameters: json.RawMessage(`{"type":"object","additionalProperties":false}`)}, Scope: scope, ReadOnly: reader, Behavior: behavior, Handler: handler}}
		copy.ToolPolicy = &sdk.ToolPolicy{Default: tools.Allow}
		if !reader {
			copy.ApprovalReviewer = func(context.Context, sdk.ApprovalPrompt) (string, bool, error) { return "fixture-operator", true, nil }
		}
		client, err := sdk.New(copy)
		if err != nil {
			t.Fatal(err)
		}
		return client
	}
	entered := make(chan int, 2)
	canceledObserved := make(chan struct{})
	releases := []chan struct{}{make(chan struct{}), make(chan struct{})}
	var releaseOnce [2]sync.Once
	release := func(i int) { releaseOnce[i].Do(func() { close(releases[i]) }) }
	defer func() { release(0); release(1) }()
	readerCtx, stopReader := context.WithCancel(ctx)
	defer stopReader()
	done := []chan error{make(chan error, 1), make(chan error, 1)}
	for i := 0; i < 2; i++ {
		index := i
		reader := newClient("read_shared", scope, true, func(ctx context.Context, _ json.RawMessage) (runtime.ToolResult, error) {
			entered <- index
			select {
			case <-releases[index]:
			case <-ctx.Done():
				if index == 0 {
					close(canceledObserved)
				}
				// Explicitly exercise delayed cooperative cleanup: cancellation
				// cannot release ownership while this callback still runs.
				<-releases[index]
			}
			return runtime.ToolResult{Content: "observed", Effect: runtime.NoEffect}, nil
		})
		runCtx := ctx
		if index == 0 {
			runCtx = readerCtx
		}
		go func() {
			_, err := reader.Run(runCtx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "read scoped state"})
			done[index] <- err
		}()
	}
	joined := [2]bool{}
	defer func() {
		cancel()
		release(0)
		release(1)
		for i := range done {
			if !joined[i] {
				select {
				case <-done[i]:
				case <-time.After(2 * time.Second):
					t.Error("reader did not join")
				}
			}
		}
	}()
	for range 2 {
		select {
		case <-entered:
		case err := <-done[0]:
			joined[0] = true
			t.Fatalf("first reader exited before overlap: %v", err)
		case err := <-done[1]:
			joined[1] = true
			t.Fatalf("second reader exited before overlap: %v", err)
		case <-ctx.Done():
			t.Fatal("readers failed to overlap")
		}
	}
	db, err := telemetry.OpenReadOnly(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertReaders := func(want int) {
		t.Helper()
		leases, err := db.InspectLeases(ctx, scope)
		if err != nil || len(leases) != want {
			t.Fatal("unexpected active reader leases", len(leases), want, err)
		}
		for _, lease := range leases {
			if lease.Writer || lease.Released {
				t.Fatal("read handler lacks active reader lease")
			}
		}
	}
	assertReaders(2)
	writer := newClient("write_shared", scope, false, func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
		effects.Add(1)
		return runtime.ToolResult{Content: "saved", Effect: runtime.ConfirmedEffect}, nil
	})
	if _, err := writer.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "write scoped state"}); err == nil || effects.Load() != 0 {
		t.Fatal("writer entered while readers active", err, effects.Load())
	}
	assertReaders(2)
	other := newClient("write_other", "independent-artifact", false, func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
		return runtime.ToolResult{Content: "saved independently", Effect: runtime.ConfirmedEffect}, nil
	})
	if _, err := other.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "write independent state"}); err != nil {
		t.Fatal("unrelated scope unnecessarily blocked", err)
	}
	stopReader()
	select {
	case <-canceledObserved:
	case <-ctx.Done():
		t.Fatal("read callback did not observe cancellation")
	}
	select {
	case <-done[0]:
		joined[0] = true
		t.Fatal("canceled task returned before callback joined")
	default:
	}
	assertReaders(2)
	if _, err := writer.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "write after cancellation"}); err == nil || effects.Load() != 0 {
		t.Fatal("cancellation prematurely released reader ownership", err)
	}
	release(0)
	select {
	case err := <-done[0]:
		joined[0] = true
		if !errors.Is(err, context.Canceled) {
			t.Fatal("canceled reader not reported", err)
		}
	case <-ctx.Done():
		t.Fatal("released canceled reader did not join")
	}
	assertReaders(1)
	release(1)
	select {
	case err := <-done[1]:
		joined[1] = true
		if err != nil {
			t.Fatal("successful reader failed", err)
		}
	case <-ctx.Done():
		t.Fatal("reader did not finish")
	}
	assertReaders(0)
	if _, err := writer.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "write after readers finish"}); err != nil || effects.Load() != 1 {
		t.Fatal("writer did not enter after readers joined", err, effects.Load())
	}
	assertReaders(0)
}
