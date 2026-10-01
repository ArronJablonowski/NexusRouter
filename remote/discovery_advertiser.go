package remote

import (
	"context"
	"net"
	"net/netip"
	"sync/atomic"
	"time"

	"golang.org/x/net/ipv4"
)

// DiscoveryAdvertiser answers bounded explicit discovery queries on one owned
// interface. It is opt-in, sends no unsolicited announcements and supports only
// legacy unicast queries (ephemeral source port), not a general mDNS responder.
type DiscoveryAdvertiser struct {
	conn          *net.UDPConn
	packet        *ipv4.PacketConn
	iface         int
	address       net.IP
	advertisement DiscoveryAdvertisement
	running       atomic.Bool
}

func OpenDiscoveryAdvertiser(interfaceName string, a DiscoveryAdvertisement) (*DiscoveryAdvertiser, error) {
	if _, err := a.records(time.Now()); err != nil {
		return nil, err
	}
	if interfaceName == "" {
		return nil, ErrInvalid
	}
	iface, err := net.InterfaceByName(interfaceName)
	if err != nil || iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 || iface.Flags&net.FlagLoopback != 0 {
		return nil, ErrInvalid
	}
	host, _, _ := net.SplitHostPort(a.Address)
	ip := netip.MustParseAddr(host)
	addresses, err := iface.Addrs()
	if err != nil {
		return nil, ErrUnavailable
	}
	owned := false
	for _, address := range addresses {
		prefix, e := netip.ParsePrefix(address.String())
		if e == nil && prefix.Addr() == ip {
			owned = true
		}
	}
	if !owned {
		return nil, ErrDenied
	}
	conn, err := net.ListenMulticastUDP("udp4", iface, &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353})
	if err != nil {
		return nil, ErrUnavailable
	}
	packet := ipv4.NewPacketConn(conn)
	if packet.SetControlMessage(ipv4.FlagInterface|ipv4.FlagTTL, true) != nil || packet.SetTTL(255) != nil {
		conn.Close()
		return nil, ErrUnavailable
	}
	return &DiscoveryAdvertiser{conn: conn, packet: packet, iface: iface.Index, address: net.IP(ip.AsSlice()), advertisement: a}, nil
}
func (a *DiscoveryAdvertiser) Close() error {
	if a == nil || a.conn == nil {
		return nil
	}
	return a.conn.Close()
}
func (a *DiscoveryAdvertiser) Run(ctx context.Context) error {
	if a == nil || a.conn == nil || a.packet == nil || ctx == nil || ctx.Err() != nil || !a.running.CompareAndSwap(false, true) {
		return ErrInvalid
	}
	stop := context.AfterFunc(ctx, func() { a.Close() })
	defer stop()
	defer a.Close()
	window := time.Now()
	responses := 0
	buffer := make([]byte, 4097)
	for {
		n, control, sender, err := a.packet.ReadFrom(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrUnavailable
		}
		from, ok := sender.(*net.UDPAddr)
		if !ok || from.Port == 5353 || from.Port == 0 || control == nil || control.IfIndex != a.iface || control.TTL != 255 {
			continue
		}
		ip, ok := netip.AddrFromSlice(from.IP)
		if !ok || !ip.Unmap().IsPrivate() {
			continue
		}
		now := time.Now()
		if now.Sub(window) >= time.Second {
			window = now
			responses = 0
		}
		if responses >= 8 {
			continue
		}
		reply, err := a.advertisement.Reply(buffer[:n], now)
		if err != nil {
			continue
		}
		responses++
		if _, err = a.packet.WriteTo(reply, &ipv4.ControlMessage{IfIndex: a.iface, Src: a.address}, from); err != nil {
			return ErrUnavailable
		}
	}
}
