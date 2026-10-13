package comfyui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestQACancelRejectsMalformedAcknowledgement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("proxy temporarily unavailable")) }))
	defer server.Close()
	c, _ := New(server.URL)
	if e := c.DeletePending(context.Background(), "job-1"); e == nil {
		t.Fatal("malformed HTTP 200 reported as confirmed cancellation")
	}
}
func TestQAReceiptRejectsInvalidStateAndIdentity(t *testing.T) {
	base := Receipt{Version: 1, Endpoint: "http://127.0.0.1:8188", ClientID: "client-1", WorkflowSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: "accepted", PromptID: "job-1"}
	for _, kind := range []string{"state", "accepted-no-id", "traversal-id", "credential-origin", "unknown-field"} {
		t.Run(kind, func(t *testing.T) {
			r := base
			switch kind {
			case "state":
				r.State = "finished-by-magic"
			case "accepted-no-id":
				r.PromptID = ""
			case "traversal-id":
				r.PromptID = "../secret"
			case "credential-origin":
				r.Endpoint = "http://user:secret@host"
			}
			b, _ := json.Marshal(r)
			if kind == "unknown-field" {
				var m map[string]any
				json.Unmarshal(b, &m)
				m["extra"] = "unbound"
				b, _ = json.Marshal(m)
			}
			file := filepath.Join(t.TempDir(), "r.json")
			os.WriteFile(file, b, 0600)
			if _, e := LoadReceipt(file); e == nil {
				t.Fatal("invalid receipt accepted", kind)
			}
		})
	}
}
func TestQAServiceDoesNotReportUnstoredPreparedJob(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("request sent despite receipt failure") }))
	defer server.Close()
	root := t.TempDir()
	os.Chmod(root, 0700)
	svc, e := OpenService(server.URL, root)
	if e != nil {
		t.Fatal(e)
	}
	os.Remove(root)
	j, e := svc.Submit(context.Background(), "lost-root", workflow())
	if e == nil || j.State != "" {
		t.Fatal("unstored job reported as prepared", j, e)
	}
}

func TestQAServiceRestartRetainsAmbiguousDelivery(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.Write([]byte(`{`)) }))
	defer server.Close()
	root := t.TempDir()
	os.Chmod(root, 0700)
	first, _ := OpenService(server.URL, root)
	job, err := first.Submit(context.Background(), "uncertain-job", workflow())
	if !errors.Is(err, ErrUncertain) || job.State != "uncertain" {
		t.Fatal(job, err)
	}
	second, _ := OpenService(server.URL, root)
	job, err = second.Submit(context.Background(), "uncertain-job", workflow())
	if err != nil || !job.Replayed || job.State != "uncertain" || calls.Load() != 1 {
		t.Fatal("restart replayed ambiguous delivery", job, err, calls.Load())
	}
	if err = second.CancelPending(context.Background(), "uncertain-job"); err == nil || calls.Load() != 1 {
		t.Fatal("unbound cancellation dispatched")
	}
}

func TestQAReceiptOversizedAndNonPrivateFailClosed(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "r")
	os.WriteFile(file, make([]byte, MaxJSON+1), 0600)
	if _, err := LoadReceipt(file); err == nil {
		t.Fatal("oversized receipt accepted")
	}
	os.Chmod(file, 0644)
	if _, err := LoadReceipt(file); err == nil {
		t.Fatal("public receipt accepted")
	}
	link := filepath.Join(root, "link")
	os.Symlink(file, link)
	if _, err := LoadReceipt(link); err == nil {
		t.Fatal("symlink receipt accepted")
	}
}

func FuzzParseWorkflowQA(f *testing.F) {
	f.Add([]byte(`{"1":{"class_type":"EmptyImage","inputs":{"width":32}}}`))
	f.Add([]byte(`{"nodes":[],"links":[]}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		w, err := ParseWorkflow(b)
		if err != nil {
			return
		}
		encoded, err := json.Marshal(w)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = ParseWorkflow(encoded); err != nil {
			t.Fatal("accepted graph fails canonical round trip", err)
		}
	})
}

func TestQACancelAcceptsNativeEmptyAcknowledgement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer server.Close()
	c, _ := New(server.URL)
	if err := c.DeletePending(context.Background(), "owned-job"); err != nil {
		t.Fatal(err)
	}
}
