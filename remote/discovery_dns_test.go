package remote

import (
	"encoding/binary"
	"golang.org/x/net/dns/dnsmessage"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func discoveryFixturePacket(t testing.TB) dnsmessage.Message {
	t.Helper()
	name := func(value string) dnsmessage.Name {
		v, err := dnsmessage.NewName(value)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	resource := func(owner string, body dnsmessage.ResourceBody) dnsmessage.Resource {
		return dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: name(owner), Class: dnsmessage.ClassINET, TTL: 30}, Body: body}
	}
	instance := "node-a." + DiscoveryService
	return dnsmessage.Message{Header: dnsmessage.Header{ID: 7, Response: true, Authoritative: true}, Answers: []dnsmessage.Resource{resource(DiscoveryService, &dnsmessage.PTRResource{PTR: name(instance)})}, Additionals: []dnsmessage.Resource{
		resource(instance, &dnsmessage.SRVResource{Port: 8443, Target: name("node-a.local.")}),
		resource(instance, &dnsmessage.TXTResource{TXT: []string{"v=1", "id=node-a", "name=node-a.local", "pin=" + strings.Repeat("a", 64), "ssh=22"}}),
		resource("node-a.local.", &dnsmessage.AResource{A: [4]byte{192, 168, 1, 20}}),
	}}
}
func TestDiscoveryDNSQueryAndCompleteResponse(t *testing.T) {
	query, err := DiscoveryQuery(7)
	if err != nil {
		t.Fatal(err)
	}
	var q dnsmessage.Message
	if q.Unpack(query) != nil || q.ID != 7 || q.Response || len(q.Questions) != 1 || q.Questions[0].Name.String() != DiscoveryService || q.Questions[0].Type != dnsmessage.TypePTR {
		t.Fatal(q)
	}
	m := discoveryFixturePacket(t)
	packet, err := m.Pack()
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := DiscoveryPacketCandidates(packet, 7, netip.MustParseAddr("192.168.1.20"), time.Now())
	if err != nil || len(candidates) != 1 || candidates[0].Verified || candidates[0].SSHPort != 22 {
		t.Fatal(candidates, err)
	}
}
func TestDiscoveryDNSRejectsIncompleteSpoofedAndOversizedData(t *testing.T) {
	for _, change := range []func(*dnsmessage.Message){
		func(m *dnsmessage.Message) { m.Additionals = m.Additionals[:2] },
		func(m *dnsmessage.Message) { m.Additionals = append(m.Additionals, m.Additionals[0]) },
		func(m *dnsmessage.Message) { m.Additionals[0].Header.TTL = 0 },
		func(m *dnsmessage.Message) {
			m.Additionals[2].Body = &dnsmessage.AResource{A: [4]byte{192, 168, 1, 21}}
		},
		func(m *dnsmessage.Message) {
			m.Additionals[1].Body = &dnsmessage.TXTResource{TXT: []string{"v=1", "id=node-a", "name=node-a.local", "pin=" + strings.Repeat("a", 64), "operations=dispatch"}}
		},
	} {
		m := discoveryFixturePacket(t)
		change(&m)
		packet, err := m.Pack()
		if err != nil {
			t.Fatal(err)
		}
		candidates, _ := DiscoveryPacketCandidates(packet, 7, netip.MustParseAddr("192.168.1.20"), time.Now())
		if len(candidates) != 0 {
			t.Fatal(candidates)
		}
	}
	m := discoveryFixturePacket(t)
	packet, _ := m.Pack()
	forged := append([]byte{}, packet...)
	binary.BigEndian.PutUint16(forged[6:8], 65535)
	for _, bad := range [][]byte{nil, packet[:11], forged, make([]byte, 9001)} {
		if _, err := DiscoveryPacketCandidates(bad, 7, netip.MustParseAddr("192.168.1.20"), time.Now()); err == nil {
			t.Fatal("invalid packet accepted")
		}
	}
	if _, err := DiscoveryPacketCandidates(packet, 8, netip.MustParseAddr("192.168.1.20"), time.Now()); err == nil {
		t.Fatal("transaction mismatch")
	}
}
func FuzzDiscoveryDNSPacket(f *testing.F) {
	m := discoveryFixturePacket(f)
	packet, _ := m.Pack()
	f.Add(packet)
	f.Fuzz(func(t *testing.T, packet []byte) {
		now := time.Now()
		candidates, err := DiscoveryPacketCandidates(packet, 7, netip.MustParseAddr("192.168.1.20"), now)
		if err != nil {
			return
		}
		if len(candidates) > 32 {
			t.Fatal("unbounded projection")
		}
		for _, candidate := range candidates {
			if candidate.Verified || candidate.ExpiresAt.After(now.Add(120*time.Second)) {
				t.Fatal(candidate)
			}
		}
	})
}
