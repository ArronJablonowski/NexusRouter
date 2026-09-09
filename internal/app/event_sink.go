package app

import (
	"context"
	"reflect"
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
	perCall    func(runtime.Event)
	failed     bool
	err        error
}

func newEventDelivery(cancel context.CancelFunc, configured runtime.EventSink, perCall func(runtime.Event)) *eventDelivery {
	return &eventDelivery{cancel: cancel, configured: configured, perCall: perCall}
}

// CommitAndDeliver never turns a post-commit callback failure into a journal
// failure. The generic delivery error is joined by the outer Service.Run.
func (d *eventDelivery) CommitAndDeliver(ctx context.Context, event runtime.Event, includePerCall bool, commit func() error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
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
	return nil
}
