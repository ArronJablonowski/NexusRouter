package remote

import (
	"crypto/x509"
	"encoding/binary"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// DiscoveryAdvertisement is public metadata derived from the loaded listener
// certificate, not from a remote claim. It never grants the recipient trust.
type DiscoveryAdvertisement struct {
	Instance    string
	Address     string
	ServerName  string
	SSHPort     int
	Certificate *x509.Certificate
}

func (a DiscoveryAdvertisement) records(now time.Time) ([]dnsmessage.Resource, error) {
	host, port, err := net.SplitHostPort(a.Address)
	number, e := strconv.Atoi(port)
	if err != nil || e != nil || a.Certificate == nil || len(a.Certificate.Raw) == 0 || a.Instance != strings.ToLower(a.Instance) {
		return nil, ErrInvalid
	}
	leaf, err := x509.ParseCertificate(a.Certificate.Raw)
	if err != nil || now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) || leaf.VerifyHostname(a.ServerName) != nil {
		return nil, ErrInvalid
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.Is4() {
		return nil, ErrInvalid
	}
	txt := []string{"v=1", "id=" + a.Instance, "name=" + a.ServerName, "pin=" + Fingerprint(leaf)}
	if a.SSHPort != 0 {
		txt = append(txt, "ssh="+strconv.Itoa(a.SSHPort))
	}
	if _, err := ParseDiscoveryCandidate(a.Instance, host, number, txt, 30, now); err != nil {
		return nil, err
	}
	name := func(value string) dnsmessage.Name { n, _ := dnsmessage.NewName(value); return n }
	instance := a.Instance + "." + DiscoveryService
	// The SRV hostname is protocol metadata, not the machine's private hostname.
	target := a.Instance + ".local."
	resource := func(owner string, body dnsmessage.ResourceBody) dnsmessage.Resource {
		return dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: name(owner), Class: dnsmessage.ClassINET, TTL: 30}, Body: body}
	}
	return []dnsmessage.Resource{
		resource(DiscoveryService, &dnsmessage.PTRResource{PTR: name(instance)}),
		resource(instance, &dnsmessage.SRVResource{Port: uint16(number), Target: name(target)}),
		resource(instance, &dnsmessage.TXTResource{TXT: txt}),
		resource(target, &dnsmessage.AResource{A: ip.As4()}),
	}, nil
}

// Reply builds a bounded legacy-unicast response to a NexusRouter PTR query.
// It does not open sockets. Other services, response packets, malformed messages
// and excessive records receive no response. The caller enforces source/TTL and
// rate limits and only sends to the original requesting address and port.
func (a DiscoveryAdvertisement) Reply(query []byte, now time.Time) ([]byte, error) {
	if len(query) < 12 || len(query) > 4096 || binary.BigEndian.Uint16(query[4:6]) != 1 || binary.BigEndian.Uint16(query[6:8]) != 0 || binary.BigEndian.Uint16(query[8:10]) != 0 || binary.BigEndian.Uint16(query[10:12]) != 0 {
		return nil, ErrInvalid
	}
	var q dnsmessage.Message
	if q.Unpack(query) != nil || q.Response || q.Truncated || q.OpCode != 0 || q.RCode != 0 || len(q.Questions) != 1 {
		return nil, ErrInvalid
	}
	question := q.Questions[0]
	if strings.ToLower(question.Name.String()) != DiscoveryService || question.Type != dnsmessage.TypePTR || question.Class != dnsmessage.ClassINET {
		return nil, ErrInvalid
	}
	records, err := a.records(now)
	if err != nil {
		return nil, err
	}
	response := dnsmessage.Message{Header: dnsmessage.Header{ID: q.ID, Response: true, Authoritative: true}, Questions: q.Questions, Answers: records[:1], Additionals: records[1:]}
	body, err := response.Pack()
	if err != nil || len(body) > 1400 {
		return nil, ErrInvalid
	}
	return body, nil
}
