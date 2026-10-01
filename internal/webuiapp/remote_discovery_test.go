package webuiapp

import (
	"context"
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type discoveryFunc func(context.Context) ([]remote.DiscoveryCandidate, error)

func (f discoveryFunc) Discover(ctx context.Context) ([]remote.DiscoveryCandidate, error) {
	return f(ctx)
}
func TestRemoteDiscoveryAuthorityAndNoTrustMutation(t *testing.T) {
	h := mutationHandlerFixture(t, MutationServices{})
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	trust := remote.TrustFile(filepath.Join(dir, "peers.json"))
	original := remote.Registry{Version: 1, Peers: []remote.Peer{}}
	if err := trust.Replace(original, "absent"); err != nil {
		t.Fatal(err)
	}
	h.remoteTrustFile = string(trust)
	var calls atomic.Int32
	h.remoteDiscoverer = discoveryFunc(func(ctx context.Context) ([]remote.DiscoveryCandidate, error) {
		calls.Add(1)
		d, ok := ctx.Deadline()
		if !ok || time.Until(d) > 4*time.Second {
			t.Error("unbounded scan")
		}
		c, err := remote.ParseDiscoveryCandidate("node-a", "192.168.1.20", 8443, []string{"v=1", "id=node-a", "name=node-a.local", "pin=" + strings.Repeat("a", 64), "ssh=22"}, 30, time.Now())
		return []remote.DiscoveryCandidate{c}, err
	})
	call := func(r *http.Request, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("got %d want %d: %s", w.Code, want, w.Body.String())
		}
		return w
	}
	request := func(body string) *http.Request {
		return authorizedMutationRequest(t, h, "/app/api/v1/remote-discovery", body)
	}
	call(browserRequest("POST", "/app/api/v1/remote-discovery", `{"version":1}`), 401)
	r := request(`{"version":1}`)
	r.Header.Del("X-Darwin-CSRF")
	call(r, 403)
	r = request(`{"version":1}`)
	r.Header.Set("Origin", "https://evil.example")
	call(r, 403)
	for _, body := range []string{`{"version":1,"interface":"en0"}`, `{"version":1,"version":1}`, `{"version":2}`} {
		call(request(body), 400)
	}
	if calls.Load() != 0 {
		t.Fatal("unauthorized network discovery")
	}
	w := call(request(`{"version":1}`), 200)
	var page remoteDiscoveryPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || len(page.Candidates) != 1 || page.Candidates[0].Verified || page.Candidates[0].SSHPort != 22 {
		t.Fatal(page)
	}
	current, err := trust.Read()
	if err != nil || current.Digest() != original.Digest() {
		t.Fatal("discovery changed trust", err)
	}
	h.remoteDiscoverer = nil
	call(request(`{"version":1}`), 503)
}
func TestRemoteDiscoverySingleFlightAndCancellation(t *testing.T) {
	h := mutationHandlerFixture(t, MutationServices{})
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	trust := remote.TrustFile(filepath.Join(dir, "peers.json"))
	if err := trust.Replace(remote.Registry{Version: 1, Peers: []remote.Peer{}}, "absent"); err != nil {
		t.Fatal(err)
	}
	h.remoteTrustFile = string(trust)
	started := make(chan struct{})
	h.remoteDiscoverer = discoveryFunc(func(ctx context.Context) ([]remote.DiscoveryCandidate, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := authorizedMutationRequest(t, h, "/app/api/v1/remote-discovery", `{"version":1}`).WithContext(ctx)
	done := make(chan struct{})
	go func() { defer close(done); h.ServeHTTP(httptest.NewRecorder(), r) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("scan did not start")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, authorizedMutationRequest(t, h, "/app/api/v1/remote-discovery", `{"version":1}`))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "discovery_busy") {
		t.Fatal(w.Code, w.Body.String())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scan did not cancel")
	}
	if h.discoveryActive.Load() {
		t.Fatal("scan slot leaked")
	}
}
