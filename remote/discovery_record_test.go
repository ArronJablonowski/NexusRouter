package remote

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDiscoveryCandidateIsBoundedUnverifiedMetadata(t *testing.T) {
	now := time.Now().UTC()
	txt := []string{"v=1", "id=node-a", "name=node-a.local", "pin=" + strings.Repeat("a", 64), "ssh=22"}
	candidate, err := ParseDiscoveryCandidate("node-a", "192.168.1.20", 8443, txt, 3600, now)
	if err != nil || candidate.Verified || candidate.Endpoint != "https://192.168.1.20:8443" || candidate.SSHPort != 22 || !candidate.ExpiresAt.Equal(now.Add(120*time.Second)) {
		t.Fatal(candidate, err)
	}
	body, _ := json.Marshal(candidate)
	for _, forbidden := range []string{"operations", "models", "harnesses", "allow_private", "password", "quality"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatal(string(body))
		}
	}
	ipv6, err := ParseDiscoveryCandidate("node-a", "fd00::1", 8443, txt[:4], 10, now)
	if err != nil || ipv6.Endpoint != "https://[fd00::1]:8443" || !ipv6.ExpiresAt.Equal(now.Add(10*time.Second)) {
		t.Fatal(ipv6, err)
	}
}
func TestDiscoveryRejectsAuthorityInjectionAndAmbiguousFields(t *testing.T) {
	base := []string{"v=1", "id=node-a", "name=node-a.local", "pin=" + strings.Repeat("a", 64)}
	bad := [][]string{}
	for _, extra := range []string{"V=1", "operations=dispatch", "ssh=0", "ssh=65536", "ssh=022", "ssh=+22", "ssh=22\n"} {
		bad = append(bad, append(append([]string{}, base...), extra))
	}
	for index, value := range []string{"v=2", "id=node-b", "name=other.local/command", "pin=" + strings.Repeat("g", 64)} {
		v := append([]string{}, base...)
		v[index] = value
		bad = append(bad, v)
	}
	for _, fields := range bad {
		if _, err := ParseDiscoveryCandidate("node-a", "10.0.0.2", 8443, fields, 10, time.Now()); err == nil {
			t.Fatal(fields)
		}
	}
	for _, address := range []string{"127.0.0.1", "0.0.0.0", "8.8.8.8", "224.0.0.251", "fe80::1", "fd00::1%en0", "::ffff:192.168.1.2", "node.local"} {
		if _, err := ParseDiscoveryCandidate("node-a", address, 8443, base, 10, time.Now()); err == nil {
			t.Fatal(address)
		}
	}
	if _, err := ParseDiscoveryCandidate("node-a", "10.0.0.2", 8443, base, 0, time.Now()); err == nil {
		t.Fatal("goodbye became candidate")
	}
	for _, name := range []string{"", "NODE.local", "-node.local", "node..local", strings.Repeat("a", 64) + ".local", "node.local.", "node\nlocal"} {
		if discoveryDNSName(name) {
			t.Fatal(name)
		}
	}
}

func FuzzDiscoveryCandidateNeverGrantsAuthority(f *testing.F) {
	f.Add("node-a", "10.0.0.2", 8443, "v=1|id=node-a|name=node-a.local|pin="+strings.Repeat("a", 64), uint32(120))
	f.Fuzz(func(t *testing.T, instance, address string, port int, fields string, ttl uint32) {
		now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
		result, err := ParseDiscoveryCandidate(instance, address, port, strings.Split(fields, "|"), ttl, now)
		if err != nil {
			return
		}
		if result.Verified || result.Version != 1 || !result.ExpiresAt.After(now) || result.ExpiresAt.After(now.Add(120*time.Second)) {
			t.Fatal(result)
		}
	})
}
