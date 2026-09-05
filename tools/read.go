package tools

import (
	"context"
	"encoding/json"
	"sync"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

// ReadAuthority retains a shared resource lease until its cooperative callback
// has returned. It must reject lost ownership and must never retry the callback.
// A nil Executor.Reader retains the legacy standalone cooperative execution path.
type ReadAuthority interface {
	ExecuteRead(context.Context, runtime.ToolExecution, string, func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error)
}

func (e Executor) readOwned(ctx context.Context, x runtime.ToolExecution, t entry, arguments json.RawMessage) (out runtime.ToolResult, err error) {
	var mu sync.Mutex
	closed, started := false, false
	var result runtime.ToolResult
	var handlerErr error
	invoke := func(leaseCtx context.Context) (returned runtime.ToolResult, returnedErr error) {
		mu.Lock()
		defer mu.Unlock()
		if closed || started || leaseCtx == nil {
			return runtime.ToolResult{Effect: runtime.NoEffect}, ErrDenied
		}
		started = true
		result = runtime.ToolResult{Effect: runtime.UncertainEffect}
		defer func() {
			if recover() != nil {
				result, handlerErr = runtime.ToolResult{Effect: runtime.UncertainEffect}, ErrExecution
				returned, returnedErr = result, handlerErr
			}
		}()
		runCtx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(leaseCtx, cancel)
		defer stop()
		defer cancel()
		if ctx.Err() != nil || leaseCtx.Err() != nil {
			handlerErr = ErrExecution
			return result, handlerErr
		}
		result, handlerErr = t.Handler(runCtx, arguments)
		validFailure := handlerErr == nil && result.Failed && result.Effect == runtime.NoEffect && len(result.Content) <= 1<<20 && utf8.ValidString(result.Content)
		if validFailure && ctx.Err() != nil {
			// Preserve only a diagnostic candidate. The gate must independently
			// verify ownership and return this exact result with cancellation.
			result.Recoverable = false
		} else if handlerErr != nil || runCtx.Err() != nil || leaseCtx.Err() != nil || result.Effect != runtime.NoEffect || (result.Recoverable && !result.Failed) || len(result.Content) > 1<<20 || !utf8.ValidString(result.Content) {
			result, handlerErr = runtime.ToolResult{Effect: runtime.UncertainEffect}, ErrExecution
		}
		return result, handlerErr
	}
	defer func() {
		panicked := recover() != nil
		mu.Lock()
		defer mu.Unlock()
		closed = true
		expected := result
		expected.Recoverable = false
		if !panicked && started && handlerErr == nil && ctx.Err() != nil && err == ctx.Err() && expected.Failed && expected.Effect == runtime.NoEffect && out == expected {
			out, err = expected, ctx.Err()
			return
		}
		if panicked || (started && (err != nil || handlerErr != nil || ctx.Err() != nil)) {
			out, err = runtime.ToolResult{Effect: runtime.UncertainEffect}, ErrExecution
			return
		}
		if !started {
			if err != nil && out.Effect == runtime.UncertainEffect {
				out, err = runtime.ToolResult{Effect: runtime.UncertainEffect}, ErrExecution
				return
			}
			out, err = runtime.ToolResult{Effect: runtime.NoEffect}, ErrDenied
			return
		}
		out, err = result, handlerErr
	}()
	// The gate gets a private identity copy, not mutable handler arguments.
	x.Call.Arguments = append(json.RawMessage(nil), arguments...)
	return e.Reader.ExecuteRead(ctx, x, t.Scope, invoke)
}
