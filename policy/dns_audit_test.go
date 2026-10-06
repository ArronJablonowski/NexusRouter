package policy

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestDNSAuditLogsResolutionWithoutRequestSecrets(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{true: "failure", false: "success"}[failed], func(t *testing.T) {
			dir := t.TempDir()
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
			defer server.Close()
			tr, err := NewTransport(false, []string{"https://example.com"}, DNSAudit{Path: dir, Source: "test"})
			if err != nil {
				t.Fatal(err)
			}
			defer tr.CloseIdleConnections()
			tr.inner.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig
			tr.inner.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				return tr.audit.dial(ctx, network, address, func(ctx context.Context, network, address string) (net.Conn, error) {
					trace := httptrace.ContextClientTrace(ctx)
					trace.DNSStart(httptrace.DNSStartInfo{Host: "example.com"})
					done := httptrace.DNSDoneInfo{Addrs: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}}
					if failed {
						done.Err = errors.New("private diagnostic")
						done.Addrs = nil
					}
					trace.DNSDone(done)
					if failed {
						return nil, done.Err
					}
					return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
				})
			}
			req, _ := http.NewRequest("GET", "https://example.com/private-path", nil)
			req.Header.Set("Authorization", "private-token")
			response, err := tr.RoundTrip(req)
			if failed != (err != nil) {
				t.Fatal(err)
			}
			if response != nil {
				response.Body.Close()
			}
			paths, _ := filepath.Glob(filepath.Join(dir, "dns-*.jsonl"))
			if len(paths) != 1 {
				t.Fatal(paths)
			}
			raw, _ := os.ReadFile(paths[0])
			var record dnsRecord
			if json.Unmarshal(raw, &record) != nil {
				t.Fatal("invalid audit")
			}
			if record.Query != "example.com" || record.PID != os.Getpid() || record.Source != "test" || record.Time.IsZero() {
				t.Fatal(record)
			}
			want := "resolved"
			if failed {
				want = "failed"
			}
			if record.Outcome != want {
				t.Fatal(record)
			}
			if stringContainsAny(string(raw), "private-path", "private-token", "private diagnostic") {
				t.Fatal("request secret leaked")
			}
		})
	}
}
func stringContainsAny(s string, values ...string) bool {
	for _, v := range values {
		for i := 0; i+len(v) <= len(s); i++ {
			if s[i:i+len(v)] == v {
				return true
			}
		}
	}
	return false
}
func TestDNSAuditConcurrentRecordsAndUnsafeFile(t *testing.T) {
	dir := t.TempDir()
	a := DNSAudit{Path: dir}
	rec := dnsRecord{Version: 1, Time: time.Now().UTC(), Query: "example.com"}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := a.append(rec); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	f, err := os.Open(filepath.Join(dir, "dns-"+rec.Time.Format("2006-01-02")+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	for i := 0; i < 20; i++ {
		var v dnsRecord
		if decoder.Decode(&v) != nil || v.Query != rec.Query {
			t.Fatal("lost or interleaved record")
		}
	}
	bad := t.TempDir()
	target := filepath.Join(bad, "secret")
	os.WriteFile(target, []byte("unchanged"), 0600)
	os.Symlink(target, filepath.Join(bad, "dns-"+rec.Time.Format("2006-01-02")+".jsonl"))
	if (DNSAudit{Path: bad}).append(rec) == nil {
		t.Fatal("accepted symlink")
	}
	b, _ := os.ReadFile(target)
	if string(b) != "unchanged" {
		t.Fatal("modified target")
	}
}
func TestDNSAuditLiteralIPCreatesNoLookup(t *testing.T) {
	dir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	tr, _ := NewTransport(true, []string{server.URL}, DNSAudit{Path: dir})
	defer tr.CloseIdleConnections()
	req, _ := http.NewRequest("GET", server.URL, nil)
	res, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	files, _ := os.ReadDir(dir)
	if len(files) != 0 {
		t.Fatal("fabricated DNS")
	}
}

func TestDNSAuditFailureClosesConnectionBeforeHTTP(t *testing.T) {
	a := DNSAudit{Path: filepath.Join(t.TempDir(), "missing")}
	local, peer := net.Pipe()
	defer peer.Close()
	conn, err := a.dial(context.Background(), "tcp", "example.com:443", func(ctx context.Context, _, _ string) (net.Conn, error) {
		trace := httptrace.ContextClientTrace(ctx)
		trace.DNSStart(httptrace.DNSStartInfo{Host: "example.com"})
		trace.DNSDone(httptrace.DNSDoneInfo{})
		return local, nil
	})
	if conn != nil || !errors.Is(err, ErrDNSAudit) {
		t.Fatal(conn, err)
	}
	peer.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	n, e := peer.Read(b[:])
	if n != 0 || e == nil {
		t.Fatal("unlogged connection left usable")
	}
}
