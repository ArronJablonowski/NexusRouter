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
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/codexbridge"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// This supervised probe spends two native Sol inference calls. Its source,
// summary, and trusted validation are synthetic. The first native completion
// is held at its done callback only long enough to durably queue steering; the
// runtime must then activate the approved plan and open a second owned session.
func TestLiveCodexApprovedPlanRollover(t *testing.T) {
	if os.Getenv("DARWIN_CODEX_LIVE_ROLLOVER") != "1" {
		t.Skip("explicit supervised signed-in Sol context rollover only")
	}
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("CLI unavailable")
	}
	bin, err = filepath.Abs(bin)
	if err != nil {
		t.Fatal("CLI path unavailable")
	}

	const marker = "SILVER-CEDAR-8246"
	const original = "Synthetic rollover source: preserve marker " + marker + ". Remove this source-only sentence after activation. "
	var syntheticCalls atomic.Int32
	loopback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch syntheticCalls.Add(1) {
		case 1:
			fmt.Fprintln(w, `{"message":{"content":"synthetic source accepted"},"done":true,"done_reason":"stop"}`)
		case 2:
			text, _ := json.Marshal(`{"version":1,"summary":{"requirements":["Preserve rollover marker ` + marker + `."]}}`)
			fmt.Fprintf(w, "{\"message\":{\"content\":%s},\"done\":true,\"done_reason\":\"stop\"}\n", text)
		default:
			t.Error("unexpected synthetic inference")
		}
	}))
	defer loopback.Close()

	cfg := codexTaskConfig(t)
	cfg.Providers[0].Executable = bin
	zero := 0.0
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "rollover-fixture", Kind: "ollama", Endpoint: loopback.URL})
	cfg.Models = append(cfg.Models, config.Model{ID: "rollover-fixture", Provider: "rollover-fixture", Model: "fixture", Locality: "cloud", ContextTokens: 16384, EstimatedCost: &zero, Capabilities: []string{"chat"}})
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal("rollover fixture configuration invalid")
	}
	svc.profile = healthProfile
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	source, err := svc.Run(ctx, Request{ModelID: "rollover-fixture", Prompt: original + strings.Repeat("history ", 300)})
	if err != nil {
		t.Fatal("synthetic rollover source failed")
	}
	operation, err := svc.PrepareSummary(ctx, "live-codex-rollover-summary-0001", PrepareSummaryRequest{
		Version: 1, TaskID: source.TaskID, ModelID: "rollover-fixture", Keep: 1, MaxCost: 0,
	})
	if err != nil || operation.TerminalAttempt == nil || operation.TerminalAttempt.Draft == nil || syntheticCalls.Load() != 2 {
		t.Fatal("synthetic rollover summary failed")
	}
	attempt := *operation.TerminalAttempt
	registry, err := sessions.NewSummaryValidatorRegistry(map[string]sessions.SummaryValidator{
		"live-rollover-v1": sessions.SummaryValidatorFunc(func(context.Context, sessions.SummaryValidationInput) (sessions.SummaryValidationDecision, error) {
			return sessions.SummaryValidationDecision{Decision: "approved", Note: "Synthetic validator matched the exact rollover marker against the source."}, nil
		}),
	})
	if err != nil {
		t.Fatal("validator registry unavailable")
	}
	review, err := svc.ValidateSummary(ctx, attempt.ID, "", "live-codex-rollover-validation-0001", "live-rollover-v1", registry)
	if err != nil || review.Version != 2 || review.Decision != "approved" {
		t.Fatal("trusted rollover validation failed")
	}

	request := Request{ModelID: "brain", ContinueTaskID: source.TaskID, Prompt: "Reply with exactly FIRST-NATIVE-TURN-DONE. Do not use tools."}
	read, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal("source inspection unavailable")
	}
	before, err := read.Read(ctx, source.TaskID, 0, 100)
	if err != nil {
		read.Close()
		t.Fatal("source journal unavailable")
	}
	_, initial, err := svc.prepareExplicitInference(ctx, read, request, svc.settings.Models[0], nil)
	read.Close()
	if err != nil {
		t.Fatal("initial rollover request unavailable")
	}
	limit, err := providers.EstimateContext(initial)
	if err != nil {
		t.Fatal("initial rollover estimate unavailable")
	}
	svc.settings.Runtime.AutoApprovedCompaction = true
	svc.settings.Models[0].ContextTokens = limit

	firstDone := make(chan struct{})
	releaseFirst := make(chan struct{})
	observed := &rolloverLiveObservedProvider{marker: marker, original: original, firstDone: firstDone, releaseFirst: releaseFirst}
	var launches atomic.Int32
	var directories []string
	svc.codexLauncher = func(callCtx context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
		generation := int(launches.Add(1))
		if generation > 2 || spec.Model != "gpt-5.6-sol" || spec.Privacy != "cloud_allowed" {
			return nil, errors.New("unexpected rollover launch metadata")
		}
		directories = append(directories, spec.CWD)
		provider, launchErr := codexbridge.LaunchChecked(callCtx, spec)
		if launchErr != nil {
			return nil, launchErr
		}
		return observed.generation(provider, generation), nil
	}

	type completion struct {
		result Result
		err    error
	}
	started := make(chan string, 1)
	finished := make(chan completion, 1)
	go func() {
		result, runErr := svc.RunStream(ctx, request, func(event runtime.Event) error {
			if event.Kind == runtime.TaskStarted {
				started <- event.TaskID
			}
			return nil
		})
		finished <- completion{result: result, err: runErr}
	}()

	var taskID string
	select {
	case taskID = <-started:
	case <-ctx.Done():
		t.Fatal("rollover task did not durably start")
	}
	select {
	case <-firstDone:
	case <-ctx.Done():
		t.Fatal("first native turn did not complete")
	}
	guidanceText := "The first native turn is complete. Reply with exactly FINAL-ROLLOVER-OK. Do not use tools. " + strings.Repeat("rollover-pressure ", 64)
	guidance, err := svc.SteerTask(ctx, taskID, "live-codex-rollover-steering", guidanceText)
	if err != nil || guidance.State != "pending" {
		close(releaseFirst)
		t.Fatal("rollover steering was not durably queued", guidance, err)
	}
	close(releaseFirst)

	var completed completion
	select {
	case completed = <-finished:
	case <-ctx.Done():
		t.Fatal("rolled-over task did not finish")
	}
	t.Logf("live rollover metadata: launches=%d streams=%d done=%d tool_calls=%d", launches.Load(), observed.streams.Load(), observed.done.Load(), observed.toolCalls.Load())
	if completed.err != nil {
		t.Fatal("live Sol rollover failed; payload withheld")
	}
	if launches.Load() != 2 || observed.streams.Load() != 2 || observed.done.Load() != 2 || observed.toolCalls.Load() != 0 ||
		!strings.Contains(completed.result.Text, "FINAL-ROLLOVER-OK") {
		t.Fatal("live rollover protocol or final completion failed; payload withheld")
	}
	for _, directory := range directories {
		if directory == "" {
			t.Fatal("owned rollover directory missing")
		}
		if _, statErr := os.Stat(directory); !os.IsNotExist(statErr) {
			t.Fatal("owned rollover directory retained")
		}
	}

	read, err = telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal("rollover outcome unavailable")
	}
	defer read.Close()
	state, err := read.ContextCompactionPlanForAttempt(ctx, attempt.ID)
	if err != nil || state.Status != sessions.ContextCompactionActivated || state.Plan == nil || len(state.Facts) == 0 ||
		state.Facts[len(state.Facts)-1].Activation == nil || state.Facts[len(state.Facts)-1].Activation.TaskID != taskID {
		t.Fatal("durable rollover activation missing")
	}
	snapshot, err := sessions.Replay(ctx, read, taskID)
	if err != nil || snapshot.State != "completed" || snapshot.Compaction == nil || snapshot.Compaction.SummaryAttemptID != attempt.ID || snapshot.Compaction.SummaryReviewID != review.ID {
		t.Fatal("rolled-over task did not replay as completed")
	}
	receipt, err := read.SteeringStatus(ctx, taskID, guidance.ID)
	if err != nil || receipt.State != "applied" || receipt.AppliedSequence == nil {
		t.Fatal("rollover steering was not durably applied")
	}
	after, err := read.Read(ctx, source.TaskID, 0, 100)
	if err != nil || string(mustJSONLiveRollover(before)) != string(mustJSONLiveRollover(after)) || syntheticCalls.Load() != 2 {
		t.Fatal("rollover changed or redispatched the synthetic source")
	}
	t.Log("live rollover result: first_completed=true activation_durable=true second_session_completed=true tools_redispatched=false")
}

type rolloverLiveObservedProvider struct {
	marker       string
	original     string
	firstDone    chan struct{}
	releaseFirst chan struct{}
	streams      atomic.Int32
	done         atomic.Int32
	toolCalls    atomic.Int32
}

func (p *rolloverLiveObservedProvider) generation(provider taskProvider, generation int) taskProvider {
	return &rolloverLiveGeneration{taskProvider: provider, observed: p, generation: generation}
}

type rolloverLiveGeneration struct {
	taskProvider
	observed   *rolloverLiveObservedProvider
	generation int
}

func (p *rolloverLiveGeneration) CheckContextRollover(ctx context.Context, current, prospective providers.Request) error {
	inspector, ok := p.taskProvider.(interface {
		CheckContextRollover(context.Context, providers.Request, providers.Request) error
	})
	if !ok {
		return fmt.Errorf("live rollover provider does not expose inspection")
	}
	return inspector.CheckContextRollover(ctx, current, prospective)
}

func (p *rolloverLiveGeneration) Stream(ctx context.Context, request providers.Request, emit func(providers.Chunk) error) error {
	p.observed.streams.Add(1)
	encoded, _ := json.Marshal(request.Messages)
	if p.generation == 1 {
		if !strings.Contains(string(encoded), p.observed.original) {
			return errors.New("first native session did not receive complete context")
		}
	} else if p.generation == 2 {
		if strings.Contains(string(encoded), p.observed.original) || !strings.Contains(string(encoded), p.observed.marker) || !strings.Contains(string(encoded), "FINAL-ROLLOVER-OK") {
			return errors.New("second native session did not receive compacted context and steering")
		}
	}
	return p.taskProvider.Stream(ctx, request, func(chunk providers.Chunk) error {
		if chunk.ToolCall != nil {
			p.observed.toolCalls.Add(1)
		}
		if chunk.Done {
			p.observed.done.Add(1)
			if p.generation == 1 {
				close(p.observed.firstDone)
				select {
				case <-p.observed.releaseFirst:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		return emit(chunk)
	})
}

func mustJSONLiveRollover(value any) []byte {
	body, _ := json.Marshal(value)
	return body
}
