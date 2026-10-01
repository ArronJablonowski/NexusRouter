package webuiapp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/remote"
)

func TestRemoteMembershipAuthorityAndAtomicChanges(t *testing.T) {
	h := mutationHandlerFixture(t, MutationServices{})
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	trust := remote.TrustFile(filepath.Join(dir, "peers.json"))
	h.remoteTrustFile = string(trust)
	original := remote.Registry{Version: 1, Peers: []remote.Peer{}}
	if err := trust.Replace(original, "absent"); err != nil {
		t.Fatal(err)
	}
	peer := remote.Peer{ID: "node-a", Endpoint: "https://127.0.0.1:8443", ServerName: "node-a", Pins: []string{strings.Repeat("a", 64)}, Operations: []string{"info", "inspect"}, Models: []string{"model-a"}, MaxContextTokens: 32768}
	input := membershipRequest{Version: 1, Action: "pair", ExpectedDigest: original.Digest(), IdentityVerified: true, Peer: &peer}
	body, _ := json.Marshal(input)
	request := func(body []byte) *http.Request {
		return authorizedMutationRequest(t, h, "/app/api/v1/remote-membership", string(body))
	}
	call := func(r *http.Request, want int) membershipPage {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("status %d want %d: %s", w.Code, want, w.Body.String())
		}
		var p membershipPage
		if want == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}
	call(browserRequest(http.MethodPost, "/app/api/v1/remote-membership", string(body)), 401)
	noCSRF := request(body)
	noCSRF.Header.Del("X-Darwin-CSRF")
	call(noCSRF, 403)
	foreign := request(body)
	foreign.Header.Set("Origin", "https://evil.example")
	call(foreign, 403)
	unverified := input
	unverified.IdentityVerified = false
	bad, _ := json.Marshal(unverified)
	call(request(bad), 400)
	duplicate := strings.Replace(string(body), `"version":1`, `"version":1,"version":1`, 1)
	call(request([]byte(duplicate)), 400)
	unknown := strings.TrimSuffix(string(body), "}") + `,"path":"/another/file"}`
	call(request([]byte(unknown)), 400)
	unchanged, err := trust.Read()
	if err != nil || unchanged.Digest() != original.Digest() {
		t.Fatal("rejected inputs changed trust")
	}
	paired := call(request(body), 200)
	if !paired.Enabled || len(paired.Registry.Peers) != 1 || paired.Registry.Peers[0].ID != peer.ID {
		t.Fatal(paired)
	}
	call(request(body), 409)
	cookie, _ := authenticateBrowser(t, h)
	get := browserGET("/app/api/v1/remote-membership", cookie)
	page := call(get, 200)
	if page.BackgroundReviewEnabled {
		t.Fatal("background review enabled without queue")
	}
	queue, err := remote.OpenReviewQueue(filepath.Join(dir, "review-queue"))
	if err != nil {
		t.Fatal(err)
	}
	h.remoteAutomatic = &RecordedRemoteAutomatic{ReviewQueue: queue}
	if !call(browserGET("/app/api/v1/remote-membership", cookie), 200).BackgroundReviewEnabled {
		t.Fatal("queue flag missing")
	}
	if page.Digest != paired.Digest {
		t.Fatal("read digest drift")
	}
	// A second scoped SSH member is retained exactly when the first is revoked.
	ssh := peer
	ssh.ID = "node-b"
	ssh.ServerName = "node-b"
	ssh.Pins = []string{strings.Repeat("b", 64)}
	ssh.Transport = "ssh"
	ssh.SSH = &remote.SSH{User: "nexus", Port: 22, IdentityFile: filepath.Join(dir, "key"), KnownHostsFile: filepath.Join(dir, "known_hosts")}
	input.Peer = &ssh
	input.ExpectedDigest = page.Digest
	body, _ = json.Marshal(input)
	both := call(request(body), 200)
	revoke := membershipRequest{Version: 1, Action: "revoke", Instance: peer.ID, ExpectedDigest: both.Digest}
	body, _ = json.Marshal(revoke)
	remaining := call(request(body), 200)
	if len(remaining.Registry.Peers) != 1 || remaining.Registry.Peers[0].Transport != "ssh" || remaining.Registry.Peers[0].ID != ssh.ID {
		t.Fatal(remaining)
	}
	call(request(body), 409)
	saved, err := trust.Read()
	if err != nil || saved.Digest() != remaining.Digest {
		t.Fatal("disk mismatch")
	}
}

func TestRemoteMembershipDisabledAndUnsafeRegistry(t *testing.T) {
	h := mutationHandlerFixture(t, MutationServices{})
	cookie, _ := authenticateBrowser(t, h)
	get := func() *httptest.ResponseRecorder {
		r := browserGET("/app/api/v1/remote-membership", cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := get()
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
	dir := t.TempDir()
	h.remoteTrustFile = filepath.Join(dir, "missing.json")
	if get().Code != 503 {
		t.Fatal("missing registry exposed")
	}
	if _, err := os.Stat(h.remoteTrustFile); !os.IsNotExist(err) {
		t.Fatal("read created registry")
	}
	if err := os.WriteFile(h.remoteTrustFile, []byte(`{"version":1,"peers":[]}`), 0644); err != nil {
		t.Fatal(err)
	}
	if get().Code != 503 {
		t.Fatal("public registry exposed")
	}
	h.remoteTrustFile = "relative.json"
	if get().Code != 503 {
		t.Fatal("relative path accepted")
	}
}
