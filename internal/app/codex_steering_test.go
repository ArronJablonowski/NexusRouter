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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/codexbridge"
	"github.com/ArronJablonowski/NexusRouter/internal/codexrpc"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

// A synthetic wire, not a Codex subprocess. Reads block until the owned fixture
// releases native notifications; real application journals and tools are used.
type codexSteeringWire struct {
	mu         sync.Mutex
	queue      chan codexrpc.Envelope
	closed     chan struct{}
	once       sync.Once
	entered    chan struct{}
	writes     []codexrpc.Envelope
	turns      int
	tool, fail bool
	check      func(codexrpc.Envelope) error
}

func (w *codexSteeringWire) Close() error { w.once.Do(func() { close(w.closed) }); return nil }
func (w *codexSteeringWire) Read() (codexrpc.Envelope, error) {
	select {
	case e := <-w.queue:
		return e, nil
	case <-w.closed:
		return codexrpc.Envelope{}, io.EOF
	}
}
func (w *codexSteeringWire) send(e codexrpc.Envelope) {
	select {
	case w.queue <- e:
	case <-w.closed:
	}
}
func (w *codexSteeringWire) notice(method, params string) {
	w.send(codexrpc.Envelope{Method: method, Params: json.RawMessage(params)})
}
func (w *codexSteeringWire) final(turn, answer string) {
	w.notice("item/started", fmt.Sprintf(`{"threadId":"steering-thread","turnId":%q,"item":{"id":%q,"type":"agentMessage","phase":"final_answer","text":""}}`, turn, "answer-"+turn))
	w.notice("item/completed", fmt.Sprintf(`{"threadId":"steering-thread","turnId":%q,"item":{"id":%q,"type":"agentMessage","phase":"final_answer","text":%q}}`, turn, "answer-"+turn, answer))
	w.notice("turn/completed", fmt.Sprintf(`{"threadId":"steering-thread","turn":{"id":%q,"status":"completed"}}`, turn))
}
func (w *codexSteeringWire) Write(e codexrpc.Envelope) error {
	body, _ := json.Marshal(e)
	var own codexrpc.Envelope
	if json.Unmarshal(body, &own) != nil {
		return errors.New("fixture encoding")
	}
	w.mu.Lock()
	w.writes = append(w.writes, own)
	w.mu.Unlock()
	response := func(result string) { w.send(codexrpc.Envelope{ID: bytes.Clone(e.ID), Result: json.RawMessage(result)}) }
	switch e.Method {
	case "initialize":
		response(`{"userAgent":"fixture"}`)
	case "initialized":
	case "thread/start":
		response(`{"model":"gpt-5.6-sol","thread":{"id":"steering-thread"}}`)
	case "thread/inject_items":
		response(`{}`)
	case "turn/start":
		w.turns++
		if w.turns == 1 {
			response(`{"turn":{"id":"first-turn","status":"inProgress"}}`)
			close(w.entered)
			if w.tool {
				w.notice("item/started", `{"threadId":"steering-thread","turnId":"first-turn","item":{"type":"dynamicToolCall","id":"call-1","tool":"delegate","namespace":"darwin","arguments":{"prompt":"Work","validation":"text"},"status":"inProgress"}}`)
				w.send(codexrpc.Envelope{ID: json.RawMessage("50"), Method: "item/tool/call", Params: json.RawMessage(`{"threadId":"steering-thread","turnId":"first-turn","callId":"call-1","tool":"delegate","namespace":"darwin","arguments":{"prompt":"Work","validation":"text"}}`)})
			}
		} else {
			if err := w.check(e); err != nil {
				return err
			}
			response(`{"turn":{"id":"second-turn","status":"inProgress"}}`)
			w.final("second-turn", "revised answer")
		}
	case "turn/steer":
		if err := w.check(e); err != nil {
			return err
		}
		if w.fail {
			return errors.New("fixture steering transport failed")
		}
		response(`{"turnId":"first-turn"}`)
	case "":
		if string(e.ID) != "50" {
			return errors.New("unexpected tool response")
		}
		w.notice("item/completed", `{"threadId":"steering-thread","turnId":"first-turn","item":{"type":"dynamicToolCall","id":"call-1","tool":"delegate","namespace":"darwin","arguments":{"prompt":"Work","validation":"text"},"status":"completed","success":true}}`)
		w.final("first-turn", "revised answer")
	default:
		return errors.New("unexpected fixture method")
	}
	return nil
}

func TestCodexDurableSteeringAtNativeBoundaries(t *testing.T) {
	for _, mode := range []string{"completed_segment", "paused_tool", "paused_tool_failure", "canceled_segment"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			tool := strings.HasPrefix(mode, "paused_tool")
			wire := &codexSteeringWire{queue: make(chan codexrpc.Envelope, 32), closed: make(chan struct{}), entered: make(chan struct{}), tool: tool, fail: mode == "paused_tool_failure"}
			defer wire.Close()
			cfg := codexTaskConfig(t)
			workerEntered, workerRelease := make(chan struct{}), make(chan struct{})
			var localCalls atomic.Int64
			local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				localCalls.Add(1)
				close(workerEntered)
				select {
				case <-workerRelease:
				case <-ctx.Done():
					return
				}
				fmt.Fprintln(w, `{"message":{"content":"owned local result"},"done":true,"done_reason":"stop"}`)
			}))
			defer func() { cancel(); local.Close() }()
			if tool {
				cfg.Workers.Max = 2
				cfg.Workers.DelegateModel = "worker"
				cfg.Workers.DelegateMaxCalls = 1
				cfg.Providers = append(cfg.Providers, config.Provider{ID: "local", Kind: "ollama", Endpoint: local.URL})
				zero := 0.0
				cfg.Models = append(cfg.Models, config.Model{ID: "worker", Provider: "local", Model: "fixture-local", Locality: "local", RAMBytes: 1, ContextTokens: 4096, EstimatedCost: &zero, Capabilities: []string{"chat"}})
			}
			svc, err := NewService(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			svc.profile = func(context.Context) (resources.Snapshot, error) {
				return resources.Snapshot{Time: time.Now(), CPUs: 4, TotalRAM: 8 << 30, AvailableRAM: 7 << 30}, nil
			}
			svc.codexLauncher = func(ctx context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
				return codexbridge.NewSession(ctx, wire, codexbridge.Options{Model: spec.Model, CWD: spec.CWD})
			}
			control, err := NewService(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			started := make(chan string, 1)
			type completion struct {
				out Result
				err error
			}
			done := make(chan completion, 1)
			joined := false
			defer func() {
				cancel()
				wire.Close()
				if !joined {
					<-done
				}
			}()
			var task string
			var durableTool string
			wire.check = func(e codexrpc.Envelope) error {
				var p struct {
					ThreadID, ExpectedTurnID, Model string
					Input                           []struct{ Type, Text string }
					Environments                    []any
				}
				if json.Unmarshal(e.Params, &p) != nil || p.ThreadID != "steering-thread" || len(p.Input) != 1 || p.Input[0].Type != "text" || p.Input[0].Text != "Reconsider using the saved result" {
					return errors.New("guidance missing")
				}
				if e.Method == "turn/steer" && p.ExpectedTurnID != "first-turn" || e.Method == "turn/start" && (p.Model != "gpt-5.6-sol" || p.Environments == nil || len(p.Environments) != 0) {
					return errors.New("native steering authority changed")
				}
				db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
				if err != nil {
					return err
				}
				defer db.Close()
				events, err := db.Read(ctx, task, 0, 100)
				if err != nil {
					return err
				}
				applied := false
				var toolSequence, steeringSequence int64
				for _, event := range events {
					if event.Kind == runtime.ToolCompleted {
						durableTool = event.Data.Text
						toolSequence = event.Sequence
					}
					if event.Kind == runtime.SteeringApplied && event.Data.Text == "Reconsider using the saved result" {
						applied = true
						steeringSequence = event.Sequence
					}
				}
				if !applied || tool && (durableTool == "" || toolSequence >= steeringSequence) {
					return errors.New("native steering preceded durable application")
				}
				return nil
			}
			go func() {
				out, err := svc.RunStream(ctx, Request{ModelID: "brain", Prompt: "Work then review"}, func(e runtime.Event) error {
					if e.Kind == runtime.TaskStarted {
						started <- e.TaskID
					}
					return nil
				})
				done <- completion{out, err}
			}()
			select {
			case task = <-started:
			case <-ctx.Done():
				t.Fatal("task start timeout")
			}
			select {
			case <-wire.entered:
			case <-ctx.Done():
				t.Fatal("native turn timeout")
			}
			if tool {
				select {
				case <-workerEntered:
				case <-ctx.Done():
					t.Fatal("worker timeout")
				}
			}
			guidance, err := control.SteerTask(ctx, task, "codex-steer-key", "Reconsider using the saved result")
			if err != nil || guidance.State != "pending" {
				t.Fatal(guidance, err)
			}
			if mode == "canceled_segment" {
				cancel()
			} else if tool {
				close(workerRelease)
			} else {
				wire.final("first-turn", "initial answer")
			}
			var result completion
			select {
			case result = <-done:
				joined = true
			case <-time.After(5 * time.Second):
				t.Fatal("steered task did not join")
			}
			failed := wire.fail || mode == "canceled_segment"
			if failed {
				if result.err == nil {
					t.Fatal("failed steering became success", result)
				}
			} else if result.err != nil || result.out.Text != "revised answer" {
				t.Fatalf("result=%+v error=%v", result.out, result.err)
			}
			inspect, stop := context.WithTimeout(context.Background(), time.Second)
			db, err := telemetry.OpenReadOnly(inspect, cfg.Telemetry.Database)
			if err != nil {
				stop()
				t.Fatal(err)
			}
			history, err := db.Read(inspect, task, 0, 100)
			receipts, receiptErr := db.ListSteering(inspect, task)
			db.Close()
			stop()
			if err != nil || len(history) == 0 {
				t.Fatal("missing durable outcome", err)
			}
			terminal := history[len(history)-1].Kind
			wantTerminal := runtime.TaskCompleted
			wantSteering := "applied"
			if mode == "paused_tool_failure" {
				wantTerminal = runtime.TaskFailed
			}
			if mode == "canceled_segment" {
				wantTerminal, wantSteering = runtime.TaskCanceled, "pending"
			}
			if terminal != wantTerminal {
				t.Fatal("false durable completion", terminal)
			}
			if receiptErr != nil || len(receipts) != 1 || receipts[0].ID != guidance.ID || receipts[0].State != wantSteering || (receipts[0].AppliedSequence != nil) != (wantSteering == "applied") {
				t.Fatal("incorrect durable steering receipt", receipts, receiptErr)
			}
			if wantSteering == "applied" {
				sequence := *receipts[0].AppliedSequence
				if sequence < 1 || sequence > int64(len(history)) || history[sequence-1].Kind != runtime.SteeringApplied || history[sequence-1].Data.SteeringID != guidance.ID {
					t.Fatal("steering receipt lost its exact journal event")
				}
			}
			toolResults := 0
			for _, event := range history {
				if event.Kind == runtime.ToolCompleted {
					toolResults++
				}
			}
			if tool && toolResults != 1 || !tool && toolResults != 0 {
				t.Fatal("tool result replayed in journal", toolResults)
			}
			wire.mu.Lock()
			writes := append([]codexrpc.Envelope(nil), wire.writes...)
			wire.mu.Unlock()
			threads, turns, steers, replies := 0, 0, 0, 0
			for _, write := range writes {
				switch write.Method {
				case "thread/start":
					threads++
				case "turn/start":
					turns++
				case "turn/steer":
					steers++
				case "":
					if string(write.ID) == "50" {
						replies++
						var reply struct{ ContentItems []struct{ Text string } }
						if json.Unmarshal(write.Result, &reply) != nil || len(reply.ContentItems) != 1 || reply.ContentItems[0].Text != durableTool {
							t.Fatal("tool result changed or reran", string(write.Result), durableTool)
						}
					}
				}
			}
			if threads != 1 {
				t.Fatal("steering recreated thread", threads)
			}
			if tool {
				if localCalls.Load() != 1 || turns != 1 || steers != 1 || (!failed && replies != 1) || (failed && replies != 0) {
					t.Fatal("tool steering replayed or bypassed ack", localCalls.Load(), turns, steers, replies)
				}
			} else {
				want := 2
				if failed {
					want = 1
				}
				if turns != want || steers != 0 || replies != 0 || localCalls.Load() != 0 {
					t.Fatal("completed boundary resumed incorrectly", turns, steers, replies)
				}
			}
		})
	}
}
