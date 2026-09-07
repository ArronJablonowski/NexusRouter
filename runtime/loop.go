package runtime

import (
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
	SkillContext       *SkillContextUse
	SubmissionID       string
	Compaction         *ContextCompaction
	Validation         string
	RetryOfTaskID      string
	RouteEstimatedCost *float64
	// RequireText applies only to a final answer, never an intermediate tool
	// proposal. Non-text host workflows may leave this false explicitly.
	RequireText                   bool
	Domain, Profile               string
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
	ErrPersistence     = errors.New("runtime persistence failed; inspect durable state before retry")
)

// Run starts a new durable task. It does not resume or silently retry existing
// task IDs. Completion means the loop ended, not that output passed evaluation.
func (l Loop) Run(ctx context.Context, r RunRequest) (returned Result, runErr error) {
	if r.SkillContext.Validate() != nil {
		return Result{}, ErrInvalidRun
	}
	skillContext := r.SkillContext.Clone()
	if r.Compaction != nil && r.Compaction.Validate(r.ParentTaskID) != nil {
		return Result{}, ErrInvalidRun
	}
	if r.MaxContextTokens < 0 || (l.ContextEstimator != nil && r.MaxContextTokens == 0) || (r.Validation != "" && r.Validation != "go_source") || (r.Validation != "" && !r.RequireText) {
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
	// A nil RawMessage round-trips through JSON as the literal bytes "null".
	// Preserve absence so adapters do not interpret it as a requested schema.
	if len(r.Inference.JSONSchema) == 0 {
		inference.JSONSchema = nil
	}
	seq := int64(0)
	turn := ""
	attempt := ""
	persist := func(ctx context.Context, k Kind, d Data) error {
		e := Event{Version: 1, ID: rand.Text(), TaskID: r.TaskID, SessionID: r.SessionID, CorrelationID: r.TaskID, Sequence: seq + 1, Time: time.Now().UTC(), Kind: k, TurnID: turn, AttemptID: attempt, Data: d}
		if k == RouteSelected {
			e.RouteID = rand.Text()
		}
		if k == SteeringApplied {
			e.TurnID, e.AttemptID = "", ""
		}
		if err := l.Journal.Append(ctx, seq, e); err != nil {
			if errors.Is(err, ErrSteeringPending) {
				return ErrSteeringPending
			}
			if errors.Is(err, ErrExecutionLeaseLost) {
				return ErrExecutionLeaseLost
			}
			if errors.Is(err, ErrCancellationRequested) {
				return ErrCancellationRequested
			}
			return ErrPersistence
		}
		seq++
		return nil
	}
	var routeEstimatedCost *float64
	if r.RouteEstimatedCost != nil {
		cost := *r.RouteEstimatedCost
		routeEstimatedCost = &cost
	}
	if err := persist(ctx, TaskStarted, Data{SkillContext: skillContext, SubmissionID: r.SubmissionID, Compaction: compaction, Validation: r.Validation, RetryOfTaskID: r.RetryOfTaskID, RouteEstimatedCost: routeEstimatedCost, Messages: inference.Messages, ModelID: inference.Model, ProviderID: r.ProviderID, ParentTaskID: r.ParentTaskID, Privacy: r.Privacy, Domain: r.Domain, Profile: r.Profile}); err != nil {
		return Result{}, err
	}
	result := Result{}
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
			candidate := inference
			candidate.Messages = append(append([]providers.Message(nil), inference.Messages...), providers.Message{Role: "user", Content: message.Text})
			body, encodeErr := json.Marshal(candidate.Messages)
			if encodeErr != nil || len(body) > 4<<20 {
				return applied, ErrLimit
			}
			if r.MaxContextTokens > 0 {
				estimate, estimateErr := providers.EstimateWith(ctx, l.ContextEstimator, candidate)
				if estimateErr != nil {
					return applied, providers.ErrContextEstimate
				}
				if estimate > r.MaxContextTokens {
					return applied, ErrContextOverflow
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
	steeringFailure := func(err error) (Result, error) {
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
	seen := map[string]bool{}
	for n := 0; n < r.MaxTurns; n++ {
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		if _, err := drain(); err != nil {
			return steeringFailure(err)
		}
		if body, err := json.Marshal(inference.Messages); err != nil || len(body) > 4<<20 {
			return fail(ErrLimit)
		}
		if r.MaxContextTokens > 0 {
			estimate, err := providers.EstimateWith(ctx, l.ContextEstimator, inference)
			if err != nil {
				return fail(providers.ErrContextEstimate)
			}
			if estimate > r.MaxContextTokens {
				return fail(ErrContextOverflow)
			}
		}
		turn = rand.Text()
		attempt = rand.Text()
		result.Turns++
		if err := persist(ctx, TurnStarted, Data{ModelID: inference.Model, ProviderID: r.ProviderID}); err != nil {
			return result, err
		}
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		var text strings.Builder
		calls := []providers.ToolCall{}
		var usage *providers.Usage
		done := false
		reason := ""
		var callbackErr error
		err := l.Provider.Stream(ctx, inference, func(c providers.Chunk) error {
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
					used += len(c.ToolCall.Arguments) + len(c.ToolCall.Name) + len(c.ToolCall.ID)
				}
				if used > r.MaxOutputBytes {
					return ErrLimit
				}
				if c.Text != "" {
					text.WriteString(c.Text)
					if err := persist(ctx, ModelDelta, Data{Text: c.Text}); err != nil {
						return err
					}
					if ctx.Err() != nil {
						return ctx.Err()
					}
				}
				if c.ToolCall != nil {
					call := *c.ToolCall
					var object map[string]json.RawMessage
					if call.ID == "" || call.Name == "" || seen[call.ID] || len(calls) >= 128 || json.Unmarshal(call.Arguments, &object) != nil || object == nil {
						return ErrProtocol
					}
					seen[call.ID] = true
					call.Arguments = append(json.RawMessage(nil), call.Arguments...)
					calls = append(calls, call)
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
		})
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
			return result, err
		}
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		if usage == nil || usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.InputTokens > math.MaxInt64-totalUsage.InputTokens || usage.OutputTokens > math.MaxInt64-totalUsage.OutputTokens {
			usageComplete = false
		} else {
			totalUsage.InputTokens += usage.InputTokens
			totalUsage.OutputTokens += usage.OutputTokens
		}
		if len(calls) == 0 {
			inference.Messages = append(inference.Messages, providers.Message{Role: "assistant", Content: text.String()})
			if applied, err := drain(); err != nil {
				return steeringFailure(err)
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
					return result, err
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
					return result, err
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
						return steeringFailure(drainErr)
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
				return result, err
			}
			out := ToolResult{Effect: NoEffect}
			toolErr := ctx.Err()
			if toolErr == nil {
				if scoped, ok := l.Tools.(ScopedToolExecutor); ok {
					// The executor owns its argument bytes, not the conversation's.
					owned := call
					owned.Arguments = append(json.RawMessage(nil), call.Arguments...)
					out, toolErr = scoped.ExecuteScoped(ctx, ToolExecution{TaskID: r.TaskID, SessionID: r.SessionID, TurnID: turn, AttemptID: attempt, Call: owned})
				} else {
					out, toolErr = l.Tools.Execute(ctx, call)
				}
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
				return result, err
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
