package remote

import (
	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"strings"
	"testing"
)

func TestRemoteConversationRequiresDirectTextOnly(t *testing.T) {
	task := testTask()
	task.Execution = &runtime.RemoteExecution{Mode: "direct", Depth: 1}
	task.Messages = []providers.Message{{Role: "user", Content: "previous question"}, {Role: "assistant", Content: "previous answer"}}
	if task.Validate() != nil {
		t.Fatal("text conversation rejected")
	}
	original := hash(task)
	task.Messages[1].Content = "different answer"
	if hash(task) == original {
		t.Fatal("conversation missing from durable request digest")
	}
	for _, mutate := range []func(*Task){
		func(x *Task) { x.Execution = nil },
		func(x *Task) { x.Execution = &runtime.RemoteExecution{Mode: "commander", Depth: 1} },
		func(x *Task) { x.Messages[0].Role = "tool"; x.Messages[0].ToolCallID = "call" },
		func(x *Task) {
			x.Messages[0].ToolCalls = []providers.ToolCall{{ID: "call", Name: "write_file", Arguments: []byte(`{}`)}}
		},
		func(x *Task) { x.Messages[0].ToolFailed = true },
		func(x *Task) { x.Messages[0].Content = strings.Repeat("x", MaxBody/2+1) },
	} {
		bad := task
		bad.Messages = append([]providers.Message(nil), task.Messages...)
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatal("remote conversation granted unsupported authority")
		}
	}
}

func TestDirectRegistrationDoesNotGrantOtherHarnessAuthority(t *testing.T) {
	task := testTask()
	task.HarnessID = harness.DirectRegistration(task.ModelID)
	task.HarnessDifficulty = "unknown"
	peer := Peer{Models: []string{task.ModelID}, Operations: []string{"dispatch", "info"}, MaxContextTokens: 32768, AllowPrivate: true, Endpoint: "https://127.0.0.1:9443"}
	if !peer.permitsTask(task) {
		t.Fatal("existing direct-model authority unavailable")
	}
	q := HarnessIdentityRequest{ModelID: task.ModelID, HarnessID: task.HarnessID, ContextTokens: 8192}
	if !peer.permitsIdentity(q) {
		t.Fatal("direct admission scope unavailable")
	}
	task.HarnessID = "external-pi"
	if peer.permitsTask(task) {
		t.Fatal("external harness authority granted")
	}
	q.ModelID = "other"
	if peer.permitsIdentity(q) {
		t.Fatal("another model admitted")
	}
}
