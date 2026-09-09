package app

import (
	"context"
	"reflect"
	"sort"
	"sync"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

// eventDelivery is created for one top-level execution graph. It serializes
// commit plus delivery so concurrently produced worker events cannot overtake
// an earlier event after that earlier transaction commits.
type eventDelivery struct {
	mu         sync.Mutex
	cancel     context.CancelFunc
	configured runtime.EventSink
	sequencer  *configuredSinkSequencer
	perCall    func(runtime.Event)
	failed     bool
	err        error
}

type configuredSinkSequence struct {
	mu   sync.Mutex
	refs int
}

// configuredSinkSequencer orders commit plus delivery per task while retaining
// the public contract that unrelated tasks may invoke the configured sink
// concurrently.
type configuredSinkSequencer struct {
	mu    sync.Mutex
	tasks map[string]*configuredSinkSequence
}

func (s *configuredSinkSequencer) acquire(taskIDs []string) func() {
	if s == nil {
		return func() {}
	}
	unique := map[string]bool{}
	ids := make([]string, 0, len(taskIDs))
	for _, id := range taskIDs {
		if id != "" && !unique[id] {
			unique[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	entries := make([]*configuredSinkSequence, len(ids))
	s.mu.Lock()
	if s.tasks == nil {
		s.tasks = map[string]*configuredSinkSequence{}
	}
	for i, id := range ids {
		entry := s.tasks[id]
		if entry == nil {
			entry = &configuredSinkSequence{}
			s.tasks[id] = entry
		}
		entry.refs++
		entries[i] = entry
	}
	s.mu.Unlock()
	for _, entry := range entries {
		entry.mu.Lock()
	}
	return func() {
		for i := len(entries) - 1; i >= 0; i-- {
			entries[i].mu.Unlock()
		}
		s.mu.Lock()
		for i, entry := range entries {
			entry.refs--
			if entry.refs == 0 {
				delete(s.tasks, ids[i])
			}
		}
		s.mu.Unlock()
	}
}

func newEventDelivery(cancel context.CancelFunc, configured runtime.EventSink, perCall func(runtime.Event), sequencers ...*configuredSinkSequencer) *eventDelivery {
	var sequencer *configuredSinkSequencer
	if len(sequencers) > 0 {
		sequencer = sequencers[0]
	} else if configured != nil {
		sequencer = &configuredSinkSequencer{tasks: map[string]*configuredSinkSequence{}}
	}
	return &eventDelivery{cancel: cancel, configured: configured, sequencer: sequencer, perCall: perCall}
}

// CommitAndDeliver never turns a post-commit callback failure into a journal
// failure. The generic delivery error is joined by the outer Service.Run.
func (d *eventDelivery) CommitAndDeliver(ctx context.Context, event runtime.Event, includePerCall bool, commit func() error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sequencer != nil {
		release := d.sequencer.acquire([]string{event.TaskID})
		defer release()
	}
	if err := commit(); err != nil {
		return err
	}
	if d.failed {
		return nil
	}
	failed := false
	if d.configured != nil {
		clone, err := event.Clone()
		if err != nil || d.invoke(func() error { return d.configured.Emit(ctx, clone) }) != nil {
			failed = true
		}
	}
	if includePerCall && d.perCall != nil {
		clone, err := event.Clone()
		if err != nil || d.invoke(func() error { d.perCall(clone); return nil }) != nil {
			failed = true
		}
	}
	if failed {
		d.fail()
	}
	return nil
}

func (d *eventDelivery) invoke(call func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrEventDelivery
		}
	}()
	if call() != nil {
		return ErrEventDelivery
	}
	return nil
}

func (d *eventDelivery) fail() {
	d.failed = true
	d.err = ErrEventDelivery
	d.cancel()
}

func (d *eventDelivery) Err() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

func (d *eventDelivery) Failed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.failed
}

func validEventSink(sink runtime.EventSink) bool {
	if sink == nil {
		return true
	}
	value := reflect.ValueOf(sink)
	switch value.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Interface, reflect.Chan:
		return !value.IsNil()
	default:
		return true
	}
}

// installEventSink is construction-only; the SDK calls it before sharing the
// service with any goroutine.
func installEventSink(service *Service, sink runtime.EventSink) error {
	if service == nil || !validEventSink(sink) {
		return ErrAdmission
	}
	service.eventSink = sink
	if sink != nil {
		service.eventSinkSequencer = &configuredSinkSequencer{tasks: map[string]*configuredSinkSequence{}}
	} else {
		service.eventSinkSequencer = nil
	}
	return nil
}
