package v1

import (
	"reflect"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

// Event is the version-one durable runtime event contract.
type Event = runtime.Event

// EventSink receives newly committed events. Event.Version carries the schema
// major version; replay remains explicit through ReadEvents.
type EventSink = runtime.EventSink

// EventSinkFunc adapts a function to EventSink.
type EventSinkFunc = runtime.EventSinkFunc

func validSDKEventSink(sink EventSink) bool {
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
