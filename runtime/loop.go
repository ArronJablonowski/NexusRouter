package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/providers"
)

// Journal must commit before returning nil. Implemented by the SQLite store.
type Journal interface {
	Append(context.Context, int64, Event) error
}

// ContextCompactionJournal is an optional atomic extension used only for a
// lifecycle-backed context compaction plan. Implementations must commit the
// ContextCompacted event and the plan's activation evidence in one transaction
// before returning nil. A runtime supplied with a CompactionPlan fails closed
// unless its Journal implements this capability.
type ContextCompactionJournal interface {
	AppendContextCompaction(context.Context, int64, Event, ContextCompactionPlan) error
}

// ToolExecutor is an authorization boundary, not a bare function dispatcher.
// Implementations must validate schemas, enforce inherited permissions and own
// the resource lease before performing an effect. They must not retry uncertain
// or confirmed effects. A missing executor denies all tools.
type ToolExecutor interface {
	Execute(context.Context, providers.ToolCall) (ToolResult, error)
}

// ScopedToolExecutor is an optional extension to ToolExecutor for authorization
// bound to a durable execution identity. The loop prefers it when available,
// only after ToolStarted commits. These identities are assigned by the runtime,
// not supplied by the model. They identify work; they do not grant permission.
type ScopedToolExecutor interface {
	ExecuteScoped(context.Context, ToolExecution) (ToolResult, error)
}

type ToolExecution struct {
	TaskID, SessionID, TurnID, AttemptID string
	Call                                 providers.ToolCall
}

type ToolResult struct {
	Content string
	Effect  Effect
	// Failed reports an explicit trusted-handler failure independently of side
	// effects. A failed operation may have no effect or a confirmed effect;
	// neither is automatically uncertainty. The loop records tool_failed.
	// Model text cannot set this flag.
	Failed bool
	// Recoverable permits a new model turn after a known, effect-free failure.
	// It is valid only with Failed and NoEffect; it never retries the tool call,
	// bypasses budgets, or grants authority to a subsequent proposed operation.
	Recoverable bool
}
type RunRequest struct {
	SkillContext         *SkillContextUse
	IntentClassification *IntentClassificationUse
	SubmissionID         string
	// WorkerID is an optional trusted host-supplied execution identity. It is
	// copied onto every durable event emitted by this run so an enclosing
	// worker capability can bind the inner agent loop to its durable owner.
	// Prompts, model output, and tool arguments must never populate it. An
	// empty value preserves root-task behavior.
	WorkerID           string
	Compaction         *ContextCompaction
	ApprovedCompaction *ApprovedCompaction
	// CompactionPlan is host-admitted durable evidence for one future prefix
	// replacement. Unlike the legacy ApprovedCompaction path, activation must
	// atomically persist both the ContextCompacted event and lifecycle evidence.
	CompactionPlan *ContextCompactionPlan
	ContextLineage *ContextLineage
	Validation     string
	RetryOfTaskID  string
	// ConfigID is the trusted host's digest of the effective, redacted runtime
	// configuration. When supplied it is persisted on task.started so an
	// enclosing admission protocol can bind execution to that exact generation.
	ConfigID           string
	RouteEstimatedCost *float64
	// RequireText applies only to a final answer, never an intermediate tool
	// proposal. Non-text host workflows may leave this false explicitly.
	RequireText bool
	// RequireContextRollover requires a stateful provider rollover backed by an
	// exact durable CompactionPlan. Stateless providers leave this false.
	RequireContextRollover        bool
	Domain, Profile               string
	Capabilities                  []string
	Route                         *Data
	ParentTaskID, Privacy         string
	TaskID, SessionID, ProviderID string
	Inference                     providers.Request
	MaxTurns                      int
	MaxContextTokens              int
	MaxOutputBytes                int
}
type Result struct {
	Retryable    bool
	Text         string
	Turns        int
	FinishReason string
	Usage        *providers.Usage
}
type Loop struct {
	// ContextEstimator is trusted, cooperative host code. Its estimates cannot
	// reduce the built-in conservative context floor.
	ContextEstimator providers.ContextEstimator
	Steering         SteeringSource
	Provider         providers.Provider
	Journal          Journal
	Tools            ToolExecutor
	// ValidationText supplies the host's persisted/delivered view (for example
	// secret redaction). It must match the journal and output adapter exactly.
	// It is trusted host code, never a model-supplied transformation.
	ValidationText func(string) string
}

var (
	ErrInvalidRun      = errors.New("invalid runtime request")
	ErrProvider        = errors.New("model turn failed")
	ErrContextOverflow = errors.New("context window exceeded")
	ErrProtocol        = errors.New("invalid model stream")
	ErrLimit           = errors.New("runtime budget exhausted")
	ErrEmptyOutput     = errors.New("required final text is empty")
	ErrInvalidOutput   = errors.New("final output failed requested validation")
	ErrTool            = errors.New("tool execution failed or denied")
	ErrJournalLimit    = errors.New("durable task journal budget exhausted")
	ErrPersistence     = errors.New("runtime persistence failed; inspect durable state before retry")
)

// Run starts a new durable task. It does not resume or silently retry existing
// task IDs. Completion means the loop ended, not that output passed evaluation.
func (l Loop) Run(ctx context.Context, r RunRequest) (returned Result, runErr error) {
	if r.WorkerID != "" && !validRunWorkerID(r.WorkerID) {
		return Result{}, ErrInvalidRun
	}
	if r.SkillContext.Validate() != nil {
		return Result{}, ErrInvalidRun
	}
	if (r.IntentClassification != nil && r.IntentClassification.Validate() != nil) || !validTaskCapabilities(r.Capabilities) {
		return Result{}, ErrInvalidRun
	}
	var intentClassification *IntentClassificationUse
	if r.IntentClassification != nil {
		use := *r.IntentClassification
		intentClassification = &use
	}
	capabilities := append([]string(nil), r.Capabilities...)
	skillContext := r.SkillContext.Clone()
	if r.Compaction != nil && r.Compaction.Validate(r.ParentTaskID) != nil {
		return Result{}, ErrInvalidRun
	}
	var contextLineage *ContextLineage
	if r.ContextLineage != nil {
		if r.ContextLineage.Validate() != nil {
			return Result{}, ErrInvalidRun
		}
		body, lineageErr := json.Marshal(r.ContextLineage)
		if lineageErr != nil || json.Unmarshal(body, &contextLineage) != nil {
			return Result{}, ErrInvalidRun
		}
	}
	if r.ApprovedCompaction != nil && (r.Compaction != nil || r.CompactionPlan != nil || r.MaxContextTokens < 1 || r.ApprovedCompaction.validate(r.ParentTaskID, r.Inference.Messages) != nil) {
		return Result{}, ErrInvalidRun
	}
	if r.CompactionPlan != nil && (r.Compaction != nil || r.MaxContextTokens < 1 || r.CompactionPlan.Validate() != nil ||
		r.CompactionPlan.Compaction == nil || r.CompactionPlan.Compaction.SourceTaskID != r.ParentTaskID) {
		return Result{}, ErrInvalidRun
	}
	if r.CompactionPlan != nil {
		if _, ok := l.Journal.(ContextCompactionJournal); !ok {
			return Result{}, ErrInvalidRun
		}
		original, encodeErr := json.Marshal(r.CompactionPlan.OriginalPrefix)
		initial, initialErr := json.Marshal(r.Inference.Messages)
		if encodeErr != nil || initialErr != nil || !bytes.Equal(original, initial) {
			return Result{}, ErrInvalidRun
		}
	}
	rolloverProvider, hasContextRollover := l.Provider.(providers.ContextRolloverProvider)
	if r.RequireContextRollover && (!hasContextRollover || r.CompactionPlan == nil) {
		return Result{}, ErrInvalidRun
	}
	if r.MaxContextTokens < 0 || (l.ContextEstimator != nil && r.MaxContextTokens == 0) || (r.Validation != "" && r.Validation != "go_source") || (r.Validation != "" && !r.RequireText) ||
		(r.ConfigID != "" && !validConfigID(r.ConfigID)) || r.Inference.MaxOutputTokens < 0 || r.Inference.MaxOutputTokens > providers.MaxOutputTokens {
		return Result{}, ErrInvalidRun
	}
	if l.Provider == nil || l.Journal == nil || r.TaskID == "" || r.SessionID == "" || r.ProviderID == "" || r.Inference.Model == "" || len(r.Inference.Messages) == 0 || r.MaxTurns < 1 || r.MaxTurns > 1000 || r.MaxOutputBytes < 1 || r.MaxOutputBytes > 16<<20 {
		return Result{}, ErrInvalidRun
	}
	// Snapshot nested caller-owned data before the provider receives it.
	var compaction *ContextCompaction
	if r.Compaction != nil {
		body, err := json.Marshal(r.Compaction)
		if err != nil || json.Unmarshal(body, &compaction) != nil {
			return Result{}, ErrInvalidRun
		}
	}
	if providers.ValidateMessages(r.Inference.Messages) != nil {
		return Result{}, ErrInvalidRun
	}
	b, err := json.Marshal(r.Inference)
	if err != nil {
		return Result{}, ErrInvalidRun
	}
	var inference providers.Request
	if json.Unmarshal(b, &inference) != nil {
		return Result{}, ErrInvalidRun
	}
	if compaction != nil && (compaction.Version >= 2 || contextLineage != nil) {
		contextLineage, err = ExtendContextLineage(contextLineage, r.TaskID, 1, compaction, inference.Messages)
		if err != nil {
			return Result{}, ErrInvalidRun
		}
	}
	// A nil RawMessage round-trips through JSON as the literal bytes "null".
	// Preserve absence so adapters do not interpret it as a requested schema.
	if len(r.Inference.JSONSchema) == 0 {
		inference.JSONSchema = nil
	}
	pendingCompaction, err := cloneApprovedCompaction(r.ApprovedCompaction)
	if err != nil || pendingCompaction != nil && pendingCompaction.validate(r.ParentTaskID, inference.Messages) != nil {
		return Result{}, ErrInvalidRun
	}
	var pendingCompactionPlan *ContextCompactionPlan
	if r.CompactionPlan != nil {
		owned, cloneErr := cloneContextCompactionPlan(*r.CompactionPlan)
		if cloneErr != nil || owned.Validate() != nil {
			return Result{}, ErrInvalidRun
		}
		pendingCompactionPlan = &owned
		pendingCompaction = &ApprovedCompaction{
			Compaction:        owned.Compaction,
			OriginalPrefix:    owned.OriginalPrefix,
			ReplacementPrefix: owned.ReplacementPrefix,
		}
		if pendingCompaction.validate(r.ParentTaskID, inference.Messages) != nil {
			return Result{}, ErrInvalidRun
		}
	}
	seq := int64(0)
	turn := ""
	attempt := ""
	persistWithPlan := func(ctx context.Context, k Kind, d Data, plan *ContextCompactionPlan) error {
		e := Event{Version: 1, ID: rand.Text(), TaskID: r.TaskID, SessionID: r.SessionID, CorrelationID: r.TaskID, WorkerID: r.WorkerID, Sequence: seq + 1, Time: time.Now().UTC(), Kind: k, TurnID: turn, AttemptID: attempt, Data: d}
		if k == RouteSelected {
			e.RouteID = rand.Text()
		}
		if k == SteeringApplied || k == ContextCompacted {
			e.TurnID, e.AttemptID = "", ""
		}
		var err error
		if plan == nil {
			err = invokeJournalAppend(ctx, l.Journal, seq, e)
		} else if journal, ok := l.Journal.(ContextCompactionJournal); ok && k == ContextCompacted {
			err = invokeJournalContextCompaction(ctx, journal, seq, e, *plan)
		} else {
			err = ErrPersistence
		}
		if err != nil {
			if errors.Is(err, ErrSteeringPending) {
				return ErrSteeringPending
			}
			if errors.Is(err, ErrExecutionLeaseLost) {
				return ErrExecutionLeaseLost
			}
			if errors.Is(err, ErrCancellationRequested) {
				return ErrCancellationRequested
			}
			if errors.Is(err, ErrJournalLimit) {
				return ErrJournalLimit
			}
			return ErrPersistence
		}
		seq++
		return nil
	}
	persist := func(ctx context.Context, k Kind, d Data) error {
		return persistWithPlan(ctx, k, d, nil)
	}
	var routeEstimatedCost *float64
	if r.RouteEstimatedCost != nil {
		cost := *r.RouteEstimatedCost
		routeEstimatedCost = &cost
	}
	if err := persist(ctx, TaskStarted, Data{SkillContext: skillContext, IntentClassification: intentClassification, SubmissionID: r.SubmissionID, Compaction: compaction, ContextLineage: contextLineage, Validation: r.Validation, RetryOfTaskID: r.RetryOfTaskID, ConfigID: r.ConfigID, RouteEstimatedCost: routeEstimatedCost, Messages: inference.Messages, ModelID: inference.Model, ProviderID: r.ProviderID, ParentTaskID: r.ParentTaskID, Privacy: r.Privacy, Domain: r.Domain, Profile: r.Profile, Capabilities: capabilities, ContextTokens: int(inference.ContextTokens)}); err != nil {
		return Result{}, err
	}
	result := Result{}
	compactionActivated := false
	lastProviderFinishReason := ""
	appliedSteering := 0
	// The cancellation gate guarantees no append occurred. Unlike an ambiguous
	// storage failure, it is safe to append one bounded cancellation terminal.
	defer func() {
		if (!errors.Is(runErr, ErrCancellationRequested) && !errors.Is(runErr, ErrExecutionLeaseLost)) || errors.Is(runErr, ErrPersistence) {
			return
		}
		returned.Retryable = false
		terminal, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		code := "canceled"
		if errors.Is(runErr, ErrExecutionLeaseLost) {
			code = "execution_lease_lost"
		}
		if err := persist(terminal, TaskCanceled, Data{Code: code}); err != nil {
			runErr = errors.Join(context.Canceled, runErr, err)
			return
		}
		runErr = errors.Join(context.Canceled, runErr)
	}()
	fail := func(cause error) (Result, error) {
		kind := TaskFailed
		code := "execution_failed"
		if result.Retryable && errors.Is(cause, ErrProvider) {
			code = "provider_retryable_no_output"
		}
		var providerFailure *providers.Failure
		if errors.As(cause, &providerFailure) && providerFailure != nil && providerFailure.Code == "context_overflow" {
			code = "context_overflow"
			result.Retryable = false
		}
		if errors.Is(cause, ErrLimit) {
			code = "budget_exhausted"
		}
		if errors.Is(cause, ErrJournalLimit) {
			code = "journal_exhausted"
		}
		if errors.Is(cause, providers.ErrContextEstimate) {
			code = "context_estimation_failed"
		}
		if errors.Is(cause, ErrContextOverflow) {
			code = "context_overflow"
		}
		if errors.Is(cause, ErrEmptyOutput) {
			code = "empty_output"
		}
		if errors.Is(cause, ErrInvalidOutput) {
			code = "invalid_output"
		}
		if ctx.Err() != nil {
			kind = TaskCanceled
			code = "canceled"
			cause = ctx.Err()
		}
		// Cancellation still requires a bounded durable terminal transition.
		terminal, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := persist(terminal, kind, Data{Code: code}); err != nil {
			if errors.Is(err, ErrSteeringPending) && kind == TaskFailed && code == "provider_retryable_no_output" {
				result.Retryable = false
				if rewriteErr := persist(terminal, TaskFailed, Data{Code: "execution_failed"}); rewriteErr != nil {
					return result, rewriteErr
				}
				return result, cause
			}
			return result, err
		}
		return result, cause
	}
	terminalizeJournalLimit := func(err error) (Result, error) {
		if errors.Is(err, ErrJournalLimit) {
			return fail(err)
		}
		return result, err
	}
	seen := map[string]bool{}
	if contextLineage != nil {
		for _, id := range contextLineage.ToolCallIDs {
			seen[id] = true
		}
	}
	for _, message := range inference.Messages {
		for _, call := range message.ToolCalls {
			seen[call.ID] = true
		}
	}
	// fitForDispatch may shorten only the frozen initial prefix. The additional
	// messages argument represents prospective, not-yet-durable steering and is
	// never installed until its own event commits. Live task messages are always
	// retained as an indivisible suffix.
	fitForDispatch := func(base providers.Request, additional []providers.Message) (providers.Request, providers.Request, error) {
		prospective := base
		prospective.Messages = append(append([]providers.Message(nil), base.Messages...), additional...)
		estimate, estimateErr := providers.EstimateWith(ctx, l.ContextEstimator, prospective)
		if estimateErr != nil {
			return base, prospective, providers.ErrContextEstimate
		}
		if estimate <= r.MaxContextTokens {
			return base, prospective, nil
		}
		if pendingCompaction == nil || compactionActivated || result.Turns == 0 || len(base.Messages) < len(pendingCompaction.OriginalPrefix) {
			return base, prospective, ErrContextOverflow
		}
		if hasContextRollover && lastProviderFinishReason != "stop" {
			return base, prospective, ErrContextOverflow
		}
		prefix, encodeErr := json.Marshal(base.Messages[:len(pendingCompaction.OriginalPrefix)])
		original, originalErr := json.Marshal(pendingCompaction.OriginalPrefix)
		if encodeErr != nil || originalErr != nil || !bytes.Equal(prefix, original) {
			return base, prospective, ErrProtocol
		}
		compacted := base
		compacted.Messages = append(append([]providers.Message(nil), pendingCompaction.ReplacementPrefix...), base.Messages[len(pendingCompaction.OriginalPrefix):]...)
		compactedProspective := compacted
		compactedProspective.Messages = append(append([]providers.Message(nil), compacted.Messages...), additional...)
		if providers.ValidateMessages(compactedProspective.Messages) != nil {
			return base, prospective, ErrProtocol
		}
		body, encodeErr := json.Marshal(compactedProspective.Messages)
		if encodeErr != nil || len(body) > 4<<20 {
			return base, prospective, ErrLimit
		}
		compactEstimate, estimateErr := providers.EstimateWith(ctx, l.ContextEstimator, compactedProspective)
		if estimateErr != nil {
			return base, prospective, providers.ErrContextEstimate
		}
		if compactEstimate > r.MaxContextTokens || compactEstimate >= estimate {
			return base, prospective, ErrContextOverflow
		}
		invokeRollover := func(activate bool, current, replacement providers.Request) (err error) {
			defer func() {
				if recover() != nil {
					if ctx.Err() != nil {
						err = ctx.Err()
					} else {
						err = errors.Join(ErrProvider, &providers.Failure{Code: "context_rollover_failed"})
					}
				}
			}()
			if activate {
				err = rolloverProvider.ActivateContextRollover(ctx, replacement)
			} else {
				err = rolloverProvider.CheckContextRollover(ctx, current, replacement)
			}
			if err == nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.Join(ErrProvider, &providers.Failure{Code: "context_rollover_failed"})
		}
		if hasContextRollover {
			if rolloverErr := invokeRollover(false, base, compactedProspective); rolloverErr != nil {
				return base, prospective, rolloverErr
			}
			if ctx.Err() != nil {
				return base, prospective, ctx.Err()
			}
		}
		nextLineage := contextLineage
		if pendingCompaction.Compaction.Version >= 2 || contextLineage != nil {
			var lineageErr error
			nextLineage, lineageErr = ExtendContextLineage(contextLineage, r.TaskID, seq+1, pendingCompaction.Compaction, base.Messages)
			if lineageErr != nil {
				return base, prospective, ErrProtocol
			}
		}
		if persistErr := persistWithPlan(ctx, ContextCompacted, Data{Compaction: pendingCompaction.Compaction, ContextLineage: nextLineage, ParentTaskID: r.ParentTaskID, Messages: pendingCompaction.ReplacementPrefix, ReplacedMessages: len(pendingCompaction.OriginalPrefix)}, pendingCompactionPlan); persistErr != nil {
			return base, prospective, persistErr
		}
		contextLineage = nextLineage
		if contextLineage != nil {
			for _, id := range contextLineage.ToolCallIDs {
				seen[id] = true
			}
		}
		compactionActivated = true
		if hasContextRollover {
			if rolloverErr := invokeRollover(true, providers.Request{}, compacted); rolloverErr != nil {
				return base, prospective, rolloverErr
			}
		}
		if ctx.Err() != nil {
			return base, prospective, ctx.Err()
		}
		return compacted, compactedProspective, nil
	}
	drain := func() (applied bool, err error) {
		if l.Steering == nil {
			return false, nil
		}
		next := func() (message *SteeringMessage, err error) {
			defer func() {
				if recover() != nil {
					err = ErrProtocol
				}
			}()
			return l.Steering.NextSteering(ctx, r.TaskID)
		}
		for {
			if ctx.Err() != nil {
				return applied, ctx.Err()
			}
			message, sourceErr := next()
			if sourceErr != nil {
				return applied, ErrProtocol
			}
			if message == nil {
				return applied, nil
			}
			if message.Validate() != nil || message.TaskID != r.TaskID || message.State != "pending" {
				return applied, ErrProtocol
			}
			if appliedSteering >= MaxSteeringMessages {
				return applied, ErrLimit
			}
			guidance := []providers.Message{{Role: "user", Content: message.Text}}
			candidate := inference
			candidate.Messages = append(append([]providers.Message(nil), inference.Messages...), guidance...)
			body, encodeErr := json.Marshal(candidate.Messages)
			if encodeErr != nil || len(body) > 4<<20 {
				return applied, ErrLimit
			}
			if r.MaxContextTokens > 0 {
				var fitErr error
				inference, candidate, fitErr = fitForDispatch(inference, guidance)
				if fitErr != nil {
					return applied, fitErr
				}
			}
			if persistErr := persist(ctx, SteeringApplied, Data{SteeringID: message.ID, Text: message.Text}); persistErr != nil {
				return applied, persistErr
			}
			inference = candidate
			appliedSteering++
			applied = true
		}
	}
	boundaryFailure := func(err error) (Result, error) {
		if errors.Is(err, ErrPersistence) || errors.Is(err, ErrCancellationRequested) || errors.Is(err, ErrExecutionLeaseLost) {
			return result, err
		}
		return fail(err)
	}
	// A committed append may notify a sink that cancels the task. Observe
	// that cancellation before the next normal append or external dispatch;
	// an actual failed append remains ambiguous and is never retried here.
	if ctx.Err() != nil {
		return fail(ctx.Err())
	}
	if r.Route != nil {
		if err := persist(ctx, RouteSelected, *r.Route); err != nil {
			return result, err
		}
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
	}
	used := 0
	usageComplete := true
	totalUsage := providers.Usage{}
	outputTokenBudget := inference.MaxOutputTokens
	for n := 0; n < r.MaxTurns; n++ {
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		if _, err := drain(); err != nil {
			return boundaryFailure(err)
		}
		if body, err := json.Marshal(inference.Messages); err != nil || len(body) > 4<<20 {
			return fail(ErrLimit)
		}
		if r.MaxContextTokens > 0 {
			var fitErr error
			inference, _, fitErr = fitForDispatch(inference, nil)
			if fitErr != nil {
				return boundaryFailure(fitErr)
			}
		}
		if outputTokenBudget > 0 {
			if !usageComplete || totalUsage.OutputTokens >= outputTokenBudget {
				return fail(ErrLimit)
			}
			inference.MaxOutputTokens = outputTokenBudget - totalUsage.OutputTokens
		}
		turn = rand.Text()
		attempt = rand.Text()
		result.Turns++
		if err := persist(ctx, TurnStarted, Data{ModelID: inference.Model, ProviderID: r.ProviderID}); err != nil {
			return terminalizeJournalLimit(err)
		}
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		var text strings.Builder
		calls := []providers.ToolCall{}
		var usage *providers.Usage
		done := false
		reason := ""
		committedOutput := false
		var callbackErr error
		err := invokeProviderStream(ctx, l.Provider, inference, func(c providers.Chunk) error {
			if callbackErr != nil {
				return callbackErr
			}
			accept := func() error {
				if done {
					return ErrProtocol
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				used += len(c.Text)
				if c.ToolCall != nil {
					call := *c.ToolCall
					arguments, canonicalErr := canonicalToolArguments(call.Arguments)
					if call.ID == "" || call.Name == "" || seen[call.ID] || len(calls) >= 128 || canonicalErr != nil {
						return ErrProtocol
					}
					seen[call.ID] = true
					// Persist and execute one canonical byte representation. Durable
					// approval digests can then be re-derived after restart without
					// depending on provider whitespace or object-member ordering.
					call.Arguments = arguments
					used += len(call.Arguments) + len(call.Name) + len(call.ID)
					calls = append(calls, call)
				}
				if used > r.MaxOutputBytes {
					return ErrLimit
				}
				if c.Text != "" {
					text.WriteString(c.Text)
					if err := persist(ctx, ModelDelta, Data{Text: c.Text}); err != nil {
						return err
					}
					committedOutput = true
					if ctx.Err() != nil {
						return ctx.Err()
					}
				}
				if c.Usage != nil {
					if c.Usage.InputTokens < 0 || c.Usage.OutputTokens < 0 {
						return ErrProtocol
					}
					copy := *c.Usage
					usage = &copy
				}
				if c.Done {
					done = true
					reason = c.FinishReason
				}
				return nil
			}
			callbackErr = accept()
			return callbackErr
		}, &committedOutput)
		if errors.Is(callbackErr, ErrPersistence) {
			return result, ErrPersistence
		}
		if errors.Is(callbackErr, ErrCancellationRequested) || errors.Is(callbackErr, ErrExecutionLeaseLost) {
			return result, callbackErr
		}
		if callbackErr != nil {
			return fail(callbackErr)
		}
		if err != nil {
			var providerFailure *providers.Failure
			safe := n == 0 && appliedSteering == 0 && text.Len() == 0 && len(calls) == 0 && ctx.Err() == nil && errors.As(err, &providerFailure) && providerFailure.Retryable && !providerFailure.Partial
			result.Retryable = safe
			out, failErr := fail(errors.Join(ErrProvider, err))
			out.Retryable = out.Retryable && safe && errors.Is(failErr, ErrProvider)
			return out, failErr
		}
		if !done {
			return fail(ErrProtocol)
		}
		if reason != "stop" && reason != "tool_calls" {
			return fail(ErrLimit)
		}
		if reason == "tool_calls" && len(calls) == 0 {
			return fail(ErrProtocol)
		}
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		if err := persist(ctx, TurnCompleted, Data{Text: text.String(), ToolCalls: calls, Usage: usage, FinishReason: reason}); err != nil {
			return terminalizeJournalLimit(err)
		}
		lastProviderFinishReason = reason
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		if usage == nil || usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.InputTokens > math.MaxInt64-totalUsage.InputTokens || usage.OutputTokens > math.MaxInt64-totalUsage.OutputTokens {
			usageComplete = false
		} else {
			totalUsage.InputTokens += usage.InputTokens
			totalUsage.OutputTokens += usage.OutputTokens
		}
		if outputTokenBudget > 0 && usageComplete && totalUsage.OutputTokens > outputTokenBudget {
			return fail(ErrLimit)
		}
		// A bounded task cannot safely perform tools when it cannot prove enough
		// output capacity remains to consume their results on another turn.
		if len(calls) > 0 && outputTokenBudget > 0 && (!usageComplete || totalUsage.OutputTokens >= outputTokenBudget) {
			return fail(ErrLimit)
		}
		if len(calls) == 0 {
			inference.Messages = append(inference.Messages, providers.Message{Role: "assistant", Content: text.String()})
			if applied, err := drain(); err != nil {
				return boundaryFailure(err)
			} else if applied {
				continue
			}
			validationText := text.String()
			if l.ValidationText != nil {
				validationText = l.ValidationText(validationText)
			}
			if ctx.Err() != nil {
				return fail(ctx.Err())
			}
			if r.RequireText {
				accepted := strings.TrimSpace(validationText) != ""
				if err := persist(ctx, EvaluationRecorded, Data{Accepted: &accepted, Code: "deterministic.nonempty_text.v1", ModelID: inference.Model, ProviderID: r.ProviderID, Domain: r.Domain, Profile: r.Profile}); err != nil {
					return terminalizeJournalLimit(err)
				}
				if ctx.Err() != nil {
					return fail(ctx.Err())
				}
				if !accepted {
					return fail(ErrEmptyOutput)
				}
			}
			if r.Validation == "go_source" {
				accepted := evaluation.GoSourceValid(validationText)
				if err := persist(ctx, EvaluationRecorded, Data{Accepted: &accepted, Code: "deterministic.go_syntax.v1", ModelID: inference.Model, ProviderID: r.ProviderID, Domain: r.Domain, Profile: r.Profile}); err != nil {
					return terminalizeJournalLimit(err)
				}
				if ctx.Err() != nil {
					return fail(ctx.Err())
				}
				if !accepted {
					return fail(ErrInvalidOutput)
				}
			}
			if ctx.Err() != nil {
				return fail(ctx.Err())
			}
			if err := persist(ctx, TaskCompleted, Data{}); err != nil {
				if errors.Is(err, ErrSteeringPending) {
					applied, drainErr := drain()
					if drainErr != nil {
						return boundaryFailure(drainErr)
					}
					if !applied {
						return fail(ErrProtocol)
					}
					continue
				}
				return result, err
			}
			result.Text = text.String()
			result.FinishReason = reason
			if usageComplete {
				result.Usage = &totalUsage
			}
			return result, nil
		}
		// Never perform effects on the last permitted turn: their results could
		// not be consumed within this task's iteration budget.
		if n+1 == r.MaxTurns {
			return fail(ErrLimit)
		}
		if l.Tools == nil {
			return fail(ErrTool)
		}
		inference.Messages = append(inference.Messages, providers.Message{Role: "assistant", Content: text.String(), ToolCalls: calls})
		for _, call := range calls {
			if ctx.Err() != nil {
				return fail(ctx.Err())
			}
			behavior, behaviorErr := declaredToolBehavior(l.Tools, call.Name)
			if behaviorErr != nil {
				return fail(ErrTool)
			}
			if err := persist(ctx, ToolStarted, Data{ToolCallID: call.ID, ToolName: call.Name, ToolBehavior: behavior, Effect: UncertainEffect}); err != nil {
				return terminalizeJournalLimit(err)
			}
			out := ToolResult{Effect: NoEffect}
			toolErr := ctx.Err()
			if toolErr == nil {
				out, toolErr = invokeTool(ctx, l.Tools, ToolExecution{TaskID: r.TaskID, SessionID: r.SessionID, TurnID: turn, AttemptID: attempt, Call: call})
			}
			if out.Effect != NoEffect && out.Effect != ConfirmedEffect && out.Effect != UncertainEffect {
				out.Effect = UncertainEffect
				toolErr = ErrTool
			}
			if out.Recoverable && (!out.Failed || out.Effect != NoEffect) {
				toolErr = ErrTool
			}
			if behavior == BehaviorReadOnly && out.Effect == ConfirmedEffect {
				// A declaration cannot erase an observed effect. Keep the effect
				// as evidence, but do not release this contradictory result.
				toolErr = ErrTool
			}
			used += len(out.Content)
			if used > r.MaxOutputBytes {
				out.Content = ""
				toolErr = ErrLimit
			}
			code := ""
			if toolErr != nil || out.Failed {
				code = "tool_failed"
			}
			terminal, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			err := persist(terminal, ToolCompleted, Data{ToolCallID: call.ID, ToolName: call.Name, ToolBehavior: behavior, Effect: out.Effect, Text: out.Content, Code: code})
			cancel()
			if err != nil {
				return terminalizeJournalLimit(err)
			}
			if ctx.Err() != nil {
				return fail(ctx.Err())
			}
			if toolErr != nil || (out.Failed && !out.Recoverable) || out.Effect == UncertainEffect {
				return fail(ErrTool)
			}
			inference.Messages = append(inference.Messages, providers.Message{Role: "tool", ToolCallID: call.ID, Content: out.Content, ToolFailed: out.Failed})
		}
	}
	return fail(ErrLimit)
}

func validRunWorkerID(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}
