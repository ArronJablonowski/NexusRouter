package remote

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/internal/usagestats"
	"path/filepath"
	"testing"
)

func TestRemoteOdometerTLSDispatchPollingRestartAndOwnership(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.client.UsageFile = filepath.Join(t.TempDir(), "usage.db")
	f.backend.mu.Lock()
	f.backend.usage = func() usagestats.RemoteUsage {
		u := usagestats.EmptyRemoteUsage()
		u.Local = usagestats.Count{Input: "31", Output: "7", Measured: 1}
		u.Cloud = usagestats.Count{Input: "13", Output: "3", Measured: 1}
		return u
	}
	f.backend.mu.Unlock()
	key := "odometer-request-0001"
	if _, e := f.client.Dispatch(ctx, "node-a", key, testTask()); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 3; i++ {
		if _, e := f.client.Status(ctx, "node-a", key); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := f.journal.reserve(ctx, "another-caller", "odometer-request-0002", "hidden"); e != nil {
		t.Fatal(e)
	}
	restarted := &Client{Trust: f.client.Trust, Credentials: f.client.Credentials, UsageFile: f.client.UsageFile}
	if e := restarted.SyncUsage(ctx); e != nil {
		t.Fatal(e)
	}
	got, e := usagestats.ReadRemoteUsage(ctx, f.client.UsageFile)
	if e != nil || got.Requests != 1 || got.Local.Input != "31" || got.Cloud.Input != "13" || got.Total.Input != "44" {
		t.Fatal(got, e)
	}
	f.backend.mu.Lock()
	creates := f.backend.creates
	f.backend.mu.Unlock()
	if creates != 1 {
		t.Fatal("usage reconciliation dispatched inference", creates)
	}
	// New certificate for the same caller must not add the same request twice.
	creds, pin := f.ca.leaf(t, "node-b")
	peer := f.clientPeer
	peer.Pins = []string{pin}
	writeRegistry(t, f.serverTrust, peer)
	restarted.Credentials = creds
	if e := restarted.SyncUsage(ctx); e != nil {
		t.Fatal(e)
	}
	got, e = usagestats.ReadRemoteUsage(ctx, f.client.UsageFile)
	if e != nil || got.Requests != 1 || got.Total.Input != "44" {
		t.Fatal(got, e)
	}
	// A missing receipt preserves past usage and reports incomplete reconciliation.
	f.backend.mu.Lock()
	f.backend.usage = nil
	f.backend.mu.Unlock()
	if e := restarted.SyncUsage(ctx); e == nil {
		t.Fatal("missing evidence treated as complete")
	}
	got, e = usagestats.ReadRemoteUsage(ctx, f.client.UsageFile)
	if e != nil || got.Total.Input != "44" {
		t.Fatal(got, e)
	}
}
