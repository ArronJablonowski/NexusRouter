package app

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/codexbridge"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/policy"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

type taskProvider interface {
	providers.Provider
	Close() error
}
type codexLaunch func(context.Context, codexbridge.LaunchSpec) (taskProvider, error)
type taskProviderOpen func(context.Context) (providers.Provider, func(), error)

// ownedCodexProvider gives rollover and task-final cleanup the same exactly-once
// close boundary. An ambiguous close or panic is memoized and never retried,
// while the private working directory is still removed on every terminal path.
type ownedCodexProvider struct {
	taskProvider
	dir  string
	once sync.Once
	err  error
}

func (p *ownedCodexProvider) Close() error {
	if p == nil {
		return ErrAdmission
	}
	p.once.Do(func() {
		p.err = closeCodexTaskProvider(p.taskProvider)
		_ = os.RemoveAll(p.dir)
	})
	return p.err
}

func closeCodexTaskProvider(provider taskProvider) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrAdmission
		}
	}()
	if provider == nil || (reflect.ValueOf(provider).Kind() == reflect.Pointer && reflect.ValueOf(provider).IsNil()) {
		return ErrAdmission
	}
	return provider.Close()
}

func (p *ownedCodexProvider) CheckContextRollover(ctx context.Context, current, prospective providers.Request) error {
	if p == nil {
		return &providers.Failure{Code: "context_rollover"}
	}
	inspector, ok := p.taskProvider.(interface {
		CheckContextRollover(context.Context, providers.Request, providers.Request) error
	})
	if !ok {
		return &providers.Failure{Code: "context_rollover"}
	}
	return inspector.CheckContextRollover(ctx, current, prospective)
}

// deferredTaskProvider is inert until the runtime starts its first model turn.
// The runtime therefore remains the sole owner of task.started while selected
// execution construction and owned subprocess launch happen only after the
// durable start/route/turn boundaries have accepted the attempt.
type deferredTaskProvider struct {
	mu       sync.Mutex
	open     func(context.Context) (providers.Provider, func(), error)
	provider providers.Provider
	cleanup  func()
	openErr  error
	opened   bool
	closed   bool
}

func newDeferredTaskProvider(open func(context.Context) (providers.Provider, func(), error)) *deferredTaskProvider {
	return &deferredTaskProvider{open: open}
}

func (p *deferredTaskProvider) Models(context.Context) ([]string, error) {
	return nil, &providers.Failure{Code: "adapter_failure"}
}

func (p *deferredTaskProvider) Stream(ctx context.Context, request providers.Request, emit func(providers.Chunk) error) error {
	provider, err := p.acquire(ctx)
	if err != nil {
		return err
	}
	return provider.Stream(ctx, request, emit)
}

func (p *deferredTaskProvider) acquire(ctx context.Context) (providers.Provider, error) {
	if p == nil || ctx == nil {
		return nil, &providers.Failure{Code: "adapter_failure"}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, &providers.Failure{Code: "adapter_failure"}
	}
	if p.opened {
		return p.provider, p.openErr
	}
	p.opened = true
	provider, cleanup, err := invokeTaskProviderOpen(ctx, p.open)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil || nilTaskProvider(provider) {
		safeTaskProviderCleanup(cleanup)
		p.openErr = taskProviderOpenFailure(ctx, err)
		return nil, p.openErr
	}
	p.provider, p.cleanup = provider, cleanup
	return p.provider, nil
}

func (p *deferredTaskProvider) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	cleanup := p.cleanup
	p.cleanup = nil
	p.mu.Unlock()
	safeTaskProviderCleanup(cleanup)
}

func invokeTaskProviderOpen(ctx context.Context, open func(context.Context) (providers.Provider, func(), error)) (provider providers.Provider, cleanup func(), err error) {
	defer func() {
		if recover() != nil {
			provider = nil
			err = &providers.Failure{Code: "adapter_failure"}
		}
	}()
	if open == nil {
		return nil, nil, &providers.Failure{Code: "adapter_failure"}
	}
	return open(ctx)
}

func taskProviderOpenFailure(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	var failure *providers.Failure
	if errors.As(err, &failure) && failure != nil && failure.Retryable && !failure.Partial {
		return &providers.Failure{Code: "unavailable", Retryable: true}
	}
	return &providers.Failure{Code: "adapter_failure"}
}

func nilTaskProvider(provider providers.Provider) bool {
	if provider == nil {
		return true
	}
	value := reflect.ValueOf(provider)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func safeTaskProviderCleanup(cleanup func()) {
	if cleanup == nil {
		return
	}
	defer func() { _ = recover() }()
	cleanup()
}

// prepareTaskProvider performs deterministic policy and message-shape
// admission without constructing the selected adapter or launching a process.
// Its returned opener is invoked only by deferredTaskProvider after the runtime
// has committed the task and turn boundaries.
func prepareTaskProvider(s config.Settings, provider config.Provider, model config.Model, r Request, messages []providers.Message, privacy, key string, purpose providers.Purpose) (taskProviderOpen, func(), error) {
	if purpose == "" {
		purpose = providers.PurposeExecution
	}
	if provider.Kind != "codex_app_server" {
		endpoint := provider.ResolvedEndpoint()
		transport, err := policy.NewTransportWithHeaderTimeout(s.Mode == "local_only" || model.Locality == "local", []string{endpoint}, httpProviderTimeout(provider))
		if err != nil {
			return nil, nil, ErrAdmission
		}
		open := func(ctx context.Context) (providers.Provider, func(), error) {
			adapter, err := providers.Build(ctx, r.providerFactory, providers.Connection{Version: 1, ID: provider.ID, Endpoint: endpoint, Kind: provider.Kind, Purpose: purpose, Timeout: httpProviderTimeout(provider), OllamaThink: provider.OllamaThink, APIKey: key, Transport: transport})
			if err != nil {
				return nil, nil, err
			}
			return adapter, nil, nil
		}
		return open, transport.CloseIdleConnections, nil
	}
	// Explicit continuation imports only validated, completed conversation
	// items. Compaction must come from the resolved canonical continuation path,
	// not merely from request flags. The journal rechecks stored-summary
	// approval transactionally when TaskStarted commits.
	if privacy != "cloud_allowed" || model.Locality != "cloud" || r.LocalRequired || s.Mode == "local_only" || !codexCompactionReady(r) || codexbridge.ValidateInitialMessages(messages) != nil || (r.ContinueTaskID == "" && (len(messages) != 1 || messages[0].Role != "user")) {
		return nil, nil, ErrAdmission
	}
	return func(ctx context.Context) (providers.Provider, func(), error) {
		return openOwnedCodexProvider(ctx, s, provider, model, privacy, r.codexLauncher)
	}, func() {}, nil
}

// Construct only after resource and resolved-session privacy admission and
// supported message-shape checks. Runtime context estimation still follows.
// HTTP factories retain their existing transport-only contract; the subprocess
// has explicit task lifetime ownership instead of masquerading as HTTP.
func openTaskProvider(ctx context.Context, s config.Settings, provider config.Provider, model config.Model, r Request, messages []providers.Message, privacy, key string, purposes ...providers.Purpose) (providers.Provider, func(), error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, nil, ErrAdmission
	}
	purpose := providers.PurposeExecution
	if len(purposes) > 0 {
		purpose = purposes[0]
	}
	open, preparedCleanup, err := prepareTaskProvider(s, provider, model, r, messages, privacy, key, purpose)
	if err != nil {
		return nil, nil, err
	}
	adapter, taskCleanup, err := open(ctx)
	if err != nil {
		preparedCleanup()
		return nil, nil, ErrAdmission
	}
	return adapter, func() {
		safeTaskProviderCleanup(taskCleanup)
		preparedCleanup()
	}, nil
}

func httpProviderTimeout(provider config.Provider) time.Duration {
	if provider.RequestTimeout == "" {
		return 5 * time.Minute
	}
	timeout, _ := config.Duration(provider.RequestTimeout)
	return timeout
}

// The caller admits its own request shape and privacy before this shared
// task-owned launch. Audits invoke it lazily after their durable attempt and
// context checks, never by pretending to be task continuations.
func openOwnedCodexProvider(ctx context.Context, s config.Settings, provider config.Provider, model config.Model, privacy string, launch codexLaunch) (adapter providers.Provider, closeProvider func(), err error) {
	if ctx == nil || ctx.Err() != nil || provider.Kind != "codex_app_server" || model.Locality != "cloud" || privacy != "cloud_allowed" || (s.Mode != "hybrid" && s.Mode != "cloud_only") {
		return nil, nil, ErrAdmission
	}
	dir, err := os.MkdirTemp("", "darwin-codex-task-")
	if err != nil {
		return nil, nil, ErrAdmission
	}
	env := []string{}
	for _, name := range []string{"HOME", "PATH", "TMPDIR", "CODEX_HOME"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	if launch == nil {
		launch = func(ctx context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
			return codexbridge.LaunchChecked(ctx, spec)
		}
	}
	var p taskProvider
	cleanup := func() { _ = os.RemoveAll(dir) }
	defer func() {
		if recover() != nil {
			err = ErrAdmission
		}
		if err != nil {
			cleanup()
			adapter, closeProvider = nil, nil
		}
	}()
	p, err = launch(ctx, codexbridge.LaunchSpec{Executable: provider.Executable, CWD: dir, Model: model.Model, ReasoningEffort: model.ReasoningEffort, Mode: s.Mode, Privacy: privacy, Env: env})
	if p == nil || (reflect.ValueOf(p).Kind() == reflect.Pointer && reflect.ValueOf(p).IsNil()) {
		return nil, nil, ErrAdmission
	}
	owned := &ownedCodexProvider{taskProvider: p, dir: dir}
	cleanup = func() { _ = owned.Close() }
	if err != nil {
		return nil, nil, ErrAdmission
	}
	closeProvider = func() { _ = owned.Close() }
	return owned, closeProvider, nil
}
