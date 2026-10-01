// Package v1 exposes the version-one in-process NexusRouter SDK. Reuse a
// Client to share admission budgets. It owns no persistent database handle or
// background supervisor and does not require Close.
package v1

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/contextengine"
	"github.com/ArronJablonowski/NexusRouter/harness"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/memory"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/skills"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

var ErrAdmission = app.ErrAdmission
var ErrHarnessUnsupported = app.ErrHarnessUnsupported

type NativeHarness = app.NativeHarness
type NativeHarnessPrices = app.NativeHarnessPrices

var ErrEventDelivery = app.ErrEventDelivery

// ErrRecoveryExhausted reports that automatic routing exhausted its eligible
// candidates or configured attempt budget; inspect the returned task lineage.
var ErrRecoveryExhausted = app.ErrRecoveryExhausted

// ResourceProfiler is the versioned measurement-engine contract. Resource
// reservation and route eligibility remain owned by the runtime.
type ResourceProfiler = resources.Profiler

type MemoryStore = memory.Store

// MemoryUseStore is an optional snapshot-bound context-use capability. Legacy
// MemoryStore implementations without it remain read-only during execution.
type MemoryUseStore = memory.UseStore

type SkillStore = skills.Store

type ProviderFactory = providers.Factory

type ContextEstimator = providers.ContextEstimator

type ContextEngine = contextengine.Engine

type Evaluator = evaluation.Evaluator
type EvaluatorDescriptor = evaluation.EvaluatorDescriptor
type EvaluatorRequest = evaluation.EvaluatorRequest
type EvaluatorEvidence = evaluation.EvaluatorEvidence
type EvaluatorResponse = evaluation.EvaluatorResponse
type EvaluatorResult = evaluation.EvaluatorResult
type Audit = evaluation.Audit
type AuditFinding = evaluation.AuditFinding

type Tool = tools.Definition
type ToolPolicy = tools.Policy
type ApprovalPrompt = tools.ApprovalPrompt
type ApprovalReviewer = tools.ApprovalReviewer
type ApprovalPresenter = tools.ApprovalPresenter

// ConfigOptions has no implicit process-environment lookup. Environment and
// Overrides contain scalar configuration paths; LookupSecret resolves secrets.
type ConfigOptions struct {
	NativeHarnesses []NativeHarness
	// HarnessEvidence is host-owned; keep it open for the client lifetime.
	HarnessEvidence        *harness.EvidenceStore
	UserFile, ProjectFile  string
	Environment, Overrides map[string]string
	LookupSecret           func(string) string
	// ResourceProfiler replaces host measurement only; all admission checks stay
	// active. Nil keeps configured built-in behavior. This is trusted Go code.
	ResourceProfiler ResourceProfiler
	// MemoryStore is trusted process-local storage. Runtime retrieval is opt-in
	// and query-only; operator mutations remain explicit store operations.
	MemoryStore MemoryStore
	// SkillStore replaces opt-in skill discovery/loading only. The trusted store
	// must expose active validated versions; the runtime never mutates or closes it.
	SkillStore SkillStore
	// ProviderFactory replaces provider construction after admission. Nil keeps
	// built-in adapters. Factories are trusted code and must use the supplied
	// policy transport, honor cancellation, and support concurrent calls.
	ProviderFactory ProviderFactory
	// ContextEstimator receives isolated task context for conservative admission.
	// Automatic routing measures each candidate Model, then the runtime checks
	// each actual model turn. It cannot lower the built-in byte reserve.
	// Auxiliary audits and summaries measure their assembled, redacted prompts.
	// Trusted code must honor cancellation and privacy; this is not a sandbox.
	ContextEstimator ContextEstimator
	// ContextEstimatorFactory binds accounting to effective provider configuration.
	// It is mutually exclusive with ContextEstimator and ContextEngine.
	ContextEstimatorFactory ContextEstimatorFactory
	ContextEngine           ContextEngine
	// Evaluator replaces provider-backed advisory review execution while keeping
	// configured reviewer identity, privacy admission, durable lifecycle, and
	// evidence precedence under NexusRouter control. It is trusted in-process
	// code, must honor cancellation, and cannot grant tool authority. Nil keeps
	// the configured provider-backed reviewer. Typed nils are rejected.
	Evaluator Evaluator
	// EventSink receives owned, redacted runtime events only after their durable
	// commit. It is trusted, synchronous, live-only, and must support concurrent
	// calls from separate Client operations. Typed nils are rejected.
	EventSink EventSink
	// Tools registers trusted handlers independently of filesystem
	// tools. Definitions and policy are snapshotted at construction; nil policy
	// denies all custom tools. Handlers must honor cancellation and concurrency.
	Tools      []Tool
	ToolPolicy *ToolPolicy
	// ApprovalReviewer explicitly enables reviewed writes and Ask tools. It must
	// authenticate the operator, inspect the exact proposed arguments and honor
	// cancellation. Arguments may contain sensitive data; never log them blindly.
	// Nil preserves read-only Allow-only registration. This is trusted host code,
	// not a model evaluator; its presence never grants authority to child workers.
	ApprovalReviewer ApprovalReviewer
	// ApprovalPresenter publishes a private preview then waits for a separately
	// authenticated decision through DecideApproval. Exclusive with reviewer.
	ApprovalPresenter ApprovalPresenter
}

type Client struct {
	service  *app.Service
	database string
}

type Request struct {
	HarnessID                       string
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
	HarnessOutcome          *harness.Execution
	HarnessSelection        *harness.Selection
	HarnessReview           *harness.Review
	HarnessReviewStatus     string
	HarnessAuditOperationID string
	Version                 int
	PreviousTaskIDs         []string
	RouteEstimatedCost      *float64
	AuditID, AuditStatus    string
	TaskID, Text            string
	Turns                   int
	FinishReason            string
	Usage                   *providers.Usage
}

func New(options ConfigOptions) (*Client, error) {
	if !validSDKEventSink(options.EventSink) {
		return nil, ErrAdmission
	}
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
	if options.ContextEstimatorFactory != nil {
		if options.ContextEstimator != nil || options.ContextEngine != nil {
			return nil, ErrAdmission
		}
		options.ContextEstimator, err = configuredContextEstimator(cfg, options.ContextEstimatorFactory)
		if err != nil {
			return nil, ErrAdmission
		}
	}
	constructor := tools.NewExtension
	if options.ApprovalReviewer != nil || options.ApprovalPresenter != nil {
		constructor = tools.NewApprovalExtension
	}
	extension, err := constructor(options.Tools, options.ToolPolicy)
	if err != nil {
		return nil, ErrAdmission
	}
	if options.ContextEngine != nil && options.ContextEstimator != nil {
		return nil, ErrAdmission
	}
	var service *app.Service
	if options.ContextEngine != nil {
		service, err = app.NewServiceWithContextEngineEvaluatorAndEventSink(cfg, options.LookupSecret, options.ResourceProfiler, options.MemoryStore, options.SkillStore, options.ProviderFactory, extension, options.ApprovalReviewer, options.ApprovalPresenter, options.ContextEngine, options.Evaluator, options.EventSink)
	} else {
		service, err = app.NewServiceWithContextEstimatorEvaluatorAndEventSink(cfg, options.LookupSecret, options.ResourceProfiler, options.MemoryStore, options.SkillStore, options.ProviderFactory, extension, options.ApprovalReviewer, options.ApprovalPresenter, options.ContextEstimator, options.Evaluator, options.EventSink)
	}
	if err != nil {
		return nil, ErrAdmission
	}
	if options.NativeHarnesses != nil {
		if err := service.ConfigureNativeHarnesses(options.NativeHarnesses, options.HarnessEvidence); err != nil {
			return nil, err
		}
	} else if options.HarnessEvidence != nil {
		service.ConfigureHarnessEvidence(options.HarnessEvidence)
	}
	return &Client{service: service, database: cfg.Telemetry.Database}, nil
}

func (r Request) internal() app.Request {
	return app.Request{HarnessID: r.HarnessID, SummaryAttemptID: r.SummaryAttemptID, Compaction: r.Compaction, Validation: r.Validation, ModelID: r.ModelID, Prompt: r.Prompt, ContinueTaskID: r.ContinueTaskID, Messages: r.Messages, Domain: r.Domain, Profile: r.Profile, Capabilities: r.Capabilities, ContextTokens: r.ContextTokens, MaxCost: r.MaxCost, LocalRequired: r.LocalRequired}
}
func publicResult(r app.Result) Result {
	return Result{Version: 1, HarnessOutcome: r.HarnessOutcome, HarnessSelection: r.HarnessSelection, HarnessReview: r.HarnessReview, HarnessReviewStatus: r.HarnessReviewStatus, HarnessAuditOperationID: r.HarnessAuditOperationID, PreviousTaskIDs: r.PreviousTaskIDs, RouteEstimatedCost: r.RouteEstimatedCost, AuditID: r.AuditID, AuditStatus: r.AuditStatus, TaskID: r.TaskID, Text: r.Text, Turns: r.Turns, FinishReason: r.FinishReason, Usage: r.Usage}
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

// RunTextStream delivers provisional, incrementally redacted assistant text.
// Unlike RunStream, token text is not replayable. Intermediate assistant turns
// may be included; a successful return alone establishes task completion.
func (c *Client) RunTextStream(ctx context.Context, r Request, emit func(string) error) (Result, error) {
	if !c.valid(ctx) || r.Version != 1 || emit == nil {
		return Result{Version: 1}, ErrAdmission
	}
	out, err := c.service.RunTextStream(ctx, r.internal(), emit)
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
