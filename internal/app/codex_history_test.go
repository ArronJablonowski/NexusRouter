package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/codexbridge"
	"github.com/ArronJablonowski/NexusRouter/internal/codexrpc"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/resources"
)

// Protocol fixture: no Codex process or cloud inference is started. Only fresh
// turn/start emits an answer; injected tool items cannot execute any tool.
type codexHistoryWire struct {
	mu     sync.Mutex
	queue  []codexrpc.Envelope
	writes []codexrpc.Envelope
	closed bool
}

func (w *codexHistoryWire) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return nil
}
func (w *codexHistoryWire) Read() (codexrpc.Envelope, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || len(w.queue) == 0 {
		return codexrpc.Envelope{}, io.EOF
	}
	e := w.queue[0]
	w.queue = w.queue[1:]
	return e, nil
}
func (w *codexHistoryWire) Write(e codexrpc.Envelope) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return io.ErrClosedPipe
	}
	body, _ := json.Marshal(e)
	var own codexrpc.Envelope
	if json.Unmarshal(body, &own) != nil {
		return errors.New("fixture")
	}
	w.writes = append(w.writes, own)
	result := ""
	switch e.Method {
	case "initialize":
		result = `{"userAgent":"fixture"}`
	case "initialized":
		return nil
	case "thread/start":
		result = `{"model":"gpt-5.6-sol","thread":{"id":"new-thread"}}`
	case "thread/inject_items":
		result = `{}`
	case "turn/start":
		result = `{"turn":{"id":"new-turn","status":"inProgress"}}`
	default:
		return errors.New("unexpected fixture method")
	}
	w.queue = append(w.queue, codexrpc.Envelope{ID: bytes.Clone(e.ID), Result: json.RawMessage(result)})
	if e.Method == "turn/start" {
		w.queue = append(w.queue,
			codexrpc.Envelope{Method: "item/started", Params: json.RawMessage(`{"threadId":"new-thread","turnId":"new-turn","item":{"id":"answer","type":"agentMessage","phase":"final_answer","text":""}}`)},
			codexrpc.Envelope{Method: "item/completed", Params: json.RawMessage(`{"threadId":"new-thread","turnId":"new-turn","item":{"id":"answer","type":"agentMessage","phase":"final_answer","text":"Reviewed saved work without rerunning it."}}`)},
			codexrpc.Envelope{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"new-thread","turn":{"id":"new-turn","status":"completed"}}`)})
	}
	return nil
}

func TestCodexExplicitContinuationImportsDurableHistoryWithoutRerunningTools(t *testing.T) {
	ctx := context.Background()
	var localCalls atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localCalls.Add(1)
		fmt.Fprintln(w, `{"message":{"content":"package answer\nfunc Answer() int { return 42 }"},"done":true,"done_reason":"stop"}`)
	}))
	defer local.Close()
	cfg := codexTaskConfig(t)
	cfg.Workers.Max = 2
	cfg.Workers.DelegateModel = "worker"
	cfg.Workers.DelegateMaxCalls = 1
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "local", Kind: "ollama", Endpoint: local.URL})
	zero := 0.0
	cfg.Models = append(cfg.Models, config.Model{ID: "worker", Provider: "local", Model: "fixture-local", Locality: "local", RAMBytes: 1, ContextTokens: 4096, EstimatedCost: &zero, Capabilities: []string{"chat"}})
	const rotatedSecret = "rotated-private-token-12345678901234567890"
	currentSecret := ""
	svc, err := NewService(cfg, func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return currentSecret
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now(), CPUs: 4, TotalRAM: 64 << 30, AvailableRAM: 56 << 30, SwapUsed: new(uint64)}, nil
	}
	first := &codexTaskFixture{}
	svc.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) { return first, nil }
	previous, err := svc.Run(ctx, Request{ModelID: "brain", Prompt: "Delegate then review " + rotatedSecret, Domain: "code"})
	if err != nil || localCalls.Load() != 1 {
		t.Fatal(previous, err, localCalls.Load())
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before, err := db.Read(ctx, previous.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) == 0 || len(before[0].Data.Messages) == 0 || !bytes.Contains([]byte(before[0].Data.Messages[0].Content), []byte(rotatedSecret)) {
		t.Fatal("fixture did not persist pre-rotation source")
	}
	currentSecret = rotatedSecret
	wire := &codexHistoryWire{}
	launches := 0
	svc.codexLauncher = func(ctx context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
		launches++
		if spec.Privacy != "cloud_allowed" || spec.Model != "gpt-5.6-sol" {
			t.Fatal(spec.Privacy, spec.Model)
		}
		return codexbridge.NewSession(ctx, wire, codexbridge.Options{Model: spec.Model, CWD: spec.CWD})
	}
	result, err := svc.Run(ctx, Request{ModelID: "brain", ContinueTaskID: previous.TaskID, Prompt: "Explain the saved answer", Domain: "code"})
	if err != nil || result.Text != "Reviewed saved work without rerunning it." || launches != 1 || localCalls.Load() != 1 {
		t.Fatal(result, err, launches, localCalls.Load())
	}
	after, err := db.Read(ctx, previous.TaskID, 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("source history changed", err)
	}
	snapshot, err := db.TaskSnapshot(ctx, result.TaskID)
	if err != nil || snapshot.ParentTaskID != previous.TaskID || snapshot.SessionID != before[0].SessionID || snapshot.Privacy != "cloud_allowed" {
		t.Fatal(snapshot, err)
	}
	imported, started := 0, 0
	for _, sent := range wire.writes {
		switch sent.Method {
		case "thread/inject_items":
			imported++
			var params struct {
				ThreadID string `json:"threadId"`
				Items    []struct {
					Type, Role, CallID, Name string
					Content                  json.RawMessage
					Output                   json.RawMessage
				} `json:"items"`
			}
			if json.Unmarshal(sent.Params, &params) != nil || params.ThreadID != "new-thread" || len(params.Items) < 4 {
				t.Fatal(string(sent.Params))
			}
			if !bytes.Contains(sent.Params, []byte("function_call_output")) || !bytes.Contains(sent.Params, []byte("untrusted_output")) || !bytes.Contains(sent.Params, []byte("local-call")) || bytes.Contains(sent.Params, []byte("Explain the saved answer")) {
				t.Fatal("history pairing/import failed", string(sent.Params))
			}
			if bytes.Contains(sent.Params, []byte(rotatedSecret)) || !bytes.Contains(sent.Params, []byte("[REDACTED]")) {
				t.Fatal("rotated secret reached imported history")
			}
		case "turn/start":
			started++
			if !bytes.Contains(sent.Params, []byte("Explain the saved answer")) || bytes.Contains(sent.Params, []byte("untrusted_output")) {
				t.Fatal("history flattened into new prompt")
			}
		}
	}
	if imported != 1 || started != 1 || !wire.closed {
		t.Fatal(imported, started, wire.closed)
	}
}
