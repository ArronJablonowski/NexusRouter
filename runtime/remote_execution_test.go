package runtime

import (
	"testing"
	"time"
)

func TestRemoteExecutionBounds(t *testing.T) {
	good := RemoteExecution{Mode: "commander", Depth: 1, SpecialistIDs: []string{"worker"}, MaxCalls: 2, Deadline: time.Now().Add(time.Minute)}
	if good.Validate() != nil {
		t.Fatal("bounded commander rejected")
	}
	for _, change := range []func(*RemoteExecution){func(x *RemoteExecution) { x.Depth = 2 }, func(x *RemoteExecution) { x.MaxCalls = 17 }, func(x *RemoteExecution) { x.SpecialistIDs = []string{"worker", "worker"} }, func(x *RemoteExecution) { x.Deadline = time.Time{} }, func(x *RemoteExecution) { x.Mode = "auto" }} {
		x := good
		change(&x)
		if x.Validate() == nil {
			t.Fatal("invalid bounds accepted")
		}
	}
	if (RemoteExecution{Mode: "direct", Depth: 1}).Validate() != nil {
		t.Fatal("direct rejected")
	}
}
