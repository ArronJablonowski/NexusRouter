package runtime

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"time"

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
	TaskID, SessionID, ProviderID string
	Inference                     providers.Request
	MaxTurns                      int
	MaxOutputBytes                int
}
type Result struct {
	Text  string
	Turns int
}
type Loop struct {
	Provider providers.Provider
	Journal  Journal
	Tools    ToolExecutor
}

var (
	ErrInvalidRun  = errors.New("invalid runtime request")
	ErrProvider    = errors.New("model turn failed")
	ErrProtocol    = errors.New("invalid model stream")
	ErrLimit       = errors.New("runtime budget exhausted")
	ErrTool        = errors.New("tool execution failed or denied")
	ErrPersistence = errors.New("runtime persistence failed; inspect durable state before retry")
)

// Run starts a new durable task. It does not resume or silently retry existing
// task IDs. Completion means the loop ended, not that output passed evaluation.
func (l Loop) Run(ctx context.Context, r RunRequest) (Result, error) {
	if l.Provider == nil || l.Journal == nil || r.TaskID == "" || r.SessionID == "" || r.ProviderID == "" || r.Inference.Model == "" || len(r.Inference.Messages) == 0 || r.MaxTurns < 1 || r.MaxTurns > 1000 || r.MaxOutputBytes < 1 || r.MaxOutputBytes > 16<<20 {
		return Result{}, ErrInvalidRun
	}
	// Snapshot nested caller-owned data before the provider receives it.
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
		if l.Journal.Append(ctx, seq, e) != nil {
			return ErrPersistence
		}
		seq++
		return nil
	}
	if err := persist(ctx, TaskStarted, Data{Messages: inference.Messages, ModelID: inference.Model, ProviderID: r.ProviderID}); err != nil {
		return Result{}, err
	}
	result := Result{}
	fail := func(cause error) (Result, error) {
		kind := TaskFailed
		code := "execution_failed"
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
	used := 0
	seen := map[string]bool{}
	for n := 0; n < r.MaxTurns; n++ {
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		turn = rand.Text()
		attempt = rand.Text()
		result.Turns++
		if err := persist(ctx, TurnStarted, Data{ModelID: inference.Model, ProviderID: r.ProviderID}); err != nil {
			return result, err
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
		if callbackErr != nil {
			return fail(callbackErr)
		}
		if err != nil {
			return fail(ErrProvider)
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
		if err := persist(ctx, TurnCompleted, Data{Text: text.String(), ToolCalls: calls, Usage: usage, FinishReason: reason}); err != nil {
			return result, err
		}
		if len(calls) == 0 {
			if err := persist(ctx, TaskCompleted, Data{}); err != nil {
				return result, err
			}
			result.Text = text.String()
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
			out, toolErr := l.Tools.Execute(ctx, call)
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
			if toolErr != nil || out.Effect == UncertainEffect {
				return fail(ErrTool)
			}
			inference.Messages = append(inference.Messages, providers.Message{Role: "tool", ToolCallID: call.ID, Content: out.Content})
		}
	}
	return fail(ErrLimit)
}
