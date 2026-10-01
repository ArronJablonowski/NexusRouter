package remote

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"net/netip"
	"testing"
	"time"
)

func advertisementFixture(t *testing.T) DiscoveryAdvertisement {
	t.Helper()
	creds, _ := newCA(t).leaf(t, "node-a.local")
	cert, err := tls.LoadX509KeyPair(creds.CertificateFile, creds.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return DiscoveryAdvertisement{Instance: "node-a", Address: "192.168.1.20:8443", ServerName: "node-a.local", SSHPort: 22, Certificate: leaf}
}

func TestAdvertisementRoundTripRemainsUntrusted(t *testing.T) {
	a := advertisementFixture(t)
	now := time.Now()
	query, err := DiscoveryQuery(42)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := a.Reply(query, now)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := DiscoveryPacketCandidates(reply, 42, netip.MustParseAddr("192.168.1.20"), now)
	if err != nil || len(candidates) != 1 {
		t.Fatal(candidates, err)
	}
	c := candidates[0]
	if c.Verified || c.Instance != "node-a" || c.SSHPort != 22 || c.ServerName != "node-a.local" || c.ClaimedCertificateSHA256 != Fingerprint(a.Certificate) || !c.ExpiresAt.Equal(now.Add(30*time.Second)) {
		t.Fatal(c)
	}
}

func TestAdvertisementRejectsInvalidListenerIdentity(t *testing.T) {
	base := advertisementFixture(t)
	q, _ := DiscoveryQuery(7)
	for name, change := range map[string]func(*DiscoveryAdvertisement){
		"public":      func(a *DiscoveryAdvertisement) { a.Address = "8.8.8.8:8443" },
		"wildcard":    func(a *DiscoveryAdvertisement) { a.Address = "0.0.0.0:8443" },
		"port":        func(a *DiscoveryAdvertisement) { a.Address = "192.168.1.20:0" },
		"ssh":         func(a *DiscoveryAdvertisement) { a.SSHPort = -1 },
		"name":        func(a *DiscoveryAdvertisement) { a.ServerName = "other.local" },
		"instance":    func(a *DiscoveryAdvertisement) { a.Instance = "bad.name" },
		"certificate": func(a *DiscoveryAdvertisement) { a.Certificate = nil },
		"forged_fields": func(a *DiscoveryAdvertisement) {
			c := *a.Certificate
			c.DNSNames = []string{"other.local"}
			a.Certificate = &c
			a.ServerName = "other.local"
		},
	} {
		t.Run(name, func(t *testing.T) {
			a := base
			change(&a)
			if _, err := a.Reply(q, time.Now()); err == nil {
				t.Fatal("accepted invalid advertisement")
			}
		})
	}
	if _, err := base.Reply(q, base.Certificate.NotAfter); err == nil {
		t.Fatal("expired certificate accepted")
	}
	if _, err := base.Reply(q, base.Certificate.NotBefore.Add(-time.Second)); err == nil {
		t.Fatal("premature certificate accepted")
	}
}
func TestAdvertisementRejectsUnrelatedAndExcessiveQueries(t *testing.T) {
	a := advertisementFixture(t)
	q, _ := DiscoveryQuery(7)
	for _, change := range []func([]byte){func(b []byte) { binary.BigEndian.PutUint16(b[4:6], 65535) }, func(b []byte) { b[2] |= 0x80 }, func(b []byte) { b[13] = 'x' }, func(b []byte) { binary.BigEndian.PutUint16(b[6:8], 1) }} {
		b := append([]byte(nil), q...)
		change(b)
		if _, err := a.Reply(b, time.Now()); err == nil {
			t.Fatal("accepted invalid query")
		}
	}
	if _, err := a.Reply(make([]byte, 4097), time.Now()); err == nil {
		t.Fatal("oversized query accepted")
	}
	if _, err := OpenDiscoveryAdvertiser("nexus-no-such-interface", a); err == nil {
		t.Fatal("unknown interface accepted")
	}
}
