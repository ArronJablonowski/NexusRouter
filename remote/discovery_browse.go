package remote

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"sort"
	"time"

	"golang.org/x/net/ipv4"
)

// DiscoverUnpaired performs one explicitly requested IPv4 LAN browse on one
// named interface. It never pairs, probes TLS, sends credentials or routes work.
// Only complete responses from the advertised private address are projected.
func DiscoverUnpaired(ctx context.Context, interfaceName string, wait time.Duration) ([]DiscoveryCandidate, error) {
	if ctx == nil || ctx.Err() != nil || interfaceName == "" || wait <= 0 || wait > 10*time.Second {
		return nil, ErrInvalid
	}
	iface, err := net.InterfaceByName(interfaceName)
	if err != nil || iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 || iface.Flags&net.FlagLoopback != 0 {
		return nil, ErrInvalid
	}
	addresses, err := iface.Addrs()
	if err != nil {
		return nil, ErrUnavailable
	}
	var local netip.Addr
	for _, address := range addresses {
		prefix, e := netip.ParsePrefix(address.String())
		if e == nil && prefix.Addr().Is4() && prefix.Addr().IsPrivate() {
			local = prefix.Addr()
			break
		}
	}
	if !local.IsValid() {
		return nil, ErrDenied
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IP(local.AsSlice())})
	if err != nil {
		return nil, ErrUnavailable
	}
	defer conn.Close()
	packet := ipv4.NewPacketConn(conn)
	if packet.SetMulticastInterface(iface) != nil || packet.SetMulticastTTL(255) != nil || packet.SetControlMessage(ipv4.FlagInterface|ipv4.FlagTTL, true) != nil {
		return nil, ErrUnavailable
	}
	var nonce [2]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return nil, ErrUnavailable
	}
	id := binary.BigEndian.Uint16(nonce[:])
	query, err := DiscoveryQuery(id)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	if conn.SetDeadline(deadline) != nil {
		return nil, ErrUnavailable
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if _, err = packet.WriteTo(query, &ipv4.ControlMessage{IfIndex: iface.Index}, &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}); err != nil {
		return nil, ErrUnavailable
	}
	seen := map[string]DiscoveryCandidate{}
	conflicts := map[string]bool{}
	buffer := make([]byte, 9001)
	for count := 0; count < 256; count++ {
		n, control, sender, e := packet.ReadFrom(buffer)
		if e != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			var timeout net.Error
			if errors.As(e, &timeout) && timeout.Timeout() {
				results := []DiscoveryCandidate{}
				now := time.Now()
				for key, candidate := range seen {
					if !conflicts[key] && candidate.ExpiresAt.After(now) {
						results = append(results, candidate)
					}
				}
				sort.Slice(results, func(i, j int) bool { return results[i].Instance < results[j].Instance })
				return results, nil
			}
			return nil, ErrUnavailable
		}
		from, ok := sender.(*net.UDPAddr)
		if !ok || from.Port != 5353 || control == nil || control.IfIndex != iface.Index || control.TTL != 255 {
			continue
		}
		source, ok := netip.AddrFromSlice(from.IP)
		if !ok {
			continue
		}
		source = source.Unmap()
		candidates, e := DiscoveryPacketCandidates(buffer[:n], id, source, time.Now())
		if e != nil {
			continue
		}
		for _, candidate := range candidates {
			key := candidate.Instance
			if old, ok := seen[key]; ok && (old.Endpoint != candidate.Endpoint || old.ServerName != candidate.ServerName || old.ClaimedCertificateSHA256 != candidate.ClaimedCertificateSHA256 || old.SSHPort != candidate.SSHPort) {
				conflicts[key] = true
			}
			seen[key] = candidate
			if len(seen) > 64 {
				return nil, ErrUnavailable
			}
		}
	}
	return nil, ErrUnavailable
}
