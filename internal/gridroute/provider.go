package gridroute

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

type executionClient interface {
	RankRecordedCandidates(context.Context, string, harness.Request, harness.Policy, []remote.DestinationCandidate, float64) (remote.DestinationSelection, error)
	DispatchRecordedAs(context.Context, *remote.RouteStore, string, string, remote.Task, string) (submissions.Status, error)
	InspectRecorded(context.Context, *remote.RouteStore, string) (remote.RecordedRequestStatus, error)
	CancelRecorded(context.Context, *remote.RouteStore, string) (submissions.Status, error)
}

func (b *Bridge) execution() executionClient {
	if b.executor != nil {
		return b.executor
	}
	return b.Client
}
func (b *Bridge) Open(ctx context.Context, c app.FederatedCandidate, taskID string, r routing.Request) (providers.Provider, error) {
	if ctx.Err() != nil || taskID == "" || c.Identity.Validate() != nil || b.Store == nil {
		return nil, remote.ErrInvalid
	}
	return &provider{bridge: b, candidate: c, key: "federated-" + taskID, request: r}, nil
}

type provider struct {
	bridge    *Bridge
	candidate app.FederatedCandidate
	key       string
	request   routing.Request
	mu        sync.Mutex
	started   bool
}

func (p *provider) Models(context.Context) ([]string, error) {
	return []string{p.candidate.Model.Model}, nil
}
func (p *provider) Stream(ctx context.Context, input providers.Request, emit func(providers.Chunk) error) (err error) {
	if ctx == nil || ctx.Err() != nil || emit == nil || input.Model != p.candidate.Model.Model || len(input.Tools) != 0 || input.MaxOutputTokens != 0 || providers.ValidateMessages(input.Messages) != nil {
		return &providers.Failure{Code: "invalid_request"}
	}
	p.mu.Lock()
	used := p.started
	p.started = true
	p.mu.Unlock()
	if used {
		return &providers.Failure{Code: "invalid_request"}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	c, r := p.candidate, p.request
	r.LocalRequired = r.LocalRequired || r.Mode == "local_only"
	request := harness.Request{Version: 1, Task: harness.TaskClass{Domain: r.Domain, Profile: r.Profile, Difficulty: "unknown"}, Mode: r.Mode, LocalRequired: r.LocalRequired, Capabilities: r.Capabilities, ContextTokens: int64(r.ContextTokens), MaxCost: r.MaxCost}
	checked, e := p.bridge.execution().RankRecordedCandidates(ctx, p.bridge.EvidenceRoot, request, harness.DefaultPolicy(), []remote.DestinationCandidate{{Destination: c.Instance, ModelID: c.DestinationModel, HarnessID: c.HarnessID, Candidate: harness.Candidate{Identity: c.Identity, Local: c.Candidate.Local, Available: true, Authorized: true, Compatible: true, CapacityAvailable: true, CredentialAvailable: true, Capabilities: c.Candidate.Capabilities, ContextTokens: int64(c.Candidate.ContextTokens), EstimatedCost: c.Candidate.EstimatedCost}}}, 0)
	if e != nil || checked.CallerFingerprint != c.CallerFingerprint || checked.Selection.Primary.Scope != c.Instance || checked.Selection.Primary.Ranked.Identity != c.Identity {
		return &providers.Failure{Code: "unavailable", Retryable: ctx.Err() == nil}
	}
	identity := c.Identity
	deadline, _ := ctx.Deadline()
	task := remote.Task{Version: 1, Execution: &runtime.RemoteExecution{Mode: "direct", Depth: 1, Deadline: deadline}, ExpectedHarnessIdentity: &identity, HarnessID: c.HarnessID, HarnessDifficulty: "unknown", ModelID: c.DestinationModel, Prompt: input.Messages[len(input.Messages)-1].Content, Messages: input.Messages, Domain: r.Domain, Profile: r.Profile, ContextTokens: r.ContextTokens, MaxCost: r.MaxCost, Private: r.LocalRequired}
	if task.Validate() != nil {
		return &providers.Failure{Code: "invalid_request"}
	}
	// Route binding is synced before dispatch. Uncertain delivery is never retried.
	dispatched := false
	emitted := false
	defer func() {
		if err != nil && dispatched {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			status, cancelErr := p.bridge.execution().CancelRecorded(cleanup, p.bridge.Store, p.key)
			confirmed := cancelErr == nil && (status.State == "failed" || status.State == "canceled")
			if cancelErr == nil && status.State == "running" && status.CancelRequested {
				ticker := time.NewTicker(250 * time.Millisecond)
				defer ticker.Stop()
				for !confirmed && cleanup.Err() == nil {
					select {
					case <-cleanup.Done():
					case <-ticker.C:
					}
					if cleanup.Err() != nil {
						break
					}
					observed, e := p.bridge.execution().InspectRecorded(cleanup, p.bridge.Store, p.key)
					if e != nil || observed.Destination != c.Instance {
						break
					}
					confirmed = observed.Status.State == "failed" || observed.Status.State == "canceled"
					if observed.Status.State == "succeeded" {
						break
					}
				}
			}
			var failure *providers.Failure
			if confirmed && !emitted && errors.As(err, &failure) && failure.Code == "unavailable" && !failure.Partial && ctx.Err() == nil {
				failure.Retryable = true
			}
			cancel()
		}
	}()
	dispatched = true
	status, e := p.bridge.execution().DispatchRecordedAs(ctx, p.bridge.Store, c.Instance, p.key, task, c.CallerFingerprint)
	if e != nil {
		return &providers.Failure{Code: "unavailable"}
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		switch status.State {
		case "succeeded":
			if status.Result == nil || status.Result.Text == "" {
				return &providers.Failure{Code: "invalid_response"}
			}

			finish := status.Result.FinishReason
			if finish == "" {
				finish = "stop"
			}
			if finish != "stop" && finish != "length" && finish != "content_filter" {
				return &providers.Failure{Code: "invalid_response"}
			}
			if status.Result.Usage != nil && (status.Result.Usage.InputTokens < 0 || status.Result.Usage.OutputTokens < 0) {
				return &providers.Failure{Code: "invalid_response"}
			}
			emitted = true
			if e := emit(providers.Chunk{Text: status.Result.Text}); e != nil {
				return e
			}
			return emit(providers.Chunk{Done: true, FinishReason: finish, Usage: status.Result.Usage})
		case "failed", "canceled":
			return &providers.Failure{Code: "unavailable", Retryable: ctx.Err() == nil}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		inspected, inspectErr := p.bridge.execution().InspectRecorded(ctx, p.bridge.Store, p.key)
		status, e = inspected.Status, inspectErr
		if e == nil && inspected.Destination != c.Instance {
			e = remote.ErrConflict
		}
		if e != nil {
			if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
				return e
			}
			return &providers.Failure{Code: "unavailable"}
		}
	}
}
