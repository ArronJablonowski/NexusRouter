package remote

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"testing"
	"time"
)

func (b *fakeBackend) Dependencies(context.Context) (webui.DependencyInventory, error) {
	return webui.DependencyInventory{Version: 1, Hostname: "fixture-host", ObservedAt: time.Now().UTC(), Items: []webui.Dependency{}}, nil
}
func TestDependenciesInspectPermission(t *testing.T) {
	f := setup(t)
	p, err := f.client.Dependencies(context.Background(), "node-a")
	if err != nil || p.Hostname != "fixture-host" {
		t.Fatal(p, err)
	}
	peer := f.clientPeer
	peer.Operations = []string{"info"}
	writeRegistry(t, f.serverTrust, peer)
	if _, err = f.client.Dependencies(context.Background(), "node-a"); err == nil {
		t.Fatal("inspect permission bypassed")
	}
}
