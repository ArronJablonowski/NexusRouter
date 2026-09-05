// Package v1 exposes the version-one in-process DarwinRouter SDK. Reuse a
// Client to share admission budgets. It owns no persistent database handle or
// background supervisor and does not require Close.
package v1

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/memory"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

var ErrAdmission = app.ErrAdmission
var ErrEventDelivery = app.ErrEventDelivery

// ResourceProfiler is the versioned measurement-engine contract. Resource
// reservation and route eligibility remain owned by the runtime.
type ResourceProfiler = resources.Profiler

type MemoryStore = memory.Store

// ConfigOptions has no implicit process-environment lookup. Environment and
// Overrides contain scalar configuration paths; LookupSecret resolves secrets.
type ConfigOptions struct {
	UserFile, ProjectFile  string
	Environment, Overrides map[string]string
	LookupSecret           func(string) string
	// ResourceProfiler replaces host measurement only; all admission checks stay
	// active. Nil keeps configured built-in behavior. This is trusted Go code.
	ResourceProfiler ResourceProfiler
	// MemoryStore is trusted process-local storage. Runtime retrieval is opt-in
	// and query-only; operator mutations remain explicit store operations.
	MemoryStore MemoryStore
}

type Client struct {
	service  *app.Service
	database string
}

type Request struct {
	Version                         int
	SummaryAttemptID                string
	Compaction                      *sessions.CompactionRequest
	Validation                      string
	ModelID, Prompt, ContinueTaskID string
	Messages                        []providers.Message
	Domain, Profile                 string
	Capabilities                    []string
	ContextTokens                   int
	MaxCost                         float64
	LocalRequired                   bool
}

type Result struct {
	Version              int
	PreviousTaskIDs      []string
	AuditID, AuditStatus string
	TaskID, Text         string
	Turns                int
	FinishReason         string
	Usage                *providers.Usage
}

func New(options ConfigOptions) (*Client, error) {
	clone := func(input map[string]string) map[string]string {
		out := make(map[string]string, len(input))
		for k, v := range input {
			out[k] = v
		}
		return out
	}
	cfg, err := config.Load(config.Options{UserFile: options.UserFile, ProjectFile: options.ProjectFile, Env: clone(options.Environment), Flags: clone(options.Overrides)})
	if err != nil {
		return nil, ErrAdmission
	}
	service, err := app.NewServiceWithEngines(cfg, options.LookupSecret, options.ResourceProfiler, options.MemoryStore)
	if err != nil {
		return nil, ErrAdmission
	}
	return &Client{service: service, database: cfg.Telemetry.Database}, nil
}

func (r Request) internal() app.Request {
	return app.Request{SummaryAttemptID: r.SummaryAttemptID, Compaction: r.Compaction, Validation: r.Validation, ModelID: r.ModelID, Prompt: r.Prompt, ContinueTaskID: r.ContinueTaskID, Messages: r.Messages, Domain: r.Domain, Profile: r.Profile, Capabilities: r.Capabilities, ContextTokens: r.ContextTokens, MaxCost: r.MaxCost, LocalRequired: r.LocalRequired}
}
func publicResult(r app.Result) Result {
	return Result{Version: 1, PreviousTaskIDs: r.PreviousTaskIDs, AuditID: r.AuditID, AuditStatus: r.AuditStatus, TaskID: r.TaskID, Text: r.Text, Turns: r.Turns, FinishReason: r.FinishReason, Usage: r.Usage}
}
func (c *Client) valid(ctx context.Context) bool { return c != nil && c.service != nil && ctx != nil }

func (c *Client) Run(ctx context.Context, r Request) (Result, error) {
	if !c.valid(ctx) || r.Version != 1 {
		return Result{Version: 1}, ErrAdmission
	}
	out, err := c.service.Run(ctx, r.internal())
	return publicResult(out), err
}
func (c *Client) RunStream(ctx context.Context, r Request, emit func(runtime.Event) error) (Result, error) {
	if !c.valid(ctx) || r.Version != 1 || emit == nil {
		return Result{Version: 1}, ErrAdmission
	}
	out, err := c.service.RunStream(ctx, r.internal(), emit)
	return publicResult(out), err
}
func (c *Client) CancelTask(ctx context.Context, task string) (runtime.CancellationStatus, error) {
	if !c.valid(ctx) {
		return runtime.CancellationStatus{}, ErrAdmission
	}
	return c.service.CancelTask(ctx, task)
}
func (c *Client) SteerTask(ctx context.Context, task, key, text string) (runtime.SteeringMessage, error) {
	if !c.valid(ctx) {
		return runtime.SteeringMessage{}, ErrAdmission
	}
	return c.service.SteerTask(ctx, task, key, text)
}
func (c *Client) SteeringStatus(ctx context.Context, task, id string) (runtime.SteeringMessage, error) {
	if !c.valid(ctx) {
		return runtime.SteeringMessage{}, ErrAdmission
	}
	return c.service.SteeringStatus(ctx, task, id)
}
func (c *Client) ListSteering(ctx context.Context, task string) ([]runtime.SteeringMessage, error) {
	if !c.valid(ctx) {
		return nil, ErrAdmission
	}
	return c.service.ListSteering(ctx, task)
}
func (c *Client) Feedback(ctx context.Context, task string, accepted bool, cost float64) error {
	if !c.valid(ctx) {
		return ErrAdmission
	}
	return app.RecordFeedback(ctx, c.database, task, accepted, cost)
}
func (c *Client) FeedbackHistory(ctx context.Context, task string) ([]evaluation.Record, error) {
	if !c.valid(ctx) {
		return nil, ErrAdmission
	}
	return app.FeedbackHistory(ctx, c.database, task)
}
func (c *Client) ReviseFeedback(ctx context.Context, task, expected string, accepted bool) error {
	if !c.valid(ctx) {
		return ErrAdmission
	}
	return app.ReviseFeedback(ctx, c.database, task, expected, accepted)
}
