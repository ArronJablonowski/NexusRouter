package remote

import (
	"context"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"
)

// Native manager fixtures remain loopback-only unless an interface is explicitly
// selected for multicast qualification. This is never a production service.
func serviceFixtureAddress(t *testing.T) string {
	t.Helper()
	name := os.Getenv("NEXUS_REMOTE_SERVICE_INTERFACE")
	if name == "" {
		return "127.0.0.1"
	}
	iface, err := net.InterfaceByName(name)
	if err != nil || iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 {
		t.Fatal("invalid explicit service interface", err)
	}
	addresses, err := iface.Addrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.String())
		if err == nil && prefix.Addr().Is4() && prefix.Addr().IsPrivate() {
			return prefix.Addr().String()
		}
	}
	t.Fatal("service interface lacks private IPv4")
	return ""
}
func serviceFixtureDiscovery(t *testing.T, ctx context.Context, spec ServiceTemplateSpec, pin string, present bool) {
	t.Helper()
	if spec.AdvertiseInterface == "" {
		return
	}
	found := false
	candidates, err := serviceDiscoveryCandidates(ctx, spec.AdvertiseInterface)
	if present && err == nil {
		for attempt := 0; attempt < 2; attempt++ {
			seen := false
			for _, candidate := range candidates {
				if candidate.Instance == spec.Instance {
					seen = true
				}
			}
			if seen {
				break
			}
			time.Sleep(300 * time.Millisecond)
			candidates, err = serviceDiscoveryCandidates(ctx, spec.AdvertiseInterface)
			if err != nil {
				break
			}
		}
	}
	if err != nil {
		t.Fatal("native service discovery", err)
	}
	for _, candidate := range candidates {
		if candidate.Instance != spec.Instance {
			continue
		}
		if candidate.Verified || candidate.Endpoint != "https://"+spec.Listen || candidate.ServerName != spec.AdvertiseName || candidate.ClaimedCertificateSHA256 != pin || candidate.SSHPort != spec.AdvertiseSSHPort {
			t.Fatal("native service claim mismatch", candidate)
		}
		found = true
	}
	if found != present {
		t.Fatal("native service advertisement presence", found, "want", present)
	}
	t.Logf("native service discovery presence=%v; claims remain unverified", found)
}
