package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/codexbridge"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/resources"
)

// Explicitly supervised live Sol review of synthetic, non-sensitive output.
// The candidate comes from a loopback fixture, not real local-model inference.
func TestLiveCodexTaskAudit(t *testing.T) {
	if os.Getenv("DARWIN_CODEX_LIVE_AUDIT") != "1" {
		t.Skip("explicit supervised signed-in Sol audit only")
	}
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("CLI unavailable")
	}
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"message":{"content":"I will implement the requested Go function in a future response."},"done":true,"done_reason":"stop"}`)
	}))
	defer local.Close()
	cfg := codexTaskConfig(t)
	cfg.Providers[0].Executable = bin
	cost, zero := 0.1, 0.0
	cfg.Models[0].EstimatedCost = &cost
	cfg.Evaluation.Judge = true
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "local", Kind: "ollama", Endpoint: local.URL})
	// Declare the fixture's task cloud-eligible so the explicit audit may send
	// its synthetic content to Sol. This is not local-only privacy evidence.
	cfg.Models = append(cfg.Models, config.Model{ID: "candidate", Provider: "local", Model: "fixture-local", Locality: "cloud", Capabilities: []string{"chat"}, RAMBytes: 1, ContextTokens: 4096, EstimatedCost: &zero})
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal("fixture configuration invalid")
	}
	svc.profile = func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now(), CPUs: 4, TotalRAM: 8 << 30, AvailableRAM: 7 << 30}, nil
	}
	observed := &auditLiveObservedProvider{}
	svc.codexLauncher = func(ctx context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
		p, err := codexbridge.LaunchChecked(ctx, spec)
		if err != nil {
			return nil, err
		}
		observed.taskProvider = p
		return observed, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	source, err := svc.Run(ctx, Request{ModelID: "candidate", Prompt: "Return a complete Go function named Answer that returns the integer 42. Provide the code now, not a promise to write it later.", Domain: "code"})
	if err != nil {
		t.Fatal("synthetic source execution failed")
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal("source inspection unavailable")
	}
	defer db.Close()
	before, err := db.Read(ctx, source.TaskID, 0, 100)
	if err != nil {
		t.Fatal("source inspection failed")
	}
	record, err := svc.AuditTask(ctx, source.TaskID, "brain", cost)
	t.Logf("live audit protocol metadata: streams=%d done=%t bytes=%d failure=%s", observed.streams, observed.done, observed.bytes, observed.failure)
	if err != nil {
		var shape map[string]json.RawMessage
		valid := json.Unmarshal([]byte(observed.output.String()), &shape) == nil
		matches := func(key, expected string) bool {
			var value string
			return json.Unmarshal(shape[key], &value) == nil && value == expected
		}
		t.Logf("audit output shape: json=%t fenced=%t fields=%d evaluator=%t rubric=%t domain=%t", valid, strings.HasPrefix(strings.TrimSpace(observed.output.String()), "```"), len(shape), matches("evaluator_id", "brain"), matches("rubric_version", "darwin-review-v2"), matches("domain", "code"))
		t.Fatal("live Sol audit failed; payload withheld")
	}
	if record.EvaluatorModel != "gpt-5.6-sol" || record.Audit.Verdict != "reject" || len(record.Audit.Findings) == 0 {
		t.Fatal("auditor did not flag the missing deliverable; payload withheld")
	}
	saved, err := db.Audit(ctx, record.ID)
	if err != nil || !reflect.DeepEqual(saved.Audit, record.Audit) {
		t.Fatal("validated advisory audit not durable")
	}
	after, err := db.Read(ctx, source.TaskID, 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("audit modified source journal")
	}
	t.Logf("live audit metadata: verdict=%s findings=%d source_events=%d source_unchanged=true", record.Audit.Verdict, len(record.Audit.Findings), len(after))
}

type auditLiveObservedProvider struct {
	taskProvider
	streams, bytes int
	done           bool
	failure        string
	output         strings.Builder
}

func (p *auditLiveObservedProvider) Stream(ctx context.Context, request providers.Request, emit func(providers.Chunk) error) error {
	p.streams++
	err := p.taskProvider.Stream(ctx, request, func(chunk providers.Chunk) error {
		p.bytes += len(chunk.Text)
		p.output.WriteString(chunk.Text)
		p.done = p.done || chunk.Done
		return emit(chunk)
	})
	var failure *providers.Failure
	if errors.As(err, &failure) && failure.Code == "codex_protocol" {
		p.failure = "codex_protocol"
	} else if err != nil {
		p.failure = "stream_failed"
	}
	return err
}
