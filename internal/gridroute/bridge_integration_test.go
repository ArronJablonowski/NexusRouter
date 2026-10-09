package gridroute

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/submissions"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
)

// All traffic is isolated mTLS fixture traffic; no model or real router is used.
func TestBridgeUsesCallerEvidenceAndPinnedAdmission(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "spark"}, DNSNames: []string{"spark"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
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
	store, err := remote.OpenRouteStore(filepath.Join(dir, "routes"))
	if err != nil {
		t.Fatal(err)
	}
	identity := harness.Identity{Version: 1, Harness: "pi", HarnessVersion: "1", AdapterVersion: "1", Provider: "ollama", Model: "coder", ModelRevision: "weights-1", ConfigSHA256: strings.Repeat("c", 64)}
	zero := 0.0
	info := remote.Info{Version: 1, Instance: "spark", Hostname: "spark-host", ConversationVersion: 1, Available: true, Models: []remote.Model{{ID: "coder", Provider: "ollama", Model: "coder", Local: true, ContextTokens: 32768, EstimatedCost: &zero, Capabilities: []string{"chat", "code"}}}, Harnesses: []remote.Harness{{ID: "pi", ModelID: "coder", Kind: "pi", ModelRevision: "weights-1"}}, Routing: &contract.RoutingInspection{Rankings: []contract.SpecialistRankingInspection{{Key: "coding", Domain: "code", Profile: "default", Models: []contract.SpecialistRankInspection{{ModelID: "coder", Domain: "code", Profile: "default", Score: 1, Confidence: 1, Samples: 999}}}}}}
	var requests, dispatches atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || r.Header.Get("X-Nexus-Instance") != "spark" {
			http.Error(w, "denied", 403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Nexus-Instance", "spark")
		now := time.Now().UTC()
		tokens, _ := strconv.Atoi(r.Header.Get("X-Nexus-Context"))
		q := remote.HarnessIdentityRequest{ModelID: "coder", HarnessID: "pi", ContextTokens: tokens}
		switch r.URL.Path {
		case "/v1/remote/info", "/v1/remote/catalogue":
			json.NewEncoder(w).Encode(info)
		case "/v1/remote/harness-readiness":
			json.NewEncoder(w).Encode(remote.HarnessReadiness{Version: 1, Instance: "spark", Request: q, CheckedAt: now, Readiness: harness.Readiness{Identity: identity, ExecutableMatched: true, CredentialState: "not_required", ModelState: "present", Compatible: true, Local: true, Capabilities: []string{"chat", "code"}, ContextTokens: 32768}})
		case "/v1/remote/harness-capacity":
			json.NewEncoder(w).Encode(remote.HarnessCapacity{Version: 1, Instance: "spark", Request: q, CheckedAt: now, Identity: identity, Need: resources.Need{RAM: 100}, Capacity: resources.CapacityResult{Version: 1, Action: resources.CapacityAdmit, Reason: resources.CapacityAvailable, SnapshotTime: now, ObservedAt: now, Headroom: resources.CapacityHeadroom{RAMBytes: 1000}, MaxAdditional: 1}})
		case "/v1/remote/tasks/federated-parent-fixture":
			dispatches.Add(1)
			if _, e := store.Lookup("federated-parent-fixture"); e != nil {
				t.Error("network preceded durable binding", e)
			}
			var task remote.Task
			if json.NewDecoder(r.Body).Decode(&task) != nil || task.Validate() != nil || len(task.Messages) != 3 || task.ExpectedHarnessIdentity == nil || *task.ExpectedHarnessIdentity != identity {
				t.Error("conversation or identity lost")
			}
			json.NewEncoder(w).Encode(submissions.Status{Version: 1, ID: "submission-fixture", State: "succeeded", Result: &submissions.Result{TaskID: "remote-task", Text: "fixture answer", FinishReason: "stop"}})
		default:
			http.Error(w, "unexpected fixture path", 404)
		}
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}
	server.StartTLS()
	defer server.Close()
	trust := remote.TrustFile(filepath.Join(dir, "peers.json"))
	registry := remote.Registry{Version: 1, Peers: []remote.Peer{{ID: "spark", Endpoint: server.URL, ServerName: "spark", Pins: []string{remote.Fingerprint(cert)}, Operations: []string{"info", "dispatch", "inspect", "cancel"}, Models: []string{"coder"}, Harnesses: []string{"pi"}, AllowPrivate: true, MaxContextTokens: 32768}}}
	if err = trust.Replace(registry, "absent"); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "evidence")
	if err = remote.PrepareOutcomeEvidence(root); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(struct{ Destination, Caller string }{"spark", remote.Fingerprint(cert)})
	scope := sha256.Sum256(encoded)
	scopeDir := filepath.Join(root, fmt.Sprintf("%x", scope))
	if err = os.Mkdir(scopeDir, 0700); err != nil {
		t.Fatal(err)
	}
	ledger, err := harness.OpenEvidenceStore(filepath.Join(scopeDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	execution := harness.Execution{Version: 1, ID: "old-output", Actual: identity, Task: harness.TaskClass{Domain: "code", Profile: "default", Difficulty: "unknown"}, Status: "completed", OutputSHA256: strings.Repeat("b", 64), CompletedAt: now.Add(-time.Minute)}
	if err = ledger.AppendExecution(context.Background(), execution, now); err != nil {
		t.Fatal(err)
	}
	digest, _ := execution.Digest()
	if err = ledger.AppendReview(context.Background(), harness.Review{Version: 1, ID: "confirmed-failure", ExecutionDigest: digest, Verdict: "failed", Method: "deterministic", MethodVersion: "fixture-v1", Reviewer: "trusted-host", Confidence: 1, Quality: 1, CreatedAt: now}, now); err != nil {
		t.Fatal(err)
	}
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}
	client := &remote.Client{Trust: trust, Credentials: remote.Credentials{CertificateFile: certPath, KeyFile: keyPath, CAFile: certPath}}
	bridge := &Bridge{Client: client, Store: store, EvidenceRoot: root}
	request := routing.Request{Mode: "local_only", Domain: "code", Profile: "default", ContextTokens: 8192}
	candidates, err := bridge.Candidates(context.Background(), request)
	if err != nil || len(candidates) != 1 || candidates[0].Hostname != "spark-host" || len(candidates[0].Observations.Fitness) != 1 || candidates[0].Observations.Fitness[0].Quality != 0 {
		t.Fatal("host score replaced caller-owned accuracy", candidates, err)
	}
	adapter, err := bridge.Open(context.Background(), candidates[0], "parent-fixture", request)
	if err != nil {
		t.Fatal(err)
	}
	chunks := 0
	if err = adapter.Stream(context.Background(), providers.Request{Model: "coder", Messages: []providers.Message{{Role: "user", Content: "prior"}, {Role: "assistant", Content: "prior answer"}, {Role: "user", Content: "current"}}}, func(providers.Chunk) error { chunks++; return nil }); err != nil || chunks != 2 || dispatches.Load() != 1 {
		t.Fatal(err, chunks, dispatches.Load())
	}
	before := requests.Load()
	if _, err = trust.Revoke("spark", registry.Digest()); err != nil {
		t.Fatal(err)
	}
	candidates, err = bridge.Candidates(context.Background(), request)
	if err != nil || len(candidates) != 0 || requests.Load() != before {
		t.Fatal("cached revoked peer probed", candidates, err, requests.Load(), before)
	}
}
