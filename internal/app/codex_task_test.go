package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/codexbridge"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/resources"
)

type codexTaskFixture struct {
	calls, closed int
	fail          bool
	result        string
}

func (p *codexTaskFixture) Close() error { p.closed++; return nil }
func (p *codexTaskFixture) Models(context.Context) ([]string, error) {
	return []string{"gpt-5.6-sol"}, nil
}
func (p *codexTaskFixture) Stream(ctx context.Context, r providers.Request, emit func(providers.Chunk) error) error {
	p.calls++
	if p.fail {
		return errors.New("fixture failed")
	}
	if p.calls == 1 {
		if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "local-call", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"Write a Go function returning 42","validation":"go_source"}`)}}); err != nil {
			return err
		}
		return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
	}
	p.result = r.Messages[len(r.Messages)-1].Content
	return emit(providers.Chunk{Text: "Reviewed local result.", Done: true, FinishReason: "stop"})
}

func codexTaskConfig(t *testing.T) config.Settings {
	s := config.Defaults()
	s.Mode = "hybrid"
	s.Telemetry.Database = filepath.Join(t.TempDir(), "tasks.db")
	s.Memory.Enabled = false
	s.Skills.Enabled = false
	s.Tools.Enabled = false
	zero := 0.0
	s.Providers = []config.Provider{{ID: "codex", Kind: "codex_app_server", Executable: "/fixture/codex"}}
	s.Models = []config.Model{{ID: "brain", Provider: "codex", Model: "gpt-5.6-sol", Locality: "cloud", ContextTokens: 16384, EstimatedCost: &zero, Capabilities: []string{"chat"}}}
	return s
}

func TestCodexTaskUsesDurableDelegationAndCloses(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			var localCalls atomic.Int32
			local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				localCalls.Add(1)
				if r.Header.Get("Authorization") != "" {
					t.Error("credential reached local worker")
				}
				fmt.Fprint(w, `{"message":{"content":"package answer\nfunc Answer() int { return 42 }"},"done":true,"done_reason":"stop"}`+"\n")
			}))
			defer local.Close()
			s := codexTaskConfig(t)
			s.Workers.Max = 2
			s.Workers.DelegateModel = "worker"
			s.Workers.DelegateMaxCalls = 1
			s.Providers = append(s.Providers, config.Provider{ID: "local", Kind: "ollama", Endpoint: local.URL})
			zero := 0.0
			s.Models = append(s.Models, config.Model{ID: "worker", Provider: "local", Model: "fixture-local", Locality: "local", RAMBytes: 1, ContextTokens: 4096, EstimatedCost: &zero, Capabilities: []string{"chat"}})
			svc, err := NewService(s, nil)
			if err != nil {
				t.Fatal(err)
			}
			svc.profile = func(context.Context) (resources.Snapshot, error) {
				return resources.Snapshot{Time: time.Now(), CPUs: 4, TotalRAM: 8 << 30, AvailableRAM: 7 << 30}, nil
			}
			p := &codexTaskFixture{fail: fail}
			dir := ""
			svc.codexLauncher = func(ctx context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
				dir = spec.CWD
				if spec.Privacy != "cloud_allowed" || spec.Model != "gpt-5.6-sol" {
					t.Fatal("bad launcher admission")
				}
				return p, nil
			}
			result, err := svc.Run(context.Background(), Request{ModelID: "brain", Prompt: "Delegate then review", Domain: "code"})
			if p.closed != 1 {
				t.Fatal("task did not close coordinator")
			}
			if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
				t.Fatal("private task directory retained")
			}
			if fail {
				if err == nil || localCalls.Load() != 0 {
					t.Fatal("failed coordinator delegated")
				}
				return
			}
			if err != nil || result.Text != "Reviewed local result." || p.calls != 2 || localCalls.Load() != 1 {
				t.Fatal(result, err, p.calls, localCalls.Load())
			}
			var child struct {
				Work      string `json:"work_task_id"`
				Execution string `json:"execution_task_id"`
				Output    string `json:"untrusted_output"`
			}
			if json.Unmarshal([]byte(p.result), &child) != nil || child.Output == "" {
				t.Fatal("missing child evidence")
			}
			db, err := telemetry.OpenReadOnly(context.Background(), s.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			parent, err := db.TaskSnapshot(context.Background(), result.TaskID)
			if err != nil || parent.State != "completed" {
				t.Fatal("parent not durable")
			}
			execution, err := db.TaskSnapshot(context.Background(), child.Execution)
			if err != nil || execution.ParentTaskID != child.Work || execution.Privacy != "local_only" || execution.State != "completed" {
				t.Fatal("child not durable/local")
			}
		})
	}
}

func TestCodexTaskRejectsPrivacyAndUnsupportedHistoryBeforeLaunch(t *testing.T) {
	for _, which := range []string{"local_mode", "local_request", "history", "system_context", "cancel"} {
		t.Run(which, func(t *testing.T) {
			s := codexTaskConfig(t)
			r := Request{ModelID: "brain", Prompt: "private"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch which {
			case "local_mode":
				s.Mode = "local_only"
			case "local_request":
				r.LocalRequired = true
			case "history":
				r.ContinueTaskID = "missing-private-task"
			case "system_context":
				r.Prompt = ""
				r.Messages = []providers.Message{{Role: "system", Content: "private"}, {Role: "user", Content: "task"}}
			case "cancel":
				cancel()
			}
			svc, err := NewService(s, nil)
			if err != nil {
				t.Fatal(err)
			}
			svc.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) {
				t.Fatal("denial launched Codex")
				return nil, nil
			}
			if _, err := svc.Run(ctx, r); err == nil {
				t.Fatal("unsupported request admitted")
			}
		})
	}
}
