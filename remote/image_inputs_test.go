package remote

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/attachments"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/submissions"
	"testing"
)

func TestRemoteImageRequiresVisionModel(t *testing.T) {
	zero := 0.0
	calls := 0
	backend := SDKBackend{Models: []Model{{ID: "model", Model: "actual", Local: true, EstimatedCost: &zero, ContextTokens: 8192}}, SubmitTask: func(context.Context, string, Task) (submissions.Status, error) {
		calls++
		return submissions.Status{Version: 1, ID: "submission", State: "queued"}, nil
	}}
	frame := attachments.Encode("Describe", []attachments.Input{{Name: "sample.jpg", Image: "/9j/AQ=="}})
	task := Task{Version: 1, ModelID: "model", ExpectedModel: "actual", Prompt: frame, Domain: "general", Profile: "default", ContextTokens: 8192, Private: true}
	for _, conversation := range []bool{false, true} {
		if conversation {
			task.Prompt = "Describe"
			task.Messages = []providers.Message{{Role: "user", Content: frame}}
			task.Execution = &runtime.RemoteExecution{Mode: "direct", Depth: 1}
		}
		backend.Models[0].Capabilities = []string{"chat"}
		if _, e := backend.Submit(context.Background(), "image-request-0001", task); !errors.Is(e, ErrDenied) || calls != 0 {
			t.Fatal("text-only model accepted images", e, calls)
		}
	}
	backend.Models[0].Capabilities = []string{"chat", "vision"}
	if _, e := backend.Submit(context.Background(), "image-request-0001", task); e != nil || calls != 1 {
		t.Fatal("vision request rejected", e, calls)
	}
	backend.Models[0].Capabilities = []string{"chat"}
	task.Messages = nil
	task.Execution = nil
	task.Prompt = "Ordinary document text"
	if _, e := backend.Submit(context.Background(), "text-request-00001", task); e != nil || calls != 2 {
		t.Fatal("text request regressed", e)
	}
}
