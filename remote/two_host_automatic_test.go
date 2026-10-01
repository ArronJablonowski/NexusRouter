package remote

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

func physicalAutomaticDispatch(t *testing.T, ctx context.Context, client *Client, local, transport string, task Task, wait func(string, string) submissions.Status) {
	t.Helper()
	request := AutomaticRequest{Version: 1, Prompt: task.Prompt, Routing: harness.Request{Version: 1, Task: harness.TaskClass{Domain: task.Domain, Profile: task.Profile, Difficulty: task.HarnessDifficulty}, Mode: "local_only", LocalRequired: true, ContextTokens: int64(task.ContextTokens)}}
	catalogue, err := client.DiscoverCandidates(ctx, request.Routing)
	if err != nil || len(catalogue.Candidates) != 1 {
		t.Fatal("physical automatic candidate discovery", catalogue, err)
	}
	routes, err := OpenRouteStore(filepath.Join(local, "automatic-"+transport))
	if err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(local, "automatic-evidence-"+transport)
	key := "physical-auto-" + transport
	first, choice, err := client.DispatchAutomatic(ctx, routes, evidence, key, request, harness.DefaultPolicy(), catalogue.Candidates, 0)
	if err != nil || choice.Destination != "node-a" || choice.HarnessID != task.HarnessID || choice.Identity != *task.ExpectedHarnessIdentity || choice.Explored || choice.Score.ConfirmedSamples != 0 || choice.Score.AdvisorySamples != 0 || choice.Score.EffectiveSamples != 0 {
		t.Fatal("physical automatic choice", choice, err)
	}
	result := wait(key, "succeeded")
	if result.Result == nil || result.Result.Text != "physical fixture result" || len(result.TaskIDs) != 1 {
		t.Fatal(result)
	}
	events, err := client.Events(ctx, "node-a", key, result.TaskIDs[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := runtime.ValidateHarnessOutcome(events.Events, result.TaskIDs[0])
	if err != nil || actual.Actual != choice.Identity {
		t.Fatal("automatic actual identity", actual, err)
	}
	reopened, err := OpenRouteStore(filepath.Join(local, "automatic-"+transport))
	if err != nil {
		t.Fatal(err)
	}
	recovered, saved, err := client.DispatchAutomatic(ctx, reopened, evidence, key, request, harness.Policy{}, nil, 0)
	if err != nil || recovered.ID != first.ID || recovered.State != "succeeded" || saved != choice {
		t.Fatal("automatic recovery changed choice", recovered, saved, err)
	}
	t.Log("physical automatic discovery, pinned actual identity and saved-choice recovery passed; single candidate is not comparative accuracy evidence")
}
