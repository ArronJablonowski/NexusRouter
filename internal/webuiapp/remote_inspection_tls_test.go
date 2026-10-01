package webuiapp

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

func TestBrowserRemoteInspectionUsesPinnedMTLSAndFreshRevocation(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "node-a"}, DNSNames: []string{"node-a"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	var calls, cancellations atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if (r.Method != "GET" && !(r.Method == "POST" && r.URL.Path == "/v1/remote/tasks/request-existing-0001/cancel")) || r.Header.Get("X-Nexus-Instance") != "node-a" || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			http.Error(w, "denied", 403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Nexus-Instance", "node-a")
		switch r.URL.Path {
		case "/v1/remote/info":
			json.NewEncoder(w).Encode(remote.Info{Version: 1, Instance: "node-a", Available: true})
		case "/v1/remote/tasks":
			json.NewEncoder(w).Encode(remote.TaskPage{Version: 1, Instance: "node-a", After: r.Header.Get("X-Nexus-After-Request"), Next: "request-existing-0001", Tasks: []remote.TaskSummary{{RequestID: "request-existing-0001", State: "running", TaskIDs: []string{"task-1"}}}})
		case "/v1/remote/tasks/request-existing-0001":
			json.NewEncoder(w).Encode(submissions.Status{Version: 1, ID: "submission-a", State: "running", TaskIDs: []string{"task-1"}})
		case "/v1/remote/tasks/request-existing-0001/events":
			if r.Header.Get("X-Nexus-Task") != "task-1" || r.Header.Get("X-Nexus-After") != "0" {
				http.Error(w, "invalid", 400)
				return
			}
			json.NewEncoder(w).Encode(browserEventPage())
		case "/v1/remote/tasks/request-existing-0001/cancel":
			cancellations.Add(1)
			json.NewEncoder(w).Encode(submissions.Status{Version: 1, ID: "submission-a", State: "running", CancelRequested: true})
		default:
			http.Error(w, "not found", 404)
		}
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}
	server.StartTLS()
	defer server.Close()
	dir := t.TempDir()
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	trust := remote.TrustFile(filepath.Join(dir, "peers.json"))
	registry := remote.Registry{Version: 1, Peers: []remote.Peer{{ID: "node-a", Endpoint: server.URL, ServerName: "node-a", Pins: []string{remote.Fingerprint(cert)}, Operations: []string{"info", "inspect"}, MaxContextTokens: 32768}}}
	if err = trust.Replace(registry, "absent"); err != nil {
		t.Fatal(err)
	}
	h := mutationHandlerFixture(t, MutationServices{})
	h.remoteTrustFile = string(trust)
	h.remoteInspector = &remote.Client{Trust: trust, Credentials: remote.Credentials{CertificateFile: certPath, KeyFile: keyPath, CAFile: certPath}}
	inspect := func(view string, want int) {
		t.Helper()
		r := authorizedMutationRequest(t, h, "/app/api/v1/remote-inspection", `{"version":1,"instance":"node-a","view":"`+view+`"}`)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%d want %d: %s", w.Code, want, w.Body.String())
		}
	}
	inspect("info", 200)
	inspect("tasks", 200)
	if calls.Load() != 2 {
		t.Fatal("missing authenticated calls")
	}
	h.remoteTaskController = h.remoteInspector.(*remote.Client)
	control := func(action string, want int) {
		t.Helper()
		body := `{"version":1,"instance":"node-a","request_id":"request-existing-0001","action":"` + action + `"`
		if action == "cancel" {
			body += `,"expected_submission_id":"submission-a"`
		}
		body += `}`
		r := authorizedMutationRequest(t, h, "/app/api/v1/remote-task-control", body)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("control: %d want %d: %s", w.Code, want, w.Body.String())
		}
	}
	control("status", 200)
	control("cancel", 503)
	if calls.Load() != 4 || cancellations.Load() != 0 {
		t.Fatal("inspection scope permitted cancellation")
	}
	previous := registry.Digest()
	registry.Peers[0].Operations = append(registry.Peers[0].Operations, "cancel")
	if err = trust.Replace(registry, previous); err != nil {
		t.Fatal(err)
	}
	control("cancel", 200)
	if calls.Load() != 6 || cancellations.Load() != 1 {
		t.Fatal("wrong control calls", calls.Load(), cancellations.Load())
	}
	events := func(want int) {
		t.Helper()
		r := authorizedMutationRequest(t, h, "/app/api/v1/remote-task-events", `{"version":1,"instance":"node-a","request_id":"request-existing-0001","task_id":"task-1","after":0}`)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("events %d want %d: %s", w.Code, want, w.Body.String())
		}
	}
	events(200)
	if calls.Load() != 8 {
		t.Fatal("events did not use authenticated status and events calls", calls.Load())
	}
	if _, err = trust.Revoke("node-a", registry.Digest()); err != nil {
		t.Fatal(err)
	}
	events(503)
	inspect("info", 503)
	inspect("tasks", 503)
	control("cancel", 503)
	if calls.Load() != 8 || cancellations.Load() != 1 {
		t.Fatal("revoked peer contacted")
	}
}
