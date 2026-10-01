package remote

import (
	"encoding/binary"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// DiscoveryQuery is a legacy-unicast-response DNS-SD browse query. The caller
// sends it from an ephemeral port and matches the randomized transaction ID.
func DiscoveryQuery(id uint16) ([]byte, error) {
	name, err := dnsmessage.NewName(DiscoveryService)
	if err != nil {
		return nil, err
	}
	m := dnsmessage.Message{Header: dnsmessage.Header{ID: id}, Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET}}}
	return m.Pack()
}

// DiscoveryPacketCandidates projects only complete PTR/SRV/TXT/address bundles
// in one bounded response. Incomplete records produce no candidate; callers do
// not resolve advertised hostnames through another DNS or task connection.
func DiscoveryPacketCandidates(packet []byte, id uint16, source netip.Addr, now time.Time) ([]DiscoveryCandidate, error) {
	if len(packet) < 12 || len(packet) > 9000 || !source.IsPrivate() || source.Is4In6() || source.Zone() != "" || now.IsZero() {
		return nil, ErrInvalid
	}
	total := 0
	for _, offset := range []int{6, 8, 10} {
		total += int(binary.BigEndian.Uint16(packet[offset : offset+2]))
	}
	if binary.BigEndian.Uint16(packet[4:6]) > 4 || total > 128 {
		return nil, ErrInvalid
	}
	var m dnsmessage.Message
	if m.Unpack(packet) != nil || m.ID != id || !m.Response || m.Truncated || m.OpCode != 0 || m.RCode != dnsmessage.RCodeSuccess {
		return nil, ErrInvalid
	}
	records := append(append(m.Answers, m.Authorities...), m.Additionals...)
	results := []DiscoveryCandidate{}
	for _, ptr := range records {
		target, ok := ptr.Body.(*dnsmessage.PTRResource)
		if !ok || strings.ToLower(ptr.Header.Name.String()) != DiscoveryService || !discoveryClass(ptr.Header.Class) || ptr.Header.TTL == 0 {
			continue
		}
		fqdn := strings.ToLower(target.PTR.String())
		suffix := "." + DiscoveryService
		if !strings.HasSuffix(fqdn, suffix) {
			continue
		}
		instance := strings.TrimSuffix(fqdn, suffix)
		ttl := ptr.Header.TTL
		var srv *dnsmessage.SRVResource
		var txt *dnsmessage.TXTResource
		ambiguous := false
		for _, r := range records {
			if strings.ToLower(r.Header.Name.String()) != fqdn || !discoveryClass(r.Header.Class) {
				continue
			}
			switch value := r.Body.(type) {
			case *dnsmessage.SRVResource:
				if srv != nil {
					ambiguous = true
				}
				srv = value
				ttl = min(ttl, r.Header.TTL)
			case *dnsmessage.TXTResource:
				if txt != nil {
					ambiguous = true
				}
				txt = value
				ttl = min(ttl, r.Header.TTL)
			}
		}
		if ambiguous || srv == nil || txt == nil || srv.Port == 0 || srv.Priority != 0 || srv.Weight != 0 {
			continue
		}
		addressMatched := false
		for _, r := range records {
			if strings.ToLower(r.Header.Name.String()) != strings.ToLower(srv.Target.String()) || !discoveryClass(r.Header.Class) {
				continue
			}
			var address netip.Addr
			switch value := r.Body.(type) {
			case *dnsmessage.AResource:
				address = netip.AddrFrom4(value.A)
			case *dnsmessage.AAAAResource:
				address = netip.AddrFrom16(value.AAAA)
			default:
				continue
			}
			if address == source {
				addressMatched = true
				ttl = min(ttl, r.Header.TTL)
			}
		}
		if !addressMatched {
			continue
		}
		candidate, err := ParseDiscoveryCandidate(instance, source.String(), int(srv.Port), txt.TXT, ttl, now)
		if err == nil {
			results = append(results, candidate)
		}
		if len(results) > 32 {
			return nil, ErrUnavailable
		}
	}
	return results, nil
}
func discoveryClass(class dnsmessage.Class) bool {
	return uint16(class)&0x7fff == uint16(dnsmessage.ClassINET)
}
