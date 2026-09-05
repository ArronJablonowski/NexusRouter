package policy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

func TestLocalDestinationPolicy(t *testing.T) {
	for _, u := range []string{"https://example.com", "http://192.168.1.2:11434", "http://169.254.169.254", "http://127.0.0.1.example.com", "http://localhost.", "http://2130706433", "http://[fe80::1%25lo0]", "http://user:secret@localhost", "http://localhost/?secret=x"} {
		if tr, err := NewTransport(true, []string{u}); err == nil {
			tr.CloseIdleConnections()
			t.Fatalf("accepted %s", u)
		}
	}
	for _, u := range []string{"http://localhost:11434", "http://127.0.0.1:11434", "http://[::1]:11434", "http://[::ffff:127.0.0.1]:11434"} {
		tr, err := NewTransport(true, []string{u})
		if err != nil {
			t.Fatal(u, err)
		}
		tr.CloseIdleConnections()
	}
	if _, err := NewTransport(false, []string{"http://example.com"}); err == nil {
		t.Fatal("cleartext cloud endpoint accepted")
	}
}

func TestLocalTransportIgnoresProxyAndPinsLocalhost(t *testing.T) {
	proxyCalls := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyCalls++; w.WriteHeader(500) }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "local") }))
	defer server.Close()
	endpoint := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	tr, err := NewTransport(true, []string{endpoint})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.CloseIdleConnections()
	r, err := (&http.Client{Transport: tr}).Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)
	if string(body) != "local" || proxyCalls != 0 {
		t.Fatal("unexpected destination")
	}
	for _, u := range []string{server.URL, "https://example.com", "http://localhost:1"} {
		req, _ := http.NewRequest("GET", u, nil)
		if _, err := tr.RoundTrip(req); !errors.Is(err, ErrEgress) {
			t.Fatal(u, err)
		}
	}
	req, _ := http.NewRequest("GET", endpoint, nil)
	req.Host = "example.com"
	if _, err := tr.RoundTrip(req); !errors.Is(err, ErrEgress) {
		t.Fatal("Host override accepted")
	}
}

func TestProviderCannotFollowRedirect(t *testing.T) {
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls++ }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	tr, err := NewTransport(true, []string{server.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.CloseIdleConnections()
	p, err := providers.NewHTTP(server.URL, "ollama", "", tr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Models(context.Background()); err == nil || targetCalls != 0 {
		t.Fatal("redirect followed")
	}
}
