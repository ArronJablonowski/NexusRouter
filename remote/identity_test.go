package remote

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRemoteHarnessIdentityScopesAndNoDispatch(t *testing.T) {
	t.Run("https", func(t *testing.T) { testRemoteHarnessIdentity(t, false) })
	t.Run("ssh", func(t *testing.T) {
		if os.Getenv("NEXUS_REMOTE_SSH_NATIVE") != "1" {
			t.Skip("native SSH opt-in")
		}
		testRemoteHarnessIdentity(t, true)
	})
}
func testRemoteHarnessIdentity(t *testing.T, ssh bool) {
	f := setup(t)
	if ssh {
		transport := nativeSSHServer(t)
		f.serverPeer.Transport = "ssh"
		f.serverPeer.SSH = &transport
	}
	ctx := context.Background()
	var calls atomic.Int32
	identity := harness.Identity{Version: 1, Harness: "pi", HarnessVersion: "1", AdapterVersion: "1", Provider: "local", Model: "fixture", ModelRevision: "v1", ConfigSHA256: strings.Repeat("a", 64)}
	b := &SDKBackend{Models: []Model{{ID: "chat", Provider: "local", Model: "fixture", Local: true, ContextTokens: 32768}}, Harnesses: []Harness{{ID: "pair", ModelID: "chat", Kind: "pi", ModelRevision: "v1"}}, Identify: func(m, h string, n int) (harness.Identity, error) { calls.Add(1); return identity, nil }}
	f.server.backend = b
	q := HarnessIdentityRequest{"chat", "pair", 16384}
	f.serverPeer.Harnesses = []string{"pair"}
	writeRegistry(t, f.clientTrust, f.serverPeer)
	if _, err := f.client.HarnessIdentity(ctx, "node-a", q); !errors.Is(err, ErrDenied) || calls.Load() != 0 {
		t.Fatal("server scope bypass", err)
	}
	f.clientPeer.Harnesses = []string{"pair"}
	f.clientPeer.Operations = []string{"info"}
	writeRegistry(t, f.serverTrust, f.clientPeer)
	got, err := f.client.HarnessIdentity(ctx, "node-a", q)
	if err != nil || got.Identity != identity || got.Request != q || calls.Load() != 1 {
		t.Fatal(got, err)
	}
	var reservations int
	if err = f.journal.db.QueryRow("SELECT count(*) FROM requests").Scan(&reservations); err != nil || reservations != 0 {
		t.Fatal(reservations, err)
	}
	f.serverPeer.Harnesses = nil
	writeRegistry(t, f.clientTrust, f.serverPeer)
	if _, err = f.client.HarnessIdentity(ctx, "node-a", q); !errors.Is(err, ErrDenied) || calls.Load() != 1 {
		t.Fatal("client scope bypass", err)
	}
	// Local-only discovery must not invoke identity lookup for a cloud model.
	b.Models[0].Local = false
	if _, err = b.HarnessIdentity(ctx, q, false); !errors.Is(err, ErrDenied) || calls.Load() != 1 {
		t.Fatal("cloud scope bypass", err)
	}
	b.Models[0].Local = true
	b.Identify = func(string, string, int) (harness.Identity, error) {
		bad := identity
		bad.Model = "different"
		return bad, nil
	}
	if _, err = b.HarnessIdentity(ctx, q, false); !errors.Is(err, ErrUnavailable) {
		t.Fatal("model mismatch accepted", err)
	}
}
