package v1_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/resources"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"go.yaml.in/yaml/v3"
)

type sdkEvaluatorEngine struct {
	descriptor sdk.EvaluatorDescriptor
	response   sdk.EvaluatorResponse
	call       func(context.Context, sdk.EvaluatorRequest) (sdk.EvaluatorResponse, error)
	calls      atomic.Int32
}

func (e *sdkEvaluatorEngine) Descriptor() sdk.EvaluatorDescriptor { return e.descriptor }
func (e *sdkEvaluatorEngine) Evaluate(ctx context.Context, request sdk.EvaluatorRequest) (sdk.EvaluatorResponse, error) {
	e.calls.Add(1)
	if e.call != nil {
		return e.call(ctx, request)
	}
	return e.response, nil
}

type sdkEvaluatorHarness struct {
	client, reopened *sdk.Client
	builds, streams  atomic.Int32
}

func newSDKEvaluatorHarness(t *testing.T, evaluator sdk.Evaluator) *sdkEvaluatorHarness {
	t.Helper()
	h := &sdkEvaluatorHarness{}
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Hardware.AutoProfile = false
	cfg.Hardware.Concurrent = "1"
	cfg.Evaluation.Judge = true
	cfg.Evaluation.AutoReviewModel = ""
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "evaluator.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:1"}}
	zero := 0.0
	cfg.Models = []config.Model{
		{ID: "candidate", Provider: "local", Model: "candidate-model", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero},
		{ID: "reviewer", Provider: "local", Model: "reviewer-model", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero},
	}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	factory := sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		h.builds.Add(1)
		return sdkProviderStream(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
			h.streams.Add(1)
			if request.Model != "candidate-model" {
				t.Errorf("reviewer provider constructed for injected evaluator: %q", request.Model)
			}
			return emit(providers.Chunk{Text: "candidate answer", Done: true, FinishReason: "stop"})
		}), nil
	})
	options := sdk.ConfigOptions{
		ProjectFile: path, ProviderFactory: factory, Evaluator: evaluator,
		ResourceProfiler: sdkFixtureProfiler(func(context.Context) (resources.Measurement, error) { return sdkGoodMeasurement(), nil }),
	}
	h.client, err = sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	h.reopened, err = sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func validSDKEvaluator() *sdkEvaluatorEngine {
	descriptor := sdk.EvaluatorDescriptor{Version: 1, ID: "reviewer", Revision: "extension-v1", RubricVersion: "extension-rubric-v1"}
	return &sdkEvaluatorEngine{descriptor: descriptor, response: sdk.EvaluatorResponse{Version: 1, Audit: sdk.Audit{Version: 1, EvaluatorID: descriptor.ID, RubricVersion: descriptor.RubricVersion, Domain: "code", Verdict: "reject", Confidence: .8, Findings: []sdk.AuditFinding{{Summary: "The response lacks independently verified evidence.", EvidenceRefs: []string{"candidate_execution"}}}}}}
}

func runEvaluatorCandidate(t *testing.T, h *sdkEvaluatorHarness) sdk.Result {
	t.Helper()
	result, err := h.client.Run(context.Background(), sdk.Request{Version: 1, ModelID: "candidate", Prompt: "produce an answer", Domain: "code"})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSDKEvaluatorExactlyOnceReplayAndNoReviewerProvider(t *testing.T) {
	evaluator := validSDKEvaluator()
	var sawEvidence atomic.Bool
	evaluator.call = func(_ context.Context, request sdk.EvaluatorRequest) (sdk.EvaluatorResponse, error) {
		if request.Validate() != nil || request.Version != 1 || request.Domain != "code" || request.Candidate != "candidate answer" || request.Requirements == "" {
			t.Errorf("invalid evaluator request: %+v", request)
		}
		for _, evidence := range request.Evidence {
			if evidence.ID == "candidate_execution" {
				sawEvidence.Store(true)
			}
		}
		return evaluator.response, nil
	}
	h := newSDKEvaluatorHarness(t, evaluator)
	candidate := runEvaluatorCandidate(t, h)
	request := sdk.AuditRequest{Version: 1, IdempotencyKey: "sdk-evaluator-replay-key", TaskID: candidate.TaskID, ReviewerModelID: "reviewer"}
	status, err := h.client.RunAudit(context.Background(), request, func(sdk.AuditEvent) error { return nil })
	if err != nil || status.Status != "rejected" || status.EvaluatorModel != "extension-v1" || status.EvaluatorProvider != "evaluator_extension" || !reflect.DeepEqual(status.EvidencePrecedence, evaluation.AuditEvidencePrecedence()) || evaluator.calls.Load() != 1 || !sawEvidence.Load() || h.builds.Load() != 1 || h.streams.Load() != 1 {
		t.Fatal(status, err, evaluator.calls.Load(), sawEvidence.Load(), h.builds.Load(), h.streams.Load())
	}
	// Mutating extension-owned output after return cannot alter durable state.
	evaluator.response.Audit.Findings[0].Summary = "mutated"
	evaluator.response.Audit.Findings[0].EvidenceRefs[0] = "invented"
	replayed, err := h.reopened.RunAudit(context.Background(), request, func(sdk.AuditEvent) error { return nil })
	if err != nil || replayed.ID != status.ID || replayed.AuditID != status.AuditID || evaluator.calls.Load() != 1 || h.builds.Load() != 1 || len(replayed.Findings) != 1 || replayed.Findings[0].Summary == "mutated" || replayed.Findings[0].EvidenceRefs[0] == "invented" {
		t.Fatal(replayed, err, evaluator.calls.Load(), h.builds.Load())
	}
}

func TestSDKEvaluatorTypedNilRejected(t *testing.T) {
	var evaluator *sdkEvaluatorEngine
	client, err := sdk.New(sdk.ConfigOptions{Overrides: map[string]string{"telemetry.database": filepath.Join(t.TempDir(), "unused.db")}, Evaluator: evaluator})
	if !errors.Is(err, sdk.ErrAdmission) || client != nil {
		t.Fatal("typed nil evaluator admitted", client, err)
	}
}

func TestSDKEvaluatorFailuresAreBoundedAndDurable(t *testing.T) {
	for _, mode := range []string{"error", "panic", "malformed", "mismatch", "invented-reference", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			evaluator := validSDKEvaluator()
			evaluator.call = func(context.Context, sdk.EvaluatorRequest) (sdk.EvaluatorResponse, error) {
				switch mode {
				case "error":
					return sdk.EvaluatorResponse{}, errors.New("private evaluator failure")
				case "panic":
					panic("private evaluator panic")
				case "malformed":
					bad := evaluator.response
					bad.Version = 2
					return bad, nil
				case "mismatch":
					bad := evaluator.response
					bad.Audit.EvaluatorID = "other"
					return bad, nil
				case "invented-reference":
					bad := evaluator.response
					bad.Audit.Findings[0].EvidenceRefs = []string{"invented"}
					return bad, nil
				case "oversize":
					bad := evaluator.response
					bad.Audit.Findings[0].Summary = strings.Repeat("x", 1<<20)
					return bad, nil
				}
				return evaluator.response, nil
			}
			h := newSDKEvaluatorHarness(t, evaluator)
			candidate := runEvaluatorCandidate(t, h)
			request := sdk.AuditRequest{Version: 1, IdempotencyKey: "sdk-evaluator-failure-" + mode, TaskID: candidate.TaskID, ReviewerModelID: "reviewer"}
			status, err := h.client.RunAudit(context.Background(), request, func(sdk.AuditEvent) error { return nil })
			if err == nil || strings.Contains(err.Error(), "private evaluator") || status.Status != "failed" || status.ErrorCode != "review_failed" || evaluator.calls.Load() != 1 || h.builds.Load() != 1 || h.streams.Load() != 1 {
				t.Fatal(status, err, evaluator.calls.Load(), h.builds.Load(), h.streams.Load())
			}
			inspected, inspectErr := h.reopened.InspectAudit(context.Background(), candidate.TaskID, status.ID)
			if inspectErr != nil || inspected.Status != "failed" || inspected.ErrorCode != "review_failed" {
				t.Fatal("failed evaluation was not durable", inspected, inspectErr)
			}
		})
	}
}

func TestSDKEvaluatorCancellation(t *testing.T) {
	evaluator := validSDKEvaluator()
	entered := make(chan struct{})
	evaluator.call = func(ctx context.Context, _ sdk.EvaluatorRequest) (sdk.EvaluatorResponse, error) {
		close(entered)
		<-ctx.Done()
		return sdk.EvaluatorResponse{}, ctx.Err()
	}
	h := newSDKEvaluatorHarness(t, evaluator)
	candidate := runEvaluatorCandidate(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	var status sdk.AuditStatus
	var runErr error
	go func() {
		defer close(finished)
		status, runErr = h.client.RunAudit(ctx, sdk.AuditRequest{Version: 1, IdempotencyKey: "sdk-evaluator-cancel-key", TaskID: candidate.TaskID, ReviewerModelID: "reviewer"}, func(sdk.AuditEvent) error { return nil })
	}()
	select {
	case <-entered:
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("evaluator was not invoked")
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled evaluator did not return")
	}
	if !errors.Is(runErr, context.Canceled) || status.Status != "canceled" || status.ErrorCode != "canceled" || evaluator.calls.Load() != 1 || h.builds.Load() != 1 {
		t.Fatal(status, runErr, evaluator.calls.Load(), h.builds.Load())
	}
}
