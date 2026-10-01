package remote

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

type fakeBackend struct {
	mu      sync.Mutex
	tasks   map[string]submissions.Status
	creates int
	lost    bool
}

func (b *fakeBackend) Info(context.Context) (Info, error) {
	return Info{Version: 1, Available: true, Models: []Model{{ID: "chat", Model: "fixture", Provider: "local", Harness: "nexus", Local: true, ContextTokens: 32768}}}, nil
}
func (b *fakeBackend) Submit(_ context.Context, key string, _ Task) (submissions.Status, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tasks == nil {
		b.tasks = map[string]submissions.Status{}
	}
	if s, ok := b.tasks[key]; ok {
		return s, nil
	}
	b.creates++
	s := submissions.Status{Version: 1, ID: key, State: "queued", TaskIDs: []string{"task-owned"}}
	b.tasks[key] = s
	if b.lost {
		b.lost = false
		return submissions.Status{}, ErrUnavailable
	}
	return s, nil
}
func (b *fakeBackend) Status(_ context.Context, key string) (submissions.Status, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.tasks[key]
	if !ok {
		return s, ErrUnavailable
	}
	return s, nil
}
func (b *fakeBackend) Cancel(ctx context.Context, key string) (submissions.Status, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.tasks[key]
	if !ok {
		return s, ErrUnavailable
	}
	s.State = "canceled"
	s.CancelRequested = true
	b.tasks[key] = s
	return s, nil
}
func (b *fakeBackend) Events(context.Context, string, int64, int) (sessions.EventPage, error) {
	return sessions.EventPage{}, nil
}

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newCA(t *testing.T) testCA {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	c := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, e := x509.CreateCertificate(rand.Reader, c, c, &k.PublicKey, k)
	if e != nil {
		t.Fatal(e)
	}
	c, _ = x509.ParseCertificate(der)
	return testCA{c, k, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}
func (c testCA) leaf(t *testing.T, name string) (Credentials, string) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: name}, DNSNames: []string{name}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, e := x509.CreateCertificate(rand.Reader, cert, c.cert, &k.PublicKey, c.key)
	if e != nil {
		t.Fatal(e)
	}
	parsed, _ := x509.ParseCertificate(der)
	key, _ := x509.MarshalPKCS8PrivateKey(k)
	dir := t.TempDir()
	creds := Credentials{filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"), filepath.Join(dir, "ca.pem")}
	for p, b := range map[string][]byte{creds.CertificateFile: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), creds.KeyFile: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), creds.CAFile: c.pem} {
		if e = os.WriteFile(p, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	return creds, Fingerprint(parsed)
}
func writeRegistry(t *testing.T, path string, peers ...Peer) {
	t.Helper()
	body, _ := json.Marshal(Registry{Version: 1, Peers: peers})
	if e := os.WriteFile(path, body, 0600); e != nil {
		t.Fatal(e)
	}
}
func testPeer(id, pin, endpoint string) Peer {
	return Peer{ID: id, Endpoint: endpoint, ServerName: id, Pins: []string{pin}, Operations: []string{"info", "dispatch", "inspect", "cancel"}, Models: []string{"chat"}, AllowPrivate: true, MaxCost: 1, MaxContextTokens: 32768}
}
func testTask() Task {
	return Task{Version: 1, ModelID: "chat", Prompt: "private test prompt", Domain: "coding", Profile: "tests-v1", ContextTokens: 4096, Private: true}
}

type fixture struct {
	client                   *Client
	server                   *Server
	http                     *http.Server
	journal                  *Journal
	backend                  *fakeBackend
	clientPeer, serverPeer   Peer
	serverTrust, clientTrust string
	ca                       testCA
	serverCreds              Credentials
	url                      string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ca := newCA(t)
	sc, sp := ca.leaf(t, "node-a")
	cc, cp := ca.leaf(t, "node-b")
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	endpoint := "https://" + ln.Addr().String()
	dir := t.TempDir()
	st, ct := filepath.Join(dir, "server.json"), filepath.Join(dir, "client.json")
	serverPeer, clientPeer := testPeer("node-a", sp, endpoint), testPeer("node-b", cp, "https://127.0.0.1:443")
	writeRegistry(t, st, clientPeer)
	writeRegistry(t, ct, serverPeer)
	journal, e := OpenJournal(filepath.Join(dir, "journal"), "node-a")
	if e != nil {
		t.Fatal(e)
	}
	backend := &fakeBackend{}
	server, e := NewServer("node-a", TrustFile(st), journal, backend)
	if e != nil {
		t.Fatal(e)
	}
	httpServer, e := server.HTTPServer(ln.Addr().String(), sc)
	if e != nil {
		t.Fatal(e)
	}
	go httpServer.ServeTLS(ln, "", "")
	t.Cleanup(func() { httpServer.Close(); journal.Close() })
	return &fixture{&Client{TrustFile(ct), cc}, server, httpServer, journal, backend, clientPeer, serverPeer, st, ct, ca, sc, endpoint}
}
func TestMutualTLSLifecycleAndDurableOwnership(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	info, e := f.client.Info(ctx, "node-a")
	if e != nil || !info.Available || len(info.Models) != 1 {
		t.Fatal(info, e)
	}
	key := "request-0000000001"
	s, e := f.client.Dispatch(ctx, "node-a", key, testTask())
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			retry, err := f.client.Dispatch(ctx, "node-a", key, testTask())
			if err != nil || retry.ID != s.ID {
				t.Error(retry, err)
			}
		}()
	}
	wg.Wait()
	if f.backend.creates != 1 {
		t.Fatal("duplicate dispatch")
	}
	changed := testTask()
	changed.Prompt = "different"
	if _, e = f.client.Dispatch(ctx, "node-a", key, changed); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	// A different authenticated peer may not inspect or cancel this caller's key.
	c2, pin := f.ca.leaf(t, "node-c")
	peer2 := testPeer("node-c", pin, "https://127.0.0.1:443")
	writeRegistry(t, f.serverTrust, f.clientPeer, peer2)
	other := &Client{f.client.Trust, c2}
	if _, e = other.Status(ctx, "node-a", key); e == nil {
		t.Fatal("cross-peer status")
	}
	if _, e = other.Cancel(ctx, "node-a", key); e == nil {
		t.Fatal("cross-peer cancel")
	}
	if _, e = f.client.Events(ctx, "node-a", key, "unowned-task", 0); !errors.Is(e, ErrDenied) {
		t.Fatal("cross-task events", e)
	}
	canceled, e := f.client.Cancel(ctx, "node-a", key)
	if e != nil || !canceled.CancelRequested {
		t.Fatal(canceled, e)
	}
	var audits int
	if e = f.journal.db.QueryRow("SELECT count(*) FROM audit WHERE caller='node-b' AND destination='node-a' AND outcome='succeeded'").Scan(&audits); e != nil || audits < 3 {
		t.Fatal(audits, e)
	}
}
func TestLostBindingRetryAndJournalReopen(t *testing.T) {
	f := setup(t)
	f.backend.lost = true
	ctx := context.Background()
	key := "request-0000000002"
	if _, e := f.client.Dispatch(ctx, "node-a", key, testTask()); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	// Response/binding lost after durable intake; retry reuses the same backend key.
	s, e := f.client.Dispatch(ctx, "node-a", key, testTask())
	if e != nil || f.backend.creates != 1 {
		t.Fatal(s, e)
	}
	directory := filepath.Dir(f.serverTrust) + "/journal"
	if e = f.journal.Close(); e != nil {
		t.Fatal(e)
	}
	j, e := OpenJournal(directory, "node-a")
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	f.server.journal = j
	got, e := f.client.Status(ctx, "node-a", key)
	if e != nil || got.ID != s.ID {
		t.Fatal(got, e)
	}
	if _, e = OpenJournal(directory, "another-node"); !errors.Is(e, ErrConflict) {
		t.Fatal("journal identity changed", e)
	}
}
func TestRevocationScopesPinsAndPrivacy(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	writeRegistry(t, f.serverTrust)
	if _, e := f.client.Info(ctx, "node-a"); e == nil {
		t.Fatal("revoked client accepted")
	}
	writeRegistry(t, f.serverTrust, f.clientPeer)
	p := f.serverPeer
	p.Pins = []string{f.clientPeer.Pins[0]}
	writeRegistry(t, f.clientTrust, p)
	if _, e := f.client.Info(ctx, "node-a"); e == nil {
		t.Fatal("wrong server pin")
	}
	writeRegistry(t, f.clientTrust, f.serverPeer)
	p = f.clientPeer
	p.Operations = []string{"info"}
	writeRegistry(t, f.serverTrust, p)
	if _, e := f.client.Dispatch(ctx, "node-a", "request-0000000003", testTask()); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	writeRegistry(t, f.serverTrust, f.clientPeer)
	task := testTask()
	task.Private = false
	if _, e := f.client.Dispatch(ctx, "node-a", "request-0000000003", task); !errors.Is(e, ErrDenied) {
		t.Fatal("cloud authorization widened", e)
	}
	p = f.serverPeer
	p.AllowPrivate = false
	writeRegistry(t, f.clientTrust, p)
	if _, e := f.client.Dispatch(ctx, "node-a", "request-0000000003", testTask()); !errors.Is(e, ErrDenied) {
		t.Fatal("private data left source", e)
	}
	for _, endpoint := range []string{"http://127.0.0.1:443", "https://localhost:443", "https://8.8.8.8:443", "https://127.0.0.1:443/path", "https://user@127.0.0.1:443", "https://[::ffff:8.8.8.8]:443"} {
		p = f.serverPeer
		p.Endpoint = endpoint
		if p.Validate() == nil {
			t.Fatal("unsafe endpoint", endpoint)
		}
	}
	if f.backend.creates != 0 {
		t.Fatal("denied execution reached backend")
	}
}
func TestRevocationOnReusedTLSConnection(t *testing.T) {
	f := setup(t)
	cfg, e := f.client.Credentials.clientTLS(f.serverPeer)
	if e != nil {
		t.Fatal(e)
	}
	transport := &http.Transport{TLSClientConfig: cfg}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	call := func() int {
		req, _ := http.NewRequest("GET", f.url+"/v1/remote/info", nil)
		req.Header.Set("X-Nexus-Instance", "node-a")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var v any
		json.NewDecoder(res.Body).Decode(&v)
		return res.StatusCode
	}
	if call() != 200 {
		t.Fatal("initial authentication")
	}
	writeRegistry(t, f.serverTrust)
	if call() != 403 {
		t.Fatal("revocation ignored on live TLS connection")
	}
}
func TestAuditFailurePreventsDispatch(t *testing.T) {
	f := setup(t)
	f.journal.Close()
	if _, e := f.client.Dispatch(context.Background(), "node-a", "request-0000000004", testTask()); e == nil || f.backend.creates != 0 {
		t.Fatal("audit failure dispatched", e)
	}
}
func TestTLSRequiresTrustedClientCertificate(t *testing.T) {
	f := setup(t)
	cert, roots, e := f.serverCreds.load()
	if e != nil {
		t.Fatal(e)
	}
	_ = cert
	cfg := &tls.Config{RootCAs: roots, ServerName: "node-a", MinVersion: tls.VersionTLS13}
	tr := &http.Transport{TLSClientConfig: cfg}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr}
	if _, e = client.Get(f.url + "/v1/remote/info"); e == nil {
		t.Fatal("missing client certificate accepted")
	}
}
