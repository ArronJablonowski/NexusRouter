package runtime

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

// invokeProviderStream contains a provider implementation panic at the
// provider-neutral runtime boundary. A callback error names the durable
// boundary more precisely than a provider panic and therefore retains
// precedence. Panics are never retryable; committed output makes the failure
// explicitly partial.
func invokeProviderStream(ctx context.Context, provider providers.Provider, request providers.Request, emit func(providers.Chunk) error, committedOutput *bool) (err error) {
	var callbackErr error
	defer func() {
		panicked := recover() != nil
		if callbackErr != nil {
			err = callbackErr
			return
		}
		if !panicked {
			return
		}
		if ctx != nil && ctx.Err() != nil {
			err = ctx.Err()
			return
		}
		partial := committedOutput != nil && *committedOutput
		err = &providers.Failure{Code: "adapter_failure", Partial: partial}
	}()
	return provider.Stream(ctx, request, func(chunk providers.Chunk) error {
		if callbackErr != nil {
			return callbackErr
		}
		callbackErr = invokeProviderEmit(emit, chunk)
		return callbackErr
	})
}

// invokeProviderEmit keeps a panic in DarwinRouter's own provider callback
// distinct from an adapter panic. The callback includes durable appends, so a
// panic leaves commit state ambiguous and must fail closed as persistence.
func invokeProviderEmit(emit func(providers.Chunk) error, chunk providers.Chunk) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrPersistence
		}
	}()
	return emit(chunk)
}

// invokeJournalAppend contains replaceable journal implementations at their
// narrow interface boundary. A panic cannot prove whether a commit happened,
// so callers must receive persistence ambiguity and must not invent a terminal.
func invokeJournalAppend(ctx context.Context, journal Journal, sequence int64, event Event) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrPersistence
		}
	}()
	return journal.Append(ctx, sequence, event)
}

// invokeJournalContextCompaction contains an atomic lifecycle-backed append at
// the same ambiguity boundary as an ordinary journal append. A panic cannot
// prove whether either half committed, so the runtime must not retry, append a
// terminal, or dispatch another provider turn.
func invokeJournalContextCompaction(ctx context.Context, journal ContextCompactionJournal, sequence int64, event Event, plan ContextCompactionPlan) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrPersistence
		}
	}()
	return journal.AppendContextCompaction(ctx, sequence, event, plan)
}

// invokeTool contains both legacy and scoped executor panics. Because arbitrary
// executor code may have performed an effect before panicking, the only safe
// disposition is an empty, uncertain failure. The loop persists that evidence
// before terminating and never retries the call.
func invokeTool(ctx context.Context, executor ToolExecutor, execution ToolExecution) (out ToolResult, err error) {
	defer func() {
		if recover() != nil {
			out = ToolResult{Effect: UncertainEffect}
			err = ErrTool
		}
	}()
	if scoped, ok := executor.(ScopedToolExecutor); ok {
		owned := execution
		owned.Call.Arguments = append([]byte(nil), execution.Call.Arguments...)
		return scoped.ExecuteScoped(ctx, owned)
	}
	return executor.Execute(ctx, execution.Call)
}
