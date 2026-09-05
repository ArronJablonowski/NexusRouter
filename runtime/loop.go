package runtime

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"darwinrouter/evaluation"
	"darwinrouter/providers"
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

type ToolResult struct {
	Content string
	Effect  Effect
}
type RunRequest struct {
	Compaction    *ContextCompaction
	Validation    string
	RetryOfTaskID string
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
	Provider providers.Provider
	Journal  Journal
	Tools    ToolExecutor
	// ValidationText supplies the host's persisted/delivered view (for example
	// secret redaction). It must match the journal and output adapter exactly.
	// It is trusted host code, never a model-supplied transformation.
	ValidationText func(string) string
}

var (
	ErrInvalidRun    = errors.New("invalid runtime request")
	ErrProvider      = errors.New("model turn failed")
	ErrProtocol      = errors.New("invalid model stream")
	ErrLimit         = errors.New("runtime budget exhausted")
	ErrEmptyOutput   = errors.New("required final text is empty")
	ErrInvalidOutput = errors.New("final output failed requested validation")
	ErrTool          = errors.New("tool execution failed or denied")
	ErrPersistence   = errors.New("runtime persistence failed; inspect durable state before retry")
)

// Run starts a new durable task. It does not resume or silently retry existing
// task IDs. Completion means the loop ended, not that output passed evaluation.
func (l Loop) Run(ctx context.Context, r RunRequest) (returned Result, runErr error) {
	if r.Compaction != nil && r.Compaction.Validate(r.ParentTaskID) != nil {
		return Result{}, ErrInvalidRun
	}
	if r.MaxContextTokens < 0 || (r.Validation != "" && r.Validation != "go_source") || (r.Validation != "" && !r.RequireText) {
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
		if err := l.Journal.Append(ctx, seq, e); err != nil {
			if errors.Is(err, ErrCancellationRequested) {
				return ErrCancellationRequested
			}
			return ErrPersistence
		}
		seq++
		return nil
	}
	if err := persist(ctx, TaskStarted, Data{Compaction: compaction, Validation: r.Validation, RetryOfTaskID: r.RetryOfTaskID, Messages: inference.Messages, ModelID: inference.Model, ProviderID: r.ProviderID, ParentTaskID: r.ParentTaskID, Privacy: r.Privacy, Domain: r.Domain, Profile: r.Profile}); err != nil {
		return Result{}, err
	}
	result := Result{}
	// The cancellation gate guarantees no append occurred. Unlike an ambiguous
	// storage failure, it is safe to append one bounded cancellation terminal.
	defer func() {
		if !errors.Is(runErr, ErrCancellationRequested) || errors.Is(runErr, ErrPersistence) {
			return
		}
		returned.Retryable = false
		terminal, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := persist(terminal, TaskCanceled, Data{Code: "canceled"}); err != nil {
			runErr = errors.Join(context.Canceled, err)
			return
		}
		runErr = context.Canceled
	}()
	fail := func(cause error) (Result, error) {
		kind := TaskFailed
		code := "execution_failed"
		if result.Retryable && errors.Is(cause, ErrProvider) {
			code = "provider_retryable_no_output"
		}
		if errors.Is(cause, ErrLimit) {
			code = "budget_exhausted"
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
			return result, err
		}
		return result, cause
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
		if r.MaxContextTokens > 0 {
			estimate, err := providers.EstimateContext(inference)
			if err != nil || estimate > r.MaxContextTokens {
				return fail(ErrLimit)
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
		if errors.Is(callbackErr, ErrCancellationRequested) {
			return result, callbackErr
		}
		if callbackErr != nil {
			return fail(callbackErr)
		}
		if err != nil {
			var providerFailure *providers.Failure
			safe := n == 0 && text.Len() == 0 && len(calls) == 0 && ctx.Err() == nil && errors.As(err, &providerFailure) && providerFailure.Retryable && !providerFailure.Partial
			result.Retryable = safe
			out, failErr := fail(ErrProvider)
			out.Retryable = safe && errors.Is(failErr, ErrProvider)
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
		if usage == nil {
			usageComplete = false
		} else {
			totalUsage.InputTokens += usage.InputTokens
			totalUsage.OutputTokens += usage.OutputTokens
		}
		if len(calls) == 0 {
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
			if err := persist(ctx, ToolStarted, Data{ToolCallID: call.ID, ToolName: call.Name, Effect: UncertainEffect}); err != nil {
				return result, err
			}
			out := ToolResult{Effect: NoEffect}
			toolErr := ctx.Err()
			if toolErr == nil {
				out, toolErr = l.Tools.Execute(ctx, call)
			}
			if out.Effect != NoEffect && out.Effect != ConfirmedEffect && out.Effect != UncertainEffect {
				out.Effect = UncertainEffect
				toolErr = ErrTool
			}
			used += len(out.Content)
			if used > r.MaxOutputBytes {
				out.Content = ""
				toolErr = ErrLimit
			}
			code := ""
			if toolErr != nil {
				code = "tool_failed"
			}
			terminal, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			err := persist(terminal, ToolCompleted, Data{ToolCallID: call.ID, ToolName: call.Name, Effect: out.Effect, Text: out.Content, Code: code})
			cancel()
			if err != nil {
				return result, err
			}
			if ctx.Err() != nil {
				return fail(ctx.Err())
			}
			if toolErr != nil || out.Effect == UncertainEffect {
				return fail(ErrTool)
			}
			inference.Messages = append(inference.Messages, providers.Message{Role: "tool", ToolCallID: call.ID, Content: out.Content})
		}
	}
	return fail(ErrLimit)
}
