// Package policy implements runtime security boundaries.
package policy

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var ErrEgress = errors.New("network destination denied by policy")

// Transport owns its connection pool. LocalOnly permits literal loopback IPs
// and localhost (pinned to 127.0.0.1), never DNS, LAN addresses or proxies.
// This protects runtime HTTP traffic, not arbitrary code running in-process.
type Transport struct {
	inner     *http.Transport
	origins   map[string]bool
	localOnly bool
}

func NewTransport(localOnly bool, endpoints []string) (*Transport, error) {
	if len(endpoints) == 0 {
		return nil, ErrEgress
	}
	origins := map[string]bool{}
	for _, endpoint := range endpoints {
		u, err := url.Parse(endpoint)
		if err != nil || !validURL(u) {
			return nil, ErrEgress
		}
		if localOnly {
			if _, ok := localAddress(u.Hostname()); !ok {
				return nil, ErrEgress
			}
		} else if u.Scheme != "https" {
			if _, ok := localAddress(u.Hostname()); !ok {
				return nil, ErrEgress
			}
		}
		origins[origin(u)] = true
	}
	t := &Transport{origins: origins, localOnly: localOnly}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	t.inner = &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 60 * time.Second, IdleConnTimeout: 90 * time.Second, MaxIdleConns: 20, MaxConnsPerHost: 8, MaxResponseHeaderBytes: 1 << 20}
	t.inner.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" && network != "tcp4" && network != "tcp6" {
			return nil, ErrEgress
		}
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, ErrEgress
		}
		if localOnly {
			ip, ok := localAddress(host)
			if !ok {
				return nil, ErrEgress
			}
			address = net.JoinHostPort(ip, port)
		}
		return dialer.DialContext(ctx, network, address)
	}
	return t, nil
}

func (t *Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r == nil || !validURL(r.URL) || !t.origins[origin(r.URL)] || (r.Host != "" && r.Host != r.URL.Host) || r.Method == http.MethodConnect {
		return nil, ErrEgress
	}
	if t.localOnly {
		if _, ok := localAddress(r.URL.Hostname()); !ok {
			return nil, ErrEgress
		}
	}
	return t.inner.RoundTrip(r)
}
func (t *Transport) CloseIdleConnections() { t.inner.CloseIdleConnections() }

func validURL(u *url.URL) bool {
	return u != nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil && u.Opaque == "" && u.Fragment == "" && u.RawQuery == ""
}
func origin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return u.Scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}
func localAddress(host string) (string, bool) {
	if strings.EqualFold(host, "localhost") {
		return "127.0.0.1", true
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.Zone() != "" || !ip.Unmap().IsLoopback() {
		return "", false
	}
	return ip.Unmap().String(), true
}
