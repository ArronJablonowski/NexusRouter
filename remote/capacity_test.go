package remote

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/resources"
)

func fixtureCapacity(identity harness.Identity) (harness.Identity, resources.Need, resources.CapacityResult, error) {
	now := time.Now().UTC()
	return identity, resources.Need{RAM: 100}, resources.CapacityResult{Version: 1, Action: resources.CapacityAdmit, Reason: resources.CapacityAvailable, SnapshotTime: now, ObservedAt: now, Headroom: resources.CapacityHeadroom{RAMBytes: 1000}, MaxAdditional: 1}, nil
}
func (b rankingBackend) HarnessCapacity(context.Context, HarnessIdentityRequest, bool) (harness.Identity, resources.Need, resources.CapacityResult, error) {
	return fixtureCapacity(b.identity)
}
func (b *automaticBackend) HarnessCapacity(context.Context, HarnessIdentityRequest, bool) (harness.Identity, resources.Need, resources.CapacityResult, error) {
	return fixtureCapacity(b.identity)
}
func TestRemoteCapacityScopesFreshnessAndNoDispatch(t *testing.T) {
	for _, transport := range []string{"https", "ssh"} {
		t.Run(transport, func(t *testing.T) {
			if transport == "ssh" && os.Getenv("NEXUS_REMOTE_SSH_NATIVE") != "1" {
				t.Skip("native SSH opt-in")
			}
			f := setup(t)
			task, _, _ := outcomeFixture(t)
			identity := *task.ExpectedHarnessIdentity
			if transport == "ssh" {
				ssh := nativeSSHServer(t)
				f.serverPeer.Transport = "ssh"
				f.serverPeer.SSH = &ssh
			}
			f.serverPeer.Models = []string{task.ModelID}
			f.serverPeer.Harnesses = []string{task.HarnessID}
			writeRegistry(t, f.clientTrust, f.serverPeer)
			calls := 0
			stale := false
			b := &SDKBackend{Models: []Model{{ID: task.ModelID, Provider: identity.Provider, Model: identity.Model, Local: true, ContextTokens: 32768}}, Harnesses: []Harness{{ID: task.HarnessID, ModelID: task.ModelID, Kind: identity.Harness, ModelRevision: identity.ModelRevision}}, Identify: func(string, string, int) (harness.Identity, error) { return identity, nil }, PlanHarness: func(ctx context.Context, _ string, _ string, _ int) (harness.Identity, resources.Need, resources.CapacityResult, error) {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 2*time.Second {
					t.Error("unbounded capacity observation")
				}
				i, n, c, e := fixtureCapacity(identity)
				if stale {
					c.SnapshotTime = c.SnapshotTime.Add(-time.Minute)
				}
				return i, n, c, e
			}}
			f.server.backend = b
			q := HarnessIdentityRequest{task.ModelID, task.HarnessID, 8192}
			if _, err := f.client.HarnessCapacity(context.Background(), "node-a", q); !errors.Is(err, ErrDenied) || calls != 0 {
				t.Fatal("scope bypass", err, calls)
			}
			f.clientPeer.Models = f.serverPeer.Models
			f.clientPeer.Harnesses = f.serverPeer.Harnesses
			writeRegistry(t, f.serverTrust, f.clientPeer)
			got, err := f.client.HarnessCapacity(context.Background(), "node-a", q)
			if err != nil || got.Identity != identity || got.Need.RAM != 100 || calls != 1 {
				t.Fatal(got, err, calls)
			}
			stale = true
			if _, err = f.client.HarnessCapacity(context.Background(), "node-a", q); err == nil {
				t.Fatal("stale capacity accepted")
			}
			b.Models[0].Local = false
			before := calls
			if _, _, _, err = b.HarnessCapacity(context.Background(), q, false); !errors.Is(err, ErrDenied) || calls != before {
				t.Fatal("cloud measured outside scope", err)
			}
			var count int
			if err = f.journal.db.QueryRow("SELECT count(*) FROM requests").Scan(&count); err != nil || count != 0 {
				t.Fatal("capacity created request", count, err)
			}
		})
	}
}

func TestCapacityRejectsInconsistentAdmit(t *testing.T) {
	task, _, _ := outcomeFixture(t)
	i, n, c, err := fixtureCapacity(*task.ExpectedHarnessIdentity)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"ram", "vram", "time"} {
		t.Run(mode, func(t *testing.T) {
			value := HarnessCapacity{Version: 1, Instance: "node-a", Request: HarnessIdentityRequest{task.ModelID, task.HarnessID, 8192}, Identity: i, Need: n, Capacity: c, CheckedAt: time.Now().UTC()}
			switch mode {
			case "ram":
				value.Need.RAM = 1001
			case "vram":
				value.Need.VRAM = 1
			case "time":
				value.CheckedAt = value.Capacity.ObservedAt.Add(-time.Second)
			}
			if value.valid() {
				t.Fatal("inconsistent admission accepted")
			}
		})
	}
}

type waitingRankingBackend struct{ rankingBackend }

func (b waitingRankingBackend) HarnessCapacity(context.Context, HarnessIdentityRequest, bool) (harness.Identity, resources.Need, resources.CapacityResult, error) {
	i, n, c, e := fixtureCapacity(b.identity)
	c.Action = resources.CapacityWait
	c.Reason = resources.CapacityExhausted
	c.MaxAdditional = 0
	return i, n, c, e
}
