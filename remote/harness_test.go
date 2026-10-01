package remote

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
)

func TestRemoteHarnessScopeAndDiscovery(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.server.backend = &SDKBackend{Client: &sdk.Client{}, Models: []Model{{ID: "chat", Local: true}, {ID: "hidden", Local: true}}, Harnesses: []Harness{{ID: "pi", ModelID: "chat", Kind: "pi"}, {ID: "goose", ModelID: "chat", Kind: "goose"}, {ID: "hidden", ModelID: "hidden", Kind: "pi"}}}
	info, err := f.client.Info(ctx, "node-a")
	if err != nil || len(info.Harnesses) != 0 {
		t.Fatal(info, err)
	}
	peer := f.clientPeer
	peer.Harnesses = []string{"pi", "hidden"}
	writeRegistry(t, f.serverTrust, peer)
	info, err = f.client.Info(ctx, "node-a")
	if err != nil || len(info.Harnesses) != 1 || info.Harnesses[0].ID != "pi" {
		t.Fatal(info, err)
	}
	task := testTask()
	task.HarnessID = "pi"
	task.HarnessDifficulty = "hard"
	// The client must also explicitly grant the registration.
	if _, err = f.client.Dispatch(ctx, "node-a", "harness-request-001", task); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	localPeer := f.serverPeer
	localPeer.Harnesses = []string{"pi"}
	writeRegistry(t, f.clientTrust, localPeer)
	peer.Harnesses = nil
	writeRegistry(t, f.serverTrust, peer)
	if _, err = f.client.Dispatch(ctx, "node-a", "harness-request-001", task); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
}
func TestRemoteHarnessContractAndLegacyDigest(t *testing.T) {
	task := testTask()
	before := hash(task)
	// Adding optional fields must preserve old request serialization/idempotency.
	legacy := struct {
		Version       int     `json:"version"`
		ModelID       string  `json:"model_id"`
		Prompt        string  `json:"prompt"`
		Domain        string  `json:"domain"`
		Profile       string  `json:"profile"`
		ContextTokens int     `json:"context_tokens"`
		MaxCost       float64 `json:"max_cost"`
		Private       bool    `json:"private"`
	}{task.Version, task.ModelID, task.Prompt, task.Domain, task.Profile, task.ContextTokens, task.MaxCost, task.Private}
	if before != hash(legacy) {
		t.Fatal("legacy request hash changed")
	}
	task.HarnessDifficulty = "hard"
	if task.Validate() == nil {
		t.Fatal("difficulty without harness")
	}
	task.HarnessID = "auto"
	if task.Validate() == nil {
		t.Fatal("unscoped auto allowed")
	}
	task.HarnessID = "pi"
	if task.Validate() != nil || hash(task) == before {
		t.Fatal("unbound harness intent")
	}
	raw, _ := json.Marshal(task)
	var copy Task
	if json.Unmarshal(raw, &copy) != nil || hash(copy) != hash(task) {
		t.Fatal("round trip")
	}
	peer := testPeer("node", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "https://127.0.0.1:443")
	peer.Harnesses = []string{"auto"}
	if peer.Validate() == nil {
		t.Fatal("auto scope")
	}
	peer.Harnesses = []string{"pi", "pi"}
	if peer.Validate() == nil {
		t.Fatal("duplicate scope")
	}
	b := SDKBackend{Client: &sdk.Client{}, Harnesses: []Harness{{ID: "pi", ModelID: "other"}}}
	if _, err := b.Submit(context.Background(), "key", task); !errors.Is(err, ErrDenied) {
		t.Fatal("wrong model/harness pair", err)
	}
}
