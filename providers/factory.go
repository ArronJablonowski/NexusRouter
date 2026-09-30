package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

// Connection is trusted host configuration, never model-selected authority.
// APIKey is sensitive. Factories must use Transport for all provider traffic,
// reject redirects, honor contexts, and never log credentials or retain the
// connection beyond the adapter's operation. Arbitrary Go code is not sandboxed.
type Connection struct {
	Version            int
	ID, Endpoint, Kind string
	// Purpose distinguishes pre-task discovery/health construction from the
	// task-owned execution adapter. Empty retains the version-one execution
	// default for behavioral compatibility.
	Purpose   Purpose
	Timeout   time.Duration
	APIKey    string            `json:"-"`
	Transport http.RoundTripper `json:"-"`
}

type Purpose string

const (
	PurposeExecution Purpose = "execution"
	PurposeDiscovery Purpose = "discovery"
	PurposeHealth    Purpose = "health"
	PurposeAuxiliary Purpose = "auxiliary"
)

func validPurpose(purpose Purpose) bool {
	return purpose == PurposeExecution || purpose == PurposeDiscovery || purpose == PurposeHealth || purpose == PurposeAuxiliary
}

// Factory creates a replaceable provider engine. Returned adapters must obey
// Provider's sequential, synchronous callback contract and be safe for their
// intended concurrent use. The host owns their lifetime; NexusRouter never calls
// Close. Transport lifetime remains controlled by the application.
type Factory interface {
	Build(context.Context, Connection) (Provider, error)
}

// Build preserves built-in behavior when factory is nil. Custom construction
// has a cooperative three-second deadline; no detached goroutines are started.
func Build(ctx context.Context, factory Factory, connection Connection) (out Provider, err error) {
	defer func() {
		if recover() != nil {
			out, err = nil, adapterFailure(false)
		}
	}()
	if connection.Purpose == "" {
		connection.Purpose = PurposeExecution
	}
	if ctx == nil || connection.Version != 1 || !factoryLabel(connection.ID) || !validPurpose(connection.Purpose) {
		return nil, adapterFailure(false)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	timeout := connection.Timeout
	if timeout == 0 {
		timeout = defaultRequestTimeout
	}
	builtin, err := NewHTTPWithTimeout(connection.Endpoint, connection.Kind, connection.APIKey, connection.Transport, timeout)
	if err != nil {
		return nil, adapterFailure(false)
	}
	if factory == nil {
		return builtin, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	p, err := factory.Build(ctx, connection)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || nilProvider(p) {
		return nil, adapterFailure(false)
	}
	return guardedProvider{provider: p, timeout: timeout}, nil
}

func nilProvider(p Provider) bool {
	if p == nil {
		return true
	}
	v := reflect.ValueOf(p)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}

type guardedProvider struct {
	provider Provider
	timeout  time.Duration
}

func (p guardedProvider) InstalledModels(ctx context.Context) (out []InstalledModel, err error) {
	defer func() {
		if recover() != nil {
			out, err = nil, adapterFailure(false)
		}
	}()
	if ctx == nil {
		return nil, adapterFailure(false)
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	inventory, ok := p.provider.(ModelInventoryProvider)
	if !ok {
		names, modelsErr := p.provider.Models(ctx)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if modelsErr != nil || len(names) > 4096 {
			return nil, normalizeAdapterError(modelsErr, false)
		}
		models := make([]InstalledModel, 0, len(names))
		for _, name := range names {
			models = append(models, InstalledModel{Name: name})
		}
		return models, nil
	}
	models, err := inventory.InstalledModels(ctx)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || len(models) > 4096 {
		return nil, normalizeAdapterError(err, false)
	}
	return append([]InstalledModel(nil), models...), nil
}

func (p guardedProvider) Models(ctx context.Context) (out []string, err error) {
	defer func() {
		if recover() != nil {
			out, err = nil, adapterFailure(false)
		}
	}()
	if ctx == nil {
		return nil, adapterFailure(false)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	names, err := p.provider.Models(ctx)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, normalizeAdapterError(err, false)
	}
	if len(names) > 4096 {
		return nil, adapterFailure(false)
	}
	seen := map[string]bool{}
	for _, name := range names {
		if !factoryLabel(name) || seen[name] {
			return nil, adapterFailure(false)
		}
		seen[name] = true
	}
	return append([]string(nil), names...), nil
}

func (p guardedProvider) Stream(ctx context.Context, input Request, emit func(Chunk) error) (err error) {
	partial, done := false, false
	var callbackErr error
	defer func() {
		if recover() != nil {
			err = adapterFailure(partial)
		}
		if callbackErr != nil {
			err = callbackErr
		}
	}()
	if ctx == nil || emit == nil {
		return adapterFailure(false)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	callerCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	if ValidateMessages(input.Messages) != nil || !factoryLabel(input.Model) || !validMaxOutputTokens(input.MaxOutputTokens) || input.ContextTokens < 0 || input.ContextTokens > MaxOutputTokens {
		return adapterFailure(false)
	}
	body, err := json.Marshal(input)
	if err != nil || len(body) > 8<<20 {
		return adapterFailure(false)
	}
	var request Request
	if json.Unmarshal(body, &request) != nil {
		return adapterFailure(false)
	}
	if input.JSONSchema == nil {
		request.JSONSchema = nil
	}
	used := 0
	err = p.provider.Stream(ctx, request, func(chunk Chunk) error {
		if callbackErr != nil {
			return callbackErr
		}
		if ctx.Err() != nil {
			callbackErr = ctx.Err()
			return callbackErr
		}
		if done || !utf8.ValidString(chunk.Text) || len(chunk.Text) > (16<<20)-used {
			callbackErr = adapterFailure(partial)
			return callbackErr
		}
		if chunk.Done {
			switch chunk.FinishReason {
			case "stop", "length", "content_filter", "tool_calls", "function_call":
			default:
				callbackErr = adapterFailure(partial)
				return callbackErr
			}
		}
		used += len(chunk.Text)
		if chunk.ToolCall != nil {
			call := *chunk.ToolCall
			if !factoryLabel(call.ID) || !factoryLabel(call.Name) || len(call.Arguments) > (16<<20)-used || !jsonObject(call.Arguments) {
				callbackErr = adapterFailure(partial)
				return callbackErr
			}
			used += len(call.Arguments)
			call.Arguments = append(json.RawMessage(nil), call.Arguments...)
			chunk.ToolCall = &call
		}
		if chunk.Usage != nil {
			usage := *chunk.Usage
			if usage.InputTokens < 0 || usage.OutputTokens < 0 {
				callbackErr = adapterFailure(partial)
				return callbackErr
			}
			chunk.Usage = &usage
		}
		partial = partial || chunk.Text != "" || chunk.ToolCall != nil
		done = chunk.Done
		callbackErr = emit(chunk)
		return callbackErr
	})
	if callerCtx.Err() != nil {
		return callerCtx.Err()
	}
	// A provider request timeout is recoverable only at the runtime's safe
	// first-turn boundary. Never hide a persistence/callback failure behind it.
	if ctx.Err() == context.DeadlineExceeded && (callbackErr == nil || callbackErr == context.DeadlineExceeded) {
		callbackErr = nil
		return errors.Join(context.DeadlineExceeded, &Failure{Code: "request_timeout", Partial: partial})
	}
	if callbackErr != nil {
		return callbackErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return normalizeAdapterError(err, partial)
	}
	if !done {
		return adapterFailure(partial)
	}
	return nil
}

func factoryLabel(s string) bool {
	return len(s) > 0 && len(s) <= 256 && strings.TrimSpace(s) == s && utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool { return r < 32 || r == 127 })
}

func adapterFailure(partial bool) *Failure {
	return &Failure{Code: "adapter_failure", Partial: partial}
}

func normalizeAdapterError(err error, partial bool) (out error) {
	defer func() {
		if recover() != nil {
			out = adapterFailure(partial)
		}
	}()
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var f *Failure
	if errors.As(err, &f) && f != nil {
		switch f.Code {
		case "transport", "rate_limit", "unavailable":
			return &Failure{Code: f.Code, Partial: partial || f.Partial, Retryable: f.Retryable && !partial && !f.Partial}
		case "request_timeout", "invalid_request", "context_overflow", "http_error", "authentication", "invalid_response", "invalid_conversation", "invalid_tool_schema", "invalid_role", "unpaired_tool_result", "invalid_tool_arguments", "invalid_schema", "incomplete_or_invalid_stream", "refusal", "invalid_stream", "invalid_tool_call", "invalid_usage", "incomplete_stream":
			return &Failure{Code: f.Code, Partial: partial || f.Partial}
		}
	}
	return adapterFailure(partial)
}
