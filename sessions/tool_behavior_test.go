package sessions

import (
	"context"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestToolBehaviorReplayBinding(t *testing.T) {
	for _, behavior := range []runtime.ToolBehavior{"", runtime.BehaviorReadOnly, runtime.BehaviorIdempotentWrite, runtime.BehaviorNonIdempotentWrite} {
		events := fixture()
		events[4].Data.ToolBehavior, events[5].Data.ToolBehavior = behavior, behavior
		pending, err := Replay(context.Background(), events[:5], "task")
		if err != nil || pending.Pending["call"].ToolBehavior != behavior || !pending.UncertainEffects {
			t.Fatal("pending declaration lost", err)
		}
		if _, err := Replay(context.Background(), events, "task"); err != nil {
			t.Fatal(err)
		}
		events[5].Data.ToolBehavior = runtime.BehaviorIdempotentWrite
		if behavior == runtime.BehaviorIdempotentWrite {
			events[5].Data.ToolBehavior = ""
		}
		if _, err := Replay(context.Background(), events, "task"); err == nil {
			t.Fatal("completion declaration drift accepted")
		}
	}
}

func TestToolBehaviorEventPlacement(t *testing.T) {
	for _, kind := range []runtime.Kind{runtime.TaskStarted, runtime.ModelDelta, runtime.ToolStarted, runtime.ToolCompleted} {
		event := fixture()[4]
		event.Kind = kind
		event.Data.ToolBehavior = runtime.BehaviorIdempotentWrite
		validPlacement := kind == runtime.ToolStarted || kind == runtime.ToolCompleted
		if (event.Validate() == nil) != validPlacement {
			t.Fatal("incorrect behavior placement validation")
		}
		event.Data.ToolBehavior = "unknown"
		if event.Validate() == nil {
			t.Fatal("unknown declaration accepted")
		}
	}
}
