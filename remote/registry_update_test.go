package remote

import (
	"context"
	"errors"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryAtomicCASAndRotation(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	path := TrustFile(filepath.Join(dir, "peers.json"))
	empty := Registry{Version: 1}
	if e := path.Replace(empty, "absent"); e != nil {
		t.Fatal(e)
	}
	if e := path.Replace(empty, "absent"); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	bad := Registry{Version: 2}
	if e := path.Replace(bad, empty.Digest()); e == nil {
		t.Fatal("invalid policy installed")
	}
	current, e := path.Read()
	if e != nil || current.Digest() != empty.Digest() {
		t.Fatal(e)
	}
	f := setup(t)
	nextCreds, nextPin := f.ca.leaf(t, "node-b")
	rotated := f.clientPeer
	rotated.Pins = append(rotated.Pins, nextPin)
	writeRegistry(t, f.serverTrust, rotated)
	if _, e = f.client.Info(context.Background(), "node-a"); e != nil {
		t.Fatal(e)
	}
	oldCreds := f.client.Credentials
	f.client.Credentials = nextCreds
	if _, e = f.client.Info(context.Background(), "node-a"); e != nil {
		t.Fatal(e)
	}
	rotated.Pins = []string{nextPin}
	writeRegistry(t, f.serverTrust, rotated)
	f.client.Credentials = oldCreds
	if _, e = f.client.Info(context.Background(), "node-a"); e == nil {
		t.Fatal("retired key accepted")
	}
	f.client.Credentials = nextCreds
	if _, e = f.client.Info(context.Background(), "node-a"); e != nil {
		t.Fatal(e)
	}
}

func TestSDKCostZeroDoesNotDisableRemoteCeiling(t *testing.T) {
	paid := 1.0
	for _, cost := range []*float64{nil, &paid} {
		b := &SDKBackend{Client: &sdk.Client{}, Models: []Model{{ID: "chat", Local: true, ContextTokens: 8192, EstimatedCost: cost}}}
		if _, err := b.Submit(context.Background(), "request-cost-0001", testTask()); !errors.Is(err, ErrDenied) {
			t.Fatalf("cost %v: %v", cost, err)
		}
	}
	p := testPeer("node", strings.Repeat("a", 64), "https://127.0.0.1:443")
	p.MaxCost = math.NaN()
	if p.Validate() == nil {
		t.Fatal("NaN ceiling accepted")
	}
}
