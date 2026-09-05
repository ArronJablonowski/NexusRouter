package app

import (
	"context"
	"os"
	"reflect"
	"sync"

	"github.com/ArronJablonowski/DarwinRouter/internal/codexbridge"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/policy"
	"github.com/ArronJablonowski/DarwinRouter/providers"
)

type taskProvider interface {
	providers.Provider
	Close() error
}
type codexLaunch func(context.Context, codexbridge.LaunchSpec) (taskProvider, error)

// Construct only after resource and resolved-session privacy admission and
// supported message-shape checks. Runtime context estimation still follows.
// HTTP factories retain their existing transport-only contract; the subprocess
// has explicit task lifetime ownership instead of masquerading as HTTP.
func openTaskProvider(ctx context.Context, s config.Settings, provider config.Provider, model config.Model, r Request, messages []providers.Message, privacy, key string) (providers.Provider, func(), error) {
	if provider.Kind != "codex_app_server" {
		tr, err := policy.NewTransport(s.Mode == "local_only" || model.Locality == "local", []string{provider.Endpoint})
		if err != nil {
			return nil, nil, ErrAdmission
		}
		p, err := providers.Build(ctx, r.providerFactory, providers.Connection{Version: 1, ID: provider.ID, Endpoint: provider.Endpoint, Kind: provider.Kind, APIKey: key, Transport: tr})
		if err != nil {
			tr.CloseIdleConnections()
			return nil, nil, ErrAdmission
		}
		return p, tr.CloseIdleConnections, nil
	}
	// Explicit continuation imports only validated, completed conversation items.
	// Fresh tasks still cannot acquire extra memory/skill roles implicitly;
	// compaction and reviewed-summary imports await separate qualification.
	if ctx.Err() != nil || privacy != "cloud_allowed" || model.Locality != "cloud" || r.LocalRequired || s.Mode == "local_only" || r.Compaction != nil || r.SummaryAttemptID != "" || codexbridge.ValidateInitialMessages(messages) != nil || (r.ContinueTaskID == "" && (len(messages) != 1 || messages[0].Role != "user")) {
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
	launch := r.codexLauncher
	if launch == nil {
		launch = func(ctx context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
			return codexbridge.LaunchChecked(ctx, spec)
		}
	}
	p, err := launch(ctx, codexbridge.LaunchSpec{Executable: provider.Executable, CWD: dir, Model: model.Model, Mode: s.Mode, Privacy: privacy, Env: env})
	if err != nil || p == nil || (reflect.ValueOf(p).Kind() == reflect.Pointer && reflect.ValueOf(p).IsNil()) {
		if p != nil && !(reflect.ValueOf(p).Kind() == reflect.Pointer && reflect.ValueOf(p).IsNil()) {
			_ = p.Close()
		}
		_ = os.RemoveAll(dir) // Only this task's newly-created private directory.
		return nil, nil, ErrAdmission
	}
	var once sync.Once
	closeProvider := func() { once.Do(func() { _ = p.Close(); _ = os.RemoveAll(dir) }) }
	return p, closeProvider, nil
}
