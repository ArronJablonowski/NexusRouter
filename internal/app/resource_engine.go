package app

import (
	"context"
	"encoding/json"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/memory"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/skills"
	"github.com/ArronJablonowski/NexusRouter/tools"
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
	return NewServiceWithToolApproval(settings, secret, profiler, store, skillStore, factory, extension, nil)
}

// NewServiceWithToolApproval binds an explicit, trusted operator-review adapter.
// Review authority is process-local and is never inherited by worker children.
func NewServiceWithToolApproval(settings config.Settings, secret func(string) string, profiler resources.Profiler, store memory.Store, skillStore skills.Store, factory providers.Factory, extension *tools.Extension, reviewer tools.ApprovalReviewer) (*Service, error) {
	return NewServiceWithToolControls(settings, secret, profiler, store, skillStore, factory, extension, reviewer, nil)
}

// NewServiceWithToolControls optionally presents proposals and waits for a
// separately submitted durable decision. Reviewer and presenter are exclusive.
func NewServiceWithToolControls(settings config.Settings, secret func(string) string, profiler resources.Profiler, store memory.Store, skillStore skills.Store, factory providers.Factory, extension *tools.Extension, reviewer tools.ApprovalReviewer, presenter tools.ApprovalPresenter) (*Service, error) {
	if (reviewer != nil && presenter != nil) || ((extension.RequiresApproval() || settings.Tools.CreateEnabled || settings.Tools.ReplaceEnabled || settings.Tools.WorkboardWriteEnabled) && reviewer == nil && presenter == nil) {
		return nil, ErrAdmission
	}
	if settings.Validate() != nil {
		return nil, ErrAdmission
	}
	body, err := json.Marshal(settings)
	if err != nil {
		return nil, ErrAdmission
	}
	var snapshot config.Settings
	if json.Unmarshal(body, &snapshot) != nil {
		return nil, ErrAdmission
	}
	settings = snapshot
	constructionSettings := settings
	constructionSettings.NativeHarnesses = nil
	svc, err := NewService(constructionSettings, secret)
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
	svc.toolReviewer = reviewer
	svc.toolPresenter = presenter
	svc.settings = settings
	if err := svc.ConfigureNativeHarnesses(configuredNativeHarnesses(settings), nil); err != nil {
		return nil, err
	}
	return svc, nil
}

func selectMemoryStore(custom, fallback memory.Store) memory.Store {
	if custom != nil {
		return custom
	}
	return fallback
}

// NewServiceWithContextEstimator replaces task context measurement without
// replacing assembly, tool policy, canonical compaction or durable replay.
func NewServiceWithContextEstimator(settings config.Settings, secret func(string) string, profiler resources.Profiler, store memory.Store, skillStore skills.Store, factory providers.Factory, extension *tools.Extension, reviewer tools.ApprovalReviewer, presenter tools.ApprovalPresenter, estimator providers.ContextEstimator) (*Service, error) {
	svc, err := NewServiceWithToolControls(settings, secret, profiler, store, skillStore, factory, extension, reviewer, presenter)
	if err != nil {
		return nil, err
	}
	svc.contextEstimator = estimator
	return svc, nil
}

// NewServiceWithContextEstimatorAndEvaluator additionally installs a trusted,
// provider-neutral evaluation engine. A nil evaluator retains the built-in
// provider-backed reviewer path.
func NewServiceWithContextEstimatorAndEvaluator(settings config.Settings, secret func(string) string, profiler resources.Profiler, store memory.Store, skillStore skills.Store, factory providers.Factory, extension *tools.Extension, reviewer tools.ApprovalReviewer, presenter tools.ApprovalPresenter, estimator providers.ContextEstimator, evaluator evaluation.Evaluator) (*Service, error) {
	return NewServiceWithContextEstimatorEvaluatorAndEventSink(settings, secret, profiler, store, skillStore, factory, extension, reviewer, presenter, estimator, evaluator, nil)
}

// NewServiceWithContextEstimatorEvaluatorAndEventSink installs all trusted SDK
// extensions before the service is shared. The sink observes newly committed
// runtime events; it is not a replay subscription.
func NewServiceWithContextEstimatorEvaluatorAndEventSink(settings config.Settings, secret func(string) string, profiler resources.Profiler, store memory.Store, skillStore skills.Store, factory providers.Factory, extension *tools.Extension, reviewer tools.ApprovalReviewer, presenter tools.ApprovalPresenter, estimator providers.ContextEstimator, evaluator evaluation.Evaluator, sink runtime.EventSink) (*Service, error) {
	svc, err := NewServiceWithContextEstimator(settings, secret, profiler, store, skillStore, factory, extension, reviewer, presenter, estimator)
	if err != nil {
		return nil, err
	}
	if evaluator != nil {
		if _, err := evaluation.DescribeEvaluator(evaluator); err != nil {
			return nil, ErrAdmission
		}
	}
	svc.evaluator = evaluator
	if err := installEventSink(svc, sink); err != nil {
		return nil, err
	}
	return svc, nil
}
