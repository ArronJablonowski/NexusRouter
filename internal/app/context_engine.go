package app

import (
	"context"
	"reflect"

	"github.com/ArronJablonowski/DarwinRouter/contextengine"
	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/memory"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/skills"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

func NewServiceWithContextEngine(settings config.Settings, secret func(string) string, profiler resources.Profiler, store memory.Store, skillStore skills.Store, factory providers.Factory, extension *tools.Extension, reviewer tools.ApprovalReviewer, presenter tools.ApprovalPresenter, engine contextengine.Engine) (*Service, error) {
	if engine != nil {
		value := reflect.ValueOf(engine)
		switch value.Kind() {
		case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Interface, reflect.Chan:
			if value.IsNil() {
				return nil, ErrAdmission
			}
		}
	}
	svc, err := NewServiceWithContextEstimator(settings, secret, profiler, store, skillStore, factory, extension, reviewer, presenter, engine)
	if err != nil {
		return nil, err
	}
	svc.contextEngine = engine
	return svc, nil
}

// NewServiceWithContextEngineAndEvaluator composes custom context assembly and
// provider-neutral evaluation without changing either engine's authority.
func NewServiceWithContextEngineAndEvaluator(settings config.Settings, secret func(string) string, profiler resources.Profiler, store memory.Store, skillStore skills.Store, factory providers.Factory, extension *tools.Extension, reviewer tools.ApprovalReviewer, presenter tools.ApprovalPresenter, engine contextengine.Engine, evaluator evaluation.Evaluator) (*Service, error) {
	return NewServiceWithContextEngineEvaluatorAndEventSink(settings, secret, profiler, store, skillStore, factory, extension, reviewer, presenter, engine, evaluator, nil)
}

// NewServiceWithContextEngineEvaluatorAndEventSink composes context assembly,
// evaluation, and live committed-event delivery without changing their separate
// authority boundaries.
func NewServiceWithContextEngineEvaluatorAndEventSink(settings config.Settings, secret func(string) string, profiler resources.Profiler, store memory.Store, skillStore skills.Store, factory providers.Factory, extension *tools.Extension, reviewer tools.ApprovalReviewer, presenter tools.ApprovalPresenter, engine contextengine.Engine, evaluator evaluation.Evaluator, sink runtime.EventSink) (*Service, error) {
	svc, err := NewServiceWithContextEngine(settings, secret, profiler, store, skillStore, factory, extension, reviewer, presenter, engine)
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

func prepareTaskContext(ctx context.Context, r *Request, secrets []string) ([]providers.Message, error) {
	if r.preparedContext != nil {
		return r.preparedContext.Messages, nil
	}
	parts := contextengine.Assembly{Version: 1}
	if r.continuation != nil {
		parts.History = r.continuation.Messages
	}
	if r.memoryContext != nil {
		parts.Memory = r.memoryContext.Messages
	}
	if r.skillContext != nil {
		parts.Skills = r.skillContext.Messages
	}
	parts.Current = r.Messages
	if len(parts.Current) == 0 {
		parts.Current = []providers.Message{{Role: "user", Content: r.Prompt}}
	}
	if contract := responseContract(*r); contract.Active() {
		// Public format reminders carry user-level authority and are assembled
		// before token estimation/admission. The original request stays intact.
		parts.Current = append(append([]providers.Message(nil), parts.Current...), providers.Message{Role: "user", Content: contract.Instructions()})
	}
	if contextengine.CheckMessages(parts.History, parts.Memory, parts.Skills, parts.Current) != nil {
		return nil, ErrAdmission
	}
	for _, tier := range []*[]providers.Message{&parts.History, &parts.Memory, &parts.Skills, &parts.Current} {
		if len(*tier) == 0 {
			continue
		}
		clean, err := redactCodexHistoryMessages(*tier, secrets)
		if err != nil {
			return nil, ErrAdmission
		}
		*tier = clean
	}
	prepared, err := contextengine.Assemble(ctx, r.contextEngine, parts)
	if err != nil {
		return nil, ErrAdmission
	}
	r.preparedContext = &prepared
	if !prepared.MemoryIncluded {
		r.memoryContext = nil
	}
	if !prepared.SkillsIncluded {
		r.skillContext = nil
	}
	r.memoryPrepared, r.skillPrepared = true, true
	return prepared.Messages, nil
}

// Engine views are owned, redacted projections. Canonical compaction still
// operates on the original snapshot so its digest names the durable source.
func selectContextCompaction(ctx context.Context, engine contextengine.Engine, source sessions.Snapshot, request sessions.CompactionRequest, secrets []string) (sessions.CompactionRequest, error) {
	if engine == nil {
		return request, nil
	}
	if contextengine.CheckMessages(source.Messages) != nil {
		return sessions.CompactionRequest{}, ErrAdmission
	}
	input := source
	var err error
	input.Messages, err = redactCodexHistoryMessages(source.Messages, secrets)
	if err != nil {
		return sessions.CompactionRequest{}, ErrAdmission
	}
	// The library freezes a bounded minimal snapshot; metadata outside that
	// projection is not passed to the engine or mutated here.
	request.Summary = redactSummary(request.Summary, secrets)
	chosen, err := contextengine.SelectCompaction(ctx, engine, input, request)
	if err != nil {
		return sessions.CompactionRequest{}, ErrAdmission
	}
	return chosen, nil
}
