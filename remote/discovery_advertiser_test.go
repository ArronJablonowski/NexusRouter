package remote

import (
	"context"
	"errors"
	"golang.org/x/net/ipv4"
	"net"
	"testing"
	"time"
)

func TestAdvertiserCancellationClosesBlockedReader(t *testing.T) {
	// A loopback socket exercises cancellation without joining any multicast group
	// or sending an advertisement onto a real interface.
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	a := &DiscoveryAdvertiser{conn: conn, packet: ipv4.NewPacketConn(conn)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	deadline := time.After(time.Second)
	for !a.running.Load() {
		select {
		case <-deadline:
			t.Fatal("reader did not start")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("reader did not stop")
	}
	if _, err := conn.WriteToUDP([]byte("x"), conn.LocalAddr().(*net.UDPAddr)); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("socket not closed: %v", err)
	}
	if err := a.Run(context.Background()); !errors.Is(err, ErrInvalid) {
		t.Fatal("advertiser restarted", err)
	}
}
func TestAdvertiserRejectsUninitializedInstance(t *testing.T) {
	var a DiscoveryAdvertiser
	if err := a.Run(context.Background()); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
}
