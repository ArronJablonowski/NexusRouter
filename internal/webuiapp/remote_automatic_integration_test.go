package webuiapp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

// Only execution is synthetic. Server, trust, discovery, ranking, durable
// choice/binding, mTLS and browser API handlers are production implementations.
type browserRemoteExecution struct {
	mu                  sync.Mutex
	tasks               map[string]submissions.Status
	received            remote.Task
	events              []runtime.Event
	creates, catalogues atomic.Int32
	unavailable         atomic.Bool
	identity            harness.Identity
}

func (b *browserRemoteExecution) Info(context.Context) (remote.Info, error) {
	cost := 0.0
	return remote.Info{Version: 1, Available: true, Models: []remote.Model{{ID: "chat", Provider: "fixture", Model: "model", Local: true, ContextTokens: 32768, EstimatedCost: &cost}}, Harnesses: []remote.Harness{{ID: "pair", ModelID: "chat", Kind: "pi", ModelRevision: "revision"}}}, nil
}
func (b *browserRemoteExecution) Catalogue(ctx context.Context, _ []string, _ bool, _ []string) (remote.Info, error) {
	b.catalogues.Add(1)
	if b.unavailable.Load() {
		return remote.Info{}, remote.ErrUnavailable
	}
	return b.Info(ctx)
}
func (b *browserRemoteExecution) HarnessIdentity(context.Context, remote.HarnessIdentityRequest, bool) (harness.Identity, error) {
	return b.identity, nil
}
func (b *browserRemoteExecution) HarnessReadiness(context.Context, remote.HarnessIdentityRequest, bool) (harness.Readiness, error) {
	return harness.Readiness{Identity: b.identity, ExecutableMatched: true, CredentialState: "not_required", ModelState: "present", Compatible: true, Local: true, ContextTokens: 32768}, nil
}
func (b *browserRemoteExecution) HarnessCapacity(context.Context, remote.HarnessIdentityRequest, bool) (harness.Identity, resources.Need, resources.CapacityResult, error) {
	now := time.Now().UTC()
	return b.identity, resources.Need{RAM: 100}, resources.CapacityResult{Version: 1, Action: resources.CapacityAdmit, Reason: resources.CapacityAvailable, SnapshotTime: now, ObservedAt: now, Headroom: resources.CapacityHeadroom{RAMBytes: 1000}, MaxAdditional: 1}, nil
}
func (b *browserRemoteExecution) Submit(_ context.Context, key string, task remote.Task) (submissions.Status, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s, ok := b.tasks[key]; ok {
		return s, nil
	}
	b.creates.Add(1)
	b.received = task
	s := submissions.Status{Version: 1, ID: key, State: "queued"}
	b.tasks[key] = s
	return s, nil
}
func (b *browserRemoteExecution) Status(_ context.Context, key string) (submissions.Status, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.tasks[key]
	if !ok {
		return s, remote.ErrUnavailable
	}
	return s, nil
}
func (b *browserRemoteExecution) Cancel(ctx context.Context, key string) (submissions.Status, error) {
	return b.Status(ctx, key)
}
func (b *browserRemoteExecution) Events(_ context.Context, task string, after int64, limit int) (sessions.EventPage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if task != "review-task" || after < 0 || after > int64(len(b.events)) || limit < 1 || len(b.events) == 0 {
		return sessions.EventPage{}, remote.ErrUnavailable
	}
	end := min(int64(len(b.events)), after+int64(limit))
	return sessions.EventPage{Version: 1, TaskID: task, SessionID: "review-session", State: "completed", FromSequence: after, NextSequence: end, HeadSequence: int64(len(b.events)), HasMore: end < int64(len(b.events)), Events: b.events[after:end]}, nil
}

func browserRemoteCredentials(t *testing.T, dir string) (remote.Credentials, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"node-a"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	c := remote.Credentials{CertificateFile: filepath.Join(dir, "cert.pem"), KeyFile: filepath.Join(dir, "key.pem"), CAFile: filepath.Join(dir, "cert.pem")}
	for path, block := range map[string]*pem.Block{c.CertificateFile: {Type: "CERTIFICATE", Bytes: der}, c.KeyFile: {Type: "EC PRIVATE KEY", Bytes: priv}} {
		if err = os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return c, remote.Fingerprint(cert)
}
func TestBrowserAutomaticProductionDiscoveryDispatchAndRecovery(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	creds, pin := browserRemoteCredentials(t, dir)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	peer := remote.Peer{ID: "node-a", Endpoint: "https://" + listener.Addr().String(), ServerName: "node-a", Pins: []string{pin}, Operations: []string{"info", "dispatch", "inspect", "cancel"}, Models: []string{"chat"}, Harnesses: []string{"pair"}, AllowPrivate: true, MaxContextTokens: 32768}
	serverTrust := remote.TrustFile(filepath.Join(dir, "server.json"))
	caller := peer
	caller.ID = "browser-caller"
	if err = serverTrust.Replace(remote.Registry{Version: 1, Peers: []remote.Peer{caller}}, "absent"); err != nil {
		t.Fatal(err)
	}
	clientTrust := remote.TrustFile(filepath.Join(dir, "client.json"))
	registry := remote.Registry{Version: 1, Peers: []remote.Peer{peer}}
	if err = clientTrust.Replace(registry, "absent"); err != nil {
		t.Fatal(err)
	}
	journal, err := remote.OpenJournal(filepath.Join(dir, "journal"), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	b := &browserRemoteExecution{tasks: map[string]submissions.Status{}, identity: harness.Identity{Version: 1, Harness: "pi", HarnessVersion: "1", AdapterVersion: "1", Provider: "fixture", Model: "model", ModelRevision: "revision", ConfigSHA256: strings.Repeat("a", 64)}}
	server, err := remote.NewServer("node-a", serverTrust, journal, b)
	if err != nil {
		t.Fatal(err)
	}
	httpServer, err := server.HTTPServer(listener.Addr().String(), creds)
	if err != nil {
		t.Fatal(err)
	}
	originalHandler := httpServer.Handler
	var drop atomic.Bool
	drop.Store(true)
	httpServer.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/remote/tasks/browser-real-auto-01" && drop.CompareAndSwap(true, false) {
			captured := httptest.NewRecorder()
			originalHandler.ServeHTTP(captured, r)
			if captured.Code != 200 && captured.Code != 201 {
				t.Errorf("dispatch did not commit before lost response: %d", captured.Code)
			}
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("test transport cannot cut response")
				return
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		originalHandler.ServeHTTP(w, r)
	})
	go httpServer.ServeTLS(listener, "", "")
	defer httpServer.Close()
	routes, err := remote.OpenRouteStore(filepath.Join(dir, "routes"))
	if err != nil {
		t.Fatal(err)
	}
	h := mutationHandlerFixture(t, MutationServices{})
	queue, err := remote.OpenReviewQueue(filepath.Join(dir, "review-jobs"))
	if err != nil {
		t.Fatal(err)
	}
	evaluator := &browserContentEvaluator{t: t}
	policy := func(private bool) (remote.RemoteEvaluator, error) {
		if !private {
			t.Error("review privacy lost")
		}
		return remote.RemoteEvaluator{Evaluator: evaluator, Local: true, Timeout: time.Second}, nil
	}
	automatic := &RecordedRemoteAutomatic{Client: &remote.Client{Trust: clientTrust, Credentials: creds}, Store: routes, EvidenceRoot: filepath.Join(dir, "evidence"), ReviewQueue: queue, ReviewWait: time.Hour, ReviewPolicy: policy}
	h.remoteAutomatic = automatic
	cookie, csrf := authenticateBrowser(t, h)
	jobStatus := func(want int, status string) {
		t.Helper()
		r := browserRequest(http.MethodPost, "/app/api/v1/remote-review-job", `{"version":1,"request_id":"browser-real-auto-01"}`)
		r.AddCookie(cookie)
		r.Header.Set("X-Darwin-CSRF", csrf)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}
		if want == 200 {
			var body map[string]any
			if json.Unmarshal(w.Body.Bytes(), &body) != nil || body["status"] != status || len(body) != 4 {
				t.Fatal(w.Body.String())
			}
		}
		for _, secret := range []string{"private fixture task", "fixture answer", dir, "job_sha256"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("queue projection leaked data", w.Body.String())
			}
		}
	}
	jobStatus(503, "")
	payload := `{"version":1,"request_id":"browser-real-auto-01","prompt":"private fixture task","domain":"coding","profile":"default","difficulty":"hard","context_tokens":32768,"max_cost":0,"private":true,"capabilities":[]}`
	call := func(path, body string, want int) remoteTaskControlPage {
		t.Helper()
		r := authorizedMutationRequest(t, h, "/app/api/v1/"+path, body)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: %d want %d: %s", path, w.Code, want, w.Body.String())
		}
		var p remoteTaskControlPage
		if want == 200 {
			if e := json.Unmarshal(w.Body.Bytes(), &p); e != nil {
				t.Fatal(e)
			}
		}
		return p
	}
	call("remote-auto-dispatch", payload, 503)
	jobStatus(200, "pending")
	deadline, err := queue.Deadline("browser-real-auto-01")
	if err != nil || !deadline.After(time.Now()) {
		t.Fatal("lost dispatch response lost review intent", deadline, err)
	}
	page := call("remote-recorded-status", `{"version":1,"request_id":"browser-real-auto-01"}`, 200)
	choice, err := routes.AutomaticChoice("browser-real-auto-01")
	if err != nil || choice.Destination != "node-a" || choice.Identity != b.identity || choice.Explored || page.Instance != choice.Destination || b.creates.Load() != 1 || b.catalogues.Load() != 1 {
		t.Fatal("selection not bound", choice, page, err, b.creates.Load(), b.catalogues.Load())
	}
	b.mu.Lock()
	task := b.received
	b.mu.Unlock()
	if task.ExpectedHarnessIdentity == nil || *task.ExpectedHarnessIdentity != b.identity || task.HarnessDifficulty != "hard" || task.Prompt != "private fixture task" {
		t.Fatal(task)
	}
	b.unavailable.Store(true)
	call("remote-recorded-status", `{"version":1,"request_id":"browser-real-auto-01"}`, 200)
	call("remote-auto-dispatch", payload, 200)
	savedDeadline, err := queue.Deadline("browser-real-auto-01")
	if err != nil || !savedDeadline.Equal(deadline) {
		t.Fatal("retry extended review deadline", savedDeadline, err)
	}
	call("remote-auto-dispatch", strings.Replace(payload, "private fixture task", "changed intent", 1), 503)
	if b.creates.Load() != 1 || b.catalogues.Load() != 1 {
		t.Fatal("recovery discovered or duplicated")
	}
	verifyBrowserRemoteReview(t, h, b, payload)
	for range 2 {
		states, err := automatic.Client.ProcessReviewJobs(context.Background(), queue, routes, automatic.EvidenceRoot, policy)
		if err != nil || len(states) != 1 || states[0].Status != "completed" || !states[0].ReviewApplied {
			t.Fatal(states, err)
		}
	}
	jobStatus(200, "completed")
	if evaluator.calls.Load() != 1 || b.creates.Load() != 1 || b.catalogues.Load() != 1 {
		t.Fatal("background review replayed work", evaluator.calls.Load(), b.creates.Load(), b.catalogues.Load())
	}
	if _, err = clientTrust.Revoke("node-a", registry.Digest()); err != nil {
		t.Fatal(err)
	}
	jobStatus(503, "")
	call("remote-recorded-status", `{"version":1,"request_id":"browser-real-auto-01"}`, 503)
	call("remote-auto-dispatch", payload, 503)
	if b.creates.Load() != 1 || b.catalogues.Load() != 1 {
		t.Fatal("revocation bypass")
	}
}
