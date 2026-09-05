package app

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/memory"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"github.com/ArronJablonowski/DarwinRouter/skills"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

// NewServiceWithProfiler explicitly replaces measurement, not admission or
// resource accounting. The engine is trusted code and must honor cancellation.
// An injected engine is a manual measurement source when auto_profile is false.
func NewServiceWithProfiler(settings config.Settings, secret func(string) string, profiler resources.Profiler) (*Service, error) {
	return NewServiceWithEngines(settings, secret, profiler, nil)
}

// NewServiceWithEngines installs trusted process-local dependencies. Memory
// retrieval still requires explicit configured scope, privacy and size limits.
func NewServiceWithEngines(settings config.Settings, secret func(string) string, profiler resources.Profiler, store memory.Store) (*Service, error) {
	return NewServiceWithStores(settings, secret, profiler, store, nil)
}

// NewServiceWithStores additionally replaces procedural-skill retrieval. Store
// lifecycle and activation validation remain the embedding host's responsibility.
func NewServiceWithStores(settings config.Settings, secret func(string) string, profiler resources.Profiler, store memory.Store, skillStore skills.Store) (*Service, error) {
	return NewServiceWithProviderFactory(settings, secret, profiler, store, skillStore, nil)
}

// NewServiceWithProviderFactory installs a trusted process-local provider engine.
// It must use the supplied policy transport and honor context cancellation.
// Existing admission, resource accounting and output validation remain active.
func NewServiceWithProviderFactory(settings config.Settings, secret func(string) string, profiler resources.Profiler, store memory.Store, skillStore skills.Store, factory providers.Factory) (*Service, error) {
	return NewServiceWithToolExtension(settings, secret, profiler, store, skillStore, factory, nil)
}

// NewServiceWithToolExtension installs an immutable trusted read-only tool
// catalog. It does not enable filesystem tools or grant tools to child workers.
func NewServiceWithToolExtension(settings config.Settings, secret func(string) string, profiler resources.Profiler, store memory.Store, skillStore skills.Store, factory providers.Factory, extension *tools.Extension) (*Service, error) {
	svc, err := NewService(settings, secret)
	if err != nil {
		return nil, err
	}
	if profiler != nil {
		svc.profile = func(ctx context.Context) (resources.Snapshot, error) {
			return resources.MeasureSnapshot(ctx, profiler)
		}
	}
	svc.memoryStore = store
	svc.skillStore = skillStore
	svc.providerFactory = factory
	svc.toolExtension = extension
	return svc, nil
}

func selectMemoryStore(custom, fallback memory.Store) memory.Store {
	if custom != nil {
		return custom
	}
	return fallback
}
