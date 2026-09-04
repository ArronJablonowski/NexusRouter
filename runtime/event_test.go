package runtime

import (
	"testing"
	"time"
)

func TestEventValidation(t *testing.T) {
	base := Event{Version: 1, ID: "id", TaskID: "task", SessionID: "session", CorrelationID: "correlation", Sequence: 1, Time: time.Now(), Kind: TaskStarted}
	if _, err := base.Encode(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Event){
		func(e *Event) { e.Version = 2 },
		func(e *Event) { e.Sequence = 0 },
		func(e *Event) { e.Kind = "unknown" },
		func(e *Event) { e.Kind = ToolCompleted },
		func(e *Event) { e.Kind = EvaluationRecorded },
		func(e *Event) { e.Kind = RouteSelected },
		func(e *Event) { e.Kind = ModelDelta },
	} {
		e := base
		mutate(&e)
		if _, err := e.Encode(); err == nil {
			t.Fatalf("invalid event accepted: %+v", e)
		}
	}
}
