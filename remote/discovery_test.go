package remote

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"os"
	"path/filepath"
	"testing"
)

type discoveryBackend struct {
	*automaticBackend
	catalogueCalls, readyCalls int
	catalogueErr               bool
	cloud                      bool
	afterCatalogue             func()
}

func (b *discoveryBackend) Catalogue(context.Context, []string, bool, []string) (Info, error) {
	b.catalogueCalls++
	if b.afterCatalogue != nil {
		b.afterCatalogue()
	}
	if b.catalogueErr {
		return Info{}, ErrUnavailable
	}
	i := b.identity
	cost := 0.0
	return Info{Available: true, Models: []Model{{ID: "chat", Provider: i.Provider, Model: i.Model, Local: !b.cloud, ContextTokens: 32768, EstimatedCost: &cost}}, Harnesses: []Harness{{ID: "pair", ModelID: "chat", Kind: i.Harness, ModelRevision: i.ModelRevision}}}, nil
}
func (b *discoveryBackend) HarnessReadiness(context.Context, HarnessIdentityRequest, bool) (harness.Readiness, error) {
	b.readyCalls++
	return fixtureReadiness(b.identity), nil
}
func TestDiscoveryAndAutomaticRecovery(t *testing.T) {
	for _, ssh := range []bool{false, true} {
		t.Run(map[bool]string{false: "https", true: "ssh"}[ssh], func(t *testing.T) {
			if ssh && os.Getenv("NEXUS_REMOTE_SSH_NATIVE") != "1" {
				t.Skip("native SSH opt-in")
			}
			f, routes, request, proposals, a, _ := automaticFixture(t)
			// Use the first paired node only and align fixture registration names.
			f.serverPeer.Models = []string{"chat"}
			f.serverPeer.Harnesses = []string{"pair"}
			f.clientPeer.Models = f.serverPeer.Models
			f.clientPeer.Harnesses = f.serverPeer.Harnesses
			if ssh {
				s := nativeSSHServer(t)
				f.serverPeer.Transport = "ssh"
				f.serverPeer.SSH = &s
			}
			writeRegistry(t, f.clientTrust, f.serverPeer)
			writeRegistry(t, f.serverTrust, f.clientPeer)
			b := &discoveryBackend{automaticBackend: a}
			f.server.backend = b
			got, e := f.client.DiscoverCandidates(context.Background(), request.Routing)
			if e != nil || len(got.Candidates) != 1 || got.Candidates[0].Candidate.Identity != proposals[0].Candidate.Identity || a.creates != 0 {
				t.Fatal(got, e)
			}
			root := filepath.Join(t.TempDir(), "evidence")
			key := "discovered-lost-01"
			a.lost = true
			_, choice, e := f.client.DispatchDiscovered(context.Background(), routes, root, key, request, harness.DefaultPolicy(), 0)
			if !errors.Is(e, ErrUnavailable) || choice.Destination != "node-a" {
				t.Fatal(choice, e)
			}
			before := b.catalogueCalls
			b.catalogueErr = true
			status, recovered, e := f.client.DispatchDiscovered(context.Background(), routes, root, key, request, harness.Policy{}, .9)
			if e != nil || recovered != choice || status.State != "queued" || a.creates != 1 || b.catalogueCalls != before {
				t.Fatal("retry discovered or duplicated", status, recovered, e, b.catalogueCalls)
			}
		})
	}
}
func TestDiscoveryExcludesCloudBeforeReadiness(t *testing.T) {
	f, _, request, _, a, _ := automaticFixture(t)
	b := &discoveryBackend{automaticBackend: a, cloud: true}
	f.server.backend = b
	f.serverPeer.Models = []string{"chat"}
	f.serverPeer.Harnesses = []string{"pair"}
	f.serverPeer.AllowCloudInference = true
	// The server policy was already paired by automaticFixture; permit metadata cloud visibility.
	registry, e := TrustFile(f.serverTrust).Read()
	if e != nil {
		t.Fatal(e)
	}
	peer := registry.Peers[0]
	peer.Models = []string{"chat"}
	peer.Harnesses = []string{"pair"}
	peer.AllowCloudInference = true
	writeRegistry(t, f.serverTrust, peer)
	writeRegistry(t, f.clientTrust, f.serverPeer)
	got, e := f.client.DiscoverCandidates(context.Background(), request.Routing)
	if e != nil || len(got.Candidates) != 0 || len(got.Excluded) != 1 || b.readyCalls != 0 {
		t.Fatal(got, e, b.readyCalls)
	}
}
func TestSDKCatalogueNeverProbesProvider(t *testing.T) {
	b := &SDKBackend{Models: []Model{{ID: "chat", Local: true}, {ID: "cloud", Local: false}}, Harnesses: []Harness{{ID: "pair", ModelID: "chat"}}, Available: func(context.Context) bool { return true }}
	// Observe deliberately panics if the metadata-only path calls it.
	b.Observe = func(context.Context, []Model) ([]ModelObservation, *ResourceObservation, error) {
		t.Fatal("provider discovery called")
		return nil, nil, nil
	}
	got, e := b.Catalogue(context.Background(), []string{"chat", "cloud"}, false, []string{"pair"})
	if e != nil || len(got.Models) != 1 || len(got.Harnesses) != 1 || got.Models[0].Observation != nil {
		t.Fatal(got, e)
	}
}

func TestDiscoveryRejectsPolicyChangeAndSkipsDeniedPeers(t *testing.T) {
	f, _, request, _, a, _ := automaticFixture(t)
	b := &discoveryBackend{automaticBackend: a}
	f.server.backend = b
	f.serverPeer.Models = []string{"chat"}
	f.serverPeer.Harnesses = []string{"pair"}
	f.clientPeer.Models = f.serverPeer.Models
	f.clientPeer.Harnesses = f.serverPeer.Harnesses
	writeRegistry(t, f.serverTrust, f.clientPeer)
	f.serverPeer.Operations = []string{"info"}
	writeRegistry(t, f.clientTrust, f.serverPeer)
	got, e := f.client.DiscoverCandidates(context.Background(), request.Routing)
	if e != nil || b.catalogueCalls != 0 || len(got.Excluded) != 1 {
		t.Fatal(got, e, b.catalogueCalls)
	}
	f.serverPeer.Operations = []string{"info", "dispatch", "inspect", "cancel"}
	writeRegistry(t, f.clientTrust, f.serverPeer)
	b.afterCatalogue = func() { p := f.serverPeer; p.MaxContextTokens = 16384; writeRegistry(t, f.clientTrust, p) }
	if _, e = f.client.DiscoverCandidates(context.Background(), request.Routing); !errors.Is(e, ErrConflict) {
		t.Fatal("registry changed without conflict", e)
	}
}
