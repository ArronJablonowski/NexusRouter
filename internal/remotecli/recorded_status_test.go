package remotecli

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

type noPromptReader struct{}

func (noPromptReader) Read([]byte) (int, error) { panic("recorded-status must not read a prompt") }
func TestRecordedStatusCLIUsesSavedDestinationWithoutInput(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"node-a"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
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
	var calls atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/v1/remote/tasks/recorded-cli-0001" || r.Header.Get("X-Nexus-Instance") != "node-a" || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			http.Error(w, "denied", 403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Nexus-Instance", "node-a")
		json.NewEncoder(w).Encode(submissions.Status{Version: 1, ID: "submission-a", State: "running"})
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}
	server.StartTLS()
	defer server.Close()
	dir := t.TempDir()
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	private, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private}), 0600); err != nil {
		t.Fatal(err)
	}
	trust := remote.TrustFile(filepath.Join(dir, "peers.json"))
	registry := remote.Registry{Version: 1, Peers: []remote.Peer{{ID: "node-a", Endpoint: server.URL, ServerName: "node-a", Pins: []string{remote.Fingerprint(cert)}, Operations: []string{"inspect"}, MaxContextTokens: 8192}}}
	if err = trust.Replace(registry, "absent"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "routes")
	routes, err := remote.OpenRouteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = routes.Bind(remote.RouteBinding{Version: 1, RequestID: "recorded-cli-0001", Destination: "node-a", CallerFingerprint: remote.Fingerprint(cert), TaskSHA256: strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	args := []string{"recorded-status", "--trust", string(trust), "--cert", certPath, "--key", keyPath, "--ca", certPath, "--routes", path, "--request", "recorded-cli-0001"}
	var out bytes.Buffer
	if err = Run(context.Background(), args, noPromptReader{}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var result remote.RecordedRequestStatus
	if err = json.Unmarshal(out.Bytes(), &result); err != nil || result.Destination != "node-a" || result.Status.ID != "submission-a" || calls.Load() != 1 {
		t.Fatal(result, err, calls.Load())
	}
	if err = Run(context.Background(), append(args, "--instance", "node-c"), noPromptReader{}, io.Discard, io.Discard); err == nil || calls.Load() != 1 {
		t.Fatal("destination override contacted peer", err)
	}
	if _, err = trust.Revoke("node-a", registry.Digest()); err != nil {
		t.Fatal(err)
	}
	if err = Run(context.Background(), args, noPromptReader{}, io.Discard, io.Discard); err == nil || calls.Load() != 1 {
		t.Fatal("revoked peer contacted", err)
	}
}
