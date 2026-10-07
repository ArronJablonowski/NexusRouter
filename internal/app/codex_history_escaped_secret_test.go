package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/codexbridge"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/resources"
)

// A once-nonsensitive local result becomes a configured secret later. Its raw
// bytes do not occur literally in the saved JSON tool envelope: quote, slash
// and newline escaping must be decoded before scrubbing imported history.
func TestCodexContinuationRedactsEscapedRotatedToolOutput(t *testing.T) {
	ctx := context.Background()
	const secret = "rotated-quote\"-backslash\\-newline\nprivate-token-1234567890"
	output := "package answer\n/* " + secret + " */\nfunc Answer() int { return 42 }"
	var localCalls atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localCalls.Add(1)
		if err := json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": output}, "done": true, "done_reason": "stop"}); err != nil {
			t.Error(err)
		}
	}))
	defer local.Close()
	cfg := codexTaskConfig(t)
	cfg.Workers.Max = 2
	cfg.Workers.DelegateModel = "worker"
	cfg.Workers.DelegateMaxCalls = 1
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "local", Kind: "ollama", Endpoint: local.URL})
	zero := 0.0
	cfg.Models = append(cfg.Models, config.Model{ID: "worker", Provider: "local", Model: "fixture-local", Locality: "local", RAMBytes: 1, ContextTokens: 4096, EstimatedCost: &zero, Capabilities: []string{"chat"}})
	current := ""
	svc, err := NewService(cfg, func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return current
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
	previous, err := svc.Run(ctx, Request{ModelID: "brain", Prompt: "Delegate then review", Domain: "code"})
	if err != nil || localCalls.Load() != 1 {
		t.Fatal(previous, err, localCalls.Load())
	}
	var saved struct {
		Output string `json:"untrusted_output"`
	}
	if strings.Contains(first.result, secret) || json.Unmarshal([]byte(first.result), &saved) != nil || !strings.Contains(saved.Output, secret) {
		t.Fatal("fixture did not produce escaped secret in tool JSON")
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
	current = secret
	wire := &codexHistoryWire{}
	launches := 0
	svc.codexLauncher = func(ctx context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
		launches++
		return codexbridge.NewSession(ctx, wire, codexbridge.Options{Model: spec.Model, CWD: spec.CWD})
	}
	result, err := svc.Run(ctx, Request{ModelID: "brain", ContinueTaskID: previous.TaskID, Prompt: "Review saved output", Domain: "code"})
	if err != nil || result.Text != "Reviewed saved work without rerunning it." || launches != 1 || localCalls.Load() != 1 {
		t.Fatal(result, err, launches, localCalls.Load())
	}
	outputs := 0
	for _, sent := range wire.writes {
		if sent.Method != "thread/inject_items" {
			continue
		}
		var params struct {
			Items []struct {
				Type   string `json:"type"`
				Output string `json:"output"`
			} `json:"items"`
		}
		if json.Unmarshal(sent.Params, &params) != nil {
			t.Fatal("invalid injected items")
		}
		for _, item := range params.Items {
			if item.Type == "function_call_output" {
				outputs++
				var envelope struct {
					Output string `json:"untrusted_output"`
				}
				if json.Unmarshal([]byte(item.Output), &envelope) != nil || envelope.Output == "" || strings.Contains(envelope.Output, secret) || !strings.Contains(envelope.Output, "[REDACTED]") {
					t.Fatal("decoded rotated secret escaped history redaction")
				}
			}
		}
	}
	if outputs != 1 {
		t.Fatal("expected one paired imported tool output", outputs)
	}
	after, err := db.Read(ctx, previous.TaskID, 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("redaction rewrote original source", err)
	}
}
