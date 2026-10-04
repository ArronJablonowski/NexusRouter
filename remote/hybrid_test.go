package remote

import (
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"testing"
	"time"
)

func TestHybridRequiresEverySpecialistPermission(t *testing.T) {
	task := Task{Version: 1, ModelID: "commander", Prompt: "Summarize", Domain: "writing", Profile: "default", ContextTokens: 1024, Private: true, Execution: &runtime.RemoteExecution{Mode: "commander", Depth: 1, SpecialistIDs: []string{"worker"}, MaxCalls: 2, Deadline: time.Now().Add(time.Minute)}}
	peer := Peer{Endpoint: "https://127.0.0.1:9443", Operations: []string{"dispatch"}, Models: []string{"commander"}, MaxContextTokens: 8192, AllowPrivate: true}
	if peer.permitsTask(task) {
		t.Fatal("unpermitted specialist authorized")
	}
	peer.Models = append(peer.Models, "worker")
	if !peer.permitsTask(task) {
		t.Fatal("permitted assignment rejected")
	}
	task.Execution.SpecialistIDs = []string{"commander"}
	if task.Validate() == nil {
		t.Fatal("Commander recursion accepted")
	}
}
