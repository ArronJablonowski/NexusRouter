package policy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

// Exercise the actual provider adapter in hybrid/cloud-capable transport mode.
// DNS/connection trace events observe the real dial path without replacing it.
// Every listening endpoint belongs to this test; no external collector or model
// is contacted, and the credential is an intentionally synthetic fixture.
func TestCloudCapableTransportPinsProviderLoopbackWithoutDNSOrProxy(t *testing.T) {
	var proxyCalls atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(name, proxy.URL)
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	for _, hostname := range []string{"localhost", "LoCaLhOsT", "127.0.0.1", "::ffff:127.0.0.1"} {
		t.Run(hostname, func(t *testing.T) {
			const token = "synthetic-owned-provider-bearer"
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet || r.URL.RequestURI() != "/api/tags" || r.Header.Get("Authorization") != "Bearer "+token {
					t.Error("provider request did not preserve path/method/credential")
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"models":[{"name":"owned-local-model"}]}`)
			}))
			defer server.Close()
			u, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			endpoint := "http://" + net.JoinHostPort(hostname, u.Port())
			transport, err := NewTransport(false, []string{endpoint})
			if err != nil {
				t.Fatal(err)
			}
			defer transport.CloseIdleConnections()
			provider, err := providers.NewHTTP(endpoint, "ollama", token, transport)
			if err != nil {
				t.Fatal(err)
			}
			var dnsCalls, connections, unpinnedConnections atomic.Int64
			trace := &httptrace.ClientTrace{
				DNSStart: func(httptrace.DNSStartInfo) { dnsCalls.Add(1) },
				ConnectStart: func(network, address string) {
					connections.Add(1)
					if address != net.JoinHostPort("127.0.0.1", u.Port()) {
						unpinnedConnections.Add(1)
					}
				},
			}
			ctx, cancel := context.WithTimeout(httptrace.WithClientTrace(context.Background(), trace), 3*time.Second)
			defer cancel()
			models, err := provider.Models(ctx)
			if err != nil || !reflect.DeepEqual(models, []string{"owned-local-model"}) {
				t.Fatal("owned provider discovery failed", models, err)
			}
			if requests.Load() != 1 || connections.Load() != 1 || dnsCalls.Load() != 0 || unpinnedConnections.Load() != 0 {
				t.Fatal("provider loopback was not pinned", requests.Load(), connections.Load(), dnsCalls.Load(), unpinnedConnections.Load())
			}
		})
	}
	if proxyCalls.Load() != 0 {
		t.Fatal("provider credential request reached proxy", proxyCalls.Load())
	}
}
