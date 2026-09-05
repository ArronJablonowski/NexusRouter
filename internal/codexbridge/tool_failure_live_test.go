package codexbridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/codexrpc"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// Explicit supervised cloud inference. The only host tool returns fixed,
// effect-free fixture failure; no local model, filesystem tool or shell runs.
func TestLiveCodexRecoverableToolProtocol(t *testing.T) {
	if os.Getenv("DARWIN_CODEX_LIVE_FAILURE_REPAIR") != "1" {
		t.Skip("explicit supervised inference only")
	}
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("CLI unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal("fixture storage unavailable")
	}
	defer store.Close()
	const task = "codex-live-failure"
	env := []string{}
	for _, key := range []string{"HOME", "PATH", "TMPDIR", "CODEX_HOME"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	observation := failureLiveObservation{}
	session, err := launchChecked(ctx, LaunchSpec{Executable: bin, CWD: t.TempDir(), Model: "gpt-5.6-sol", Mode: "hybrid", Privacy: "cloud_allowed", Env: env}, func(ctx context.Context, spec codexrpc.ProcessSpec) (Wire, error) {
		process, err := codexrpc.StartProcess(ctx, spec)
		if err != nil {
			return nil, err
		}
		return &failureLiveWire{Wire: process, observation: &observation, ctx: ctx, store: store, task: task}, nil
	}, launchMetadata)
	if err != nil {
		t.Fatal("checked launch failed; payload withheld")
	}
	defer session.Close()
	executions := 0
	loop := runtime.Loop{Journal: store, Provider: session, Tools: sessionRuntimeExecutor(func(ctx context.Context, call providers.ToolCall) (runtime.ToolResult, error) {
		executions++
		var args struct{ Key string }
		if executions != 1 || call.Name != "probe" || json.Unmarshal(call.Arguments, &args) != nil || args.Key != "missing" {
			return runtime.ToolResult{Effect: runtime.NoEffect, Failed: true}, nil
		}
		events, err := store.Read(ctx, task, 0, 100)
		if err != nil || len(events) == 0 || events[len(events)-1].Kind != runtime.ToolStarted {
			return runtime.ToolResult{}, errors.New("fixture durability boundary")
		}
		return runtime.ToolResult{Content: `{"error":"fixture_not_found"}`, Failed: true, Recoverable: true, Effect: runtime.NoEffect}, nil
	})}
	result, runErr := loop.Run(ctx, runtime.RunRequest{TaskID: task, SessionID: "live-failure-session", ProviderID: "codex", RequireText: true, MaxTurns: 2, MaxOutputBytes: 4096, Inference: providers.Request{Model: "gpt-5.6-sol", Messages: []providers.Message{{Role: "user", Content: "Supervised protocol test using public synthetic data only. Call darwin.probe exactly once with key missing. After its failure, do not retry or call any other tool. Give one short final sentence acknowledging that the fixture was not found. Do not read files or use shell."}}, Tools: []providers.Tool{{Name: "probe", Description: "Return a controlled effect-free missing-fixture result", Parameters: json.RawMessage(`{"type":"object","properties":{"key":{"type":"string","enum":["missing"]}},"required":["key"],"additionalProperties":false}`)}}}})
	closeErr := session.Close()
	t.Logf("live metadata: executions=%d false_responses=%d completed_items=%d status=%s success_present=%t success=%t turns=%d", executions, observation.falseResponses, observation.completions, observation.status, observation.hasSuccess, observation.success, result.Turns)
	if runErr != nil || closeErr != nil {
		t.Fatal("live failure exchange failed; payload withheld")
	}
	if executions != 1 || observation.falseResponses != 1 || observation.completions != 1 || observation.status != "failed" || !observation.hasSuccess || observation.success || result.Turns != 2 || strings.TrimSpace(result.Text) == "" {
		t.Fatal("live failure exchange did not satisfy bounded contract")
	}
	snapshot, err := sessions.Replay(ctx, store, task)
	if err != nil || snapshot.State != "completed" || snapshot.UncertainEffects {
		t.Fatal("live failure replay invalid")
	}
	failed := 0
	for _, message := range snapshot.Messages {
		if message.ToolFailed {
			failed++
		}
	}
	if failed != 1 {
		t.Fatal("live replay lost failed step")
	}
	t.Log("durable failure before RPC, final completion and replayed failure verified")
}

type failureLiveObservation struct {
	falseResponses, completions int
	status                      string
	hasSuccess, success         bool
}

type failureLiveWire struct {
	Wire
	observation *failureLiveObservation
	ctx         context.Context
	store       *telemetry.Store
	task        string
}

func (w *failureLiveWire) Write(e codexrpc.Envelope) error {
	var response struct {
		Success      *bool           `json:"success"`
		ContentItems json.RawMessage `json:"contentItems"`
	}
	if len(e.Result) != 0 && json.Unmarshal(e.Result, &response) == nil && response.Success != nil && len(response.ContentItems) != 0 {
		if *response.Success {
			return errors.New("unexpected fixture success")
		}
		events, err := w.store.Read(w.ctx, w.task, 0, 100)
		if err != nil || len(events) == 0 {
			return errors.New("missing committed fixture outcome")
		}
		found := false
		for _, event := range events {
			found = found || (event.Kind == runtime.ToolCompleted && event.Data.Code == "tool_failed" && event.Data.Effect == runtime.NoEffect)
		}
		if !found || w.observation.falseResponses != 0 {
			return errors.New("invalid fixture response boundary")
		}
		w.observation.falseResponses++
	}
	return w.Wire.Write(e)
}

func (w *failureLiveWire) Read() (codexrpc.Envelope, error) {
	e, err := w.Wire.Read()
	if e.Method == "item/completed" {
		var notice struct {
			Item struct {
				Type, Status string
				Success      *bool
			}
		}
		if json.Unmarshal(e.Params, &notice) == nil && notice.Item.Type == "dynamicToolCall" {
			w.observation.completions++
			w.observation.status = "other"
			switch notice.Item.Status {
			case "failed", "completed", "inProgress":
				w.observation.status = notice.Item.Status
			}
			w.observation.hasSuccess = notice.Item.Success != nil
			if notice.Item.Success != nil {
				w.observation.success = *notice.Item.Success
			}
		}
	}
	return e, err
}
