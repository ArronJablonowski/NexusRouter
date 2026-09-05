package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/codexbridge"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

const codexRepairInvalidGo = "package answer\nfunc Answer( {"
const codexRepairValidGo = "package answer\nfunc Answer() int { return 42 }\n"

func codexRepairConfig(t *testing.T, executable, endpoint string) config.Settings {
	t.Helper()
	cfg := codexTaskConfig(t)
	cfg.Providers[0].Executable = executable
	cfg.Runtime.MaxTurns, cfg.Tools.MaxTurns = 4, 4
	cfg.Workers.Max, cfg.Workers.DelegateMaxCalls = 2, 2
	cfg.Workers.DelegateMaxTurns = 1
	cfg.Workers.DelegateModel = "worker"
	zero := 0.0
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "local", Kind: "ollama", Endpoint: endpoint})
	cfg.Models = append(cfg.Models, config.Model{ID: "worker", Provider: "local", Model: "controlled-repair-worker", Locality: "local", RAMBytes: 1, ContextTokens: 4096, EstimatedCost: &zero, Capabilities: []string{"chat"}})
	return cfg
}

// This test makes signed-in Sol inference only after explicit operator opt-in.
// The worker is a controlled loopback fixture, not installed Ollama inference.
// It qualifies rejection-driven delegation repair, not Go semantic execution.
func TestLiveCodexDelegationRepair(t *testing.T) {
	if os.Getenv("DARWIN_CODEX_LIVE_REPAIR") != "1" {
		t.Skip("explicit supervised signed-in Sol repair only")
	}
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("Codex unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var workerCalls atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "" {
			t.Error("unexpected controlled-worker transport")
			http.Error(w, "fixture unavailable", http.StatusBadRequest)
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 128<<10))
		n := workerCalls.Add(1)
		content := codexRepairInvalidGo
		if n == 2 {
			content = codexRepairValidGo
		}
		if n > 2 {
			t.Error("worker exceeded exact repair budget")
			http.Error(w, "fixture unavailable", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": content}, "done": true, "done_reason": "stop"})
	}))
	defer func() { cancel(); local.Close() }()
	cfg := codexRepairConfig(t, bin, local.URL)
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal("repair fixture configuration invalid")
	}
	svc.profile = func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now(), CPUs: 4, TotalRAM: 8 << 30, AvailableRAM: 7 << 30}, nil
	}
	var coordinatorDirectory string
	launches := 0
	svc.codexLauncher = func(ctx context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
		launches++
		coordinatorDirectory = spec.CWD
		if spec.Model != "gpt-5.6-sol" || spec.Privacy != "cloud_allowed" {
			return nil, ErrAdmission
		}
		return codexbridge.LaunchChecked(ctx, spec)
	}
	result, runErr := svc.Run(ctx, Request{ModelID: "brain", Domain: "code", Prompt: "Supervised delegation repair qualification using synthetic data. Use only darwin.delegate; no files, shell, web, or batch tools. First ask the worker for raw Go source containing package answer and func Answer() int returning 42, with validation set to go_source. Wait for that result. If its Go validation is rejected, use that failure feedback to ask the worker once more for corrected complete raw Go source, again with validation go_source. These must be two sequential delegate calls, never parallel. After the corrected result passes, give one concise final sentence confirming the function returns 42. Do not substitute your own code for a rejected worker result; complete both stages."})
	t.Logf("repair run metadata: coordinator_launches=%d worker_calls=%d parent_turns=%d completed=%t", launches, workerCalls.Load(), result.Turns, runErr == nil)
	if runErr != nil || result.TaskID == "" || workerCalls.Load() != 2 || launches != 1 || result.Turns < 3 || result.Turns > 4 || !strings.Contains(result.Text, "42") {
		t.Fatal("live repair did not complete exact bounded sequence; payload withheld")
	}
	if _, err := os.Stat(coordinatorDirectory); !os.IsNotExist(err) {
		t.Fatal("coordinator directory retained")
	}
	query, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	db, err := telemetry.OpenReadOnly(query, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal("repair journal unavailable")
	}
	defer db.Close()
	events, err := db.Read(query, result.TaskID, 0, 100)
	if err != nil {
		t.Fatal("parent journal unreadable")
	}
	var completions []runtime.Event
	for _, event := range events {
		if event.Kind == runtime.ToolStarted && event.Data.ToolName != "delegate" {
			t.Fatal("unexpected parent tool")
		}
		if event.Kind == runtime.ToolCompleted {
			completions = append(completions, event)
		}
	}
	if len(completions) != 2 || completions[0].Data.Code != "tool_failed" || completions[0].Data.Effect != runtime.NoEffect || completions[1].Data.Code != "" || completions[1].Data.Effect != runtime.NoEffect {
		t.Fatal("parent did not retain failure followed by accepted repair")
	}
	var rejected delegateFailure
	if json.Unmarshal([]byte(completions[0].Data.Text), &rejected) != nil || rejected.Reason != "invalid_output" || rejected.ExecutionID == "" || rejected.WorkID == "" {
		t.Fatal("rejection lacks attributed validation evidence")
	}
	var accepted struct {
		WorkID      string `json:"work_task_id"`
		ExecutionID string `json:"execution_task_id"`
	}
	if json.Unmarshal([]byte(completions[1].Data.Text), &accepted) != nil || accepted.WorkID == "" || accepted.ExecutionID == "" || accepted.WorkID == rejected.WorkID || accepted.ExecutionID == rejected.ExecutionID {
		t.Fatal("repair did not create a distinct bounded work attempt")
	}
	for _, check := range []struct{ task, parent, state string }{{result.TaskID, "", "completed"}, {rejected.WorkID, result.TaskID, "failed"}, {rejected.ExecutionID, rejected.WorkID, "failed"}, {accepted.WorkID, result.TaskID, "completed"}, {accepted.ExecutionID, accepted.WorkID, "completed"}} {
		snapshot, err := db.TaskSnapshot(query, check.task)
		if err != nil || snapshot.State != check.state || snapshot.ParentTaskID != check.parent || snapshot.UncertainEffects || len(snapshot.Pending) != 0 {
			t.Fatal("repair task-tree state mismatch; payload withheld")
		}
	}
	bad, err := db.Read(query, rejected.ExecutionID, 0, 100)
	if err != nil || len(bad) == 0 || bad[0].Data.Validation != "go_source" || bad[len(bad)-1].Kind != runtime.TaskFailed || bad[len(bad)-1].Data.Code != "invalid_output" {
		t.Fatal("first worker validation failure not durable")
	}
	good, err := db.Read(query, accepted.ExecutionID, 0, 100)
	if err != nil || len(good) == 0 || good[0].Data.Validation != "go_source" || good[len(good)-1].Kind != runtime.TaskCompleted {
		t.Fatal("corrected worker validation not durable")
	}
	corrected, err := db.TaskSnapshot(query, accepted.ExecutionID)
	if err != nil || len(corrected.Messages) != 2 || corrected.Messages[1].Role != "assistant" || corrected.Messages[1].Content != codexRepairValidGo {
		t.Fatal("saved corrected worker output differs from controlled fixture")
	}
	work, err := db.Read(query, accepted.WorkID, 0, 100)
	if err != nil {
		t.Fatal("accepted worker evidence unavailable")
	}
	validated := false
	for _, event := range work {
		validated = validated || (event.Kind == runtime.EvaluationRecorded && event.Data.Accepted != nil && *event.Data.Accepted)
	}
	if !validated {
		t.Fatal("supervisor acceptance evidence missing")
	}
	leases, err := db.InspectLeases(query, "delegation-"+result.TaskID)
	if err != nil || len(leases) != 0 {
		t.Fatal("worker leases retained after repair")
	}
	t.Logf("repair evidence: parent_events=%d rejected_child_events=%d accepted_child_events=%d tool_failures=1 tool_successes=1 uncertainty=false active_worker_leases=0", len(events), len(bad), len(good))
}

// Offline fixture qualification never launches Codex or makes model requests.
func TestCodexRepairFixtureConfiguration(t *testing.T) {
	cfg := codexRepairConfig(t, "/fixture/codex", "http://127.0.0.1:12345")
	if cfg.Validate() != nil || evaluation.GoSourceValid(codexRepairInvalidGo) || !evaluation.GoSourceValid(codexRepairValidGo) || cfg.Workers.DelegateMaxCalls != 2 || cfg.Runtime.MaxTurns != 4 {
		t.Fatal("invalid controlled repair fixture")
	}
}
