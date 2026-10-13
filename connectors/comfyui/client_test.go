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
	"time"
)

func workflow() Workflow {
	return Workflow{"1": {ClassType: "EmptyImage", Inputs: map[string]any{"width": 32, "height": 32, "batch_size": 1, "color": 0}}, "2": {ClassType: "SaveImage", Inputs: map[string]any{"images": []any{"1", 0}, "filename_prefix": "nexus-test"}}}
}
func TestSubmitOnceRetainsReceiptAndBlocksReplay(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/prompt" {
			t.Error("wrong route")
		}
		var b map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&b) != nil || len(b["prompt"]) == 0 || len(b["client_id"]) == 0 {
			t.Error("bad submission")
		}
		w.Write([]byte(`{"prompt_id":"job-1","number":0}`))
	}))
	defer s.Close()
	c, _ := New(s.URL)
	file := filepath.Join(t.TempDir(), "receipt.json")
	r, e := c.SubmitOnce(context.Background(), workflow(), file)
	if e != nil || r.State != "accepted" || r.PromptID != "job-1" {
		t.Fatalf("%+v %v", r, e)
	}
	persisted, e := LoadReceipt(file)
	if e != nil || persisted != r {
		t.Fatal("receipt not persisted")
	}
	info, _ := os.Stat(file)
	if info.Mode().Perm() != 0600 {
		t.Fatal("receipt not private")
	}
	_, e = c.SubmitOnce(context.Background(), workflow(), file)
	if e == nil || calls.Load() != 1 {
		t.Fatal("duplicate dispatched")
	}
}
func TestUncertainDeliveryNeverRetries(t *testing.T) {
	for _, mode := range []string{"disconnect", "bad-json", "redirect", "server-error"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch mode {
				case "disconnect":
					conn, _, _ := w.(http.Hijacker).Hijack()
					conn.Close()
				case "bad-json":
					w.Write([]byte(`{`))
				case "redirect":
					http.Redirect(w, r, "/other", 307)
				case "server-error":
					w.WriteHeader(500)
				}
			}))
			defer s.Close()
			c, _ := New(s.URL)
			file := filepath.Join(t.TempDir(), "receipt.json")
			_, e := c.SubmitOnce(context.Background(), workflow(), file)
			if !errors.Is(e, ErrUncertain) {
				t.Fatalf("%v", e)
			}
			r, e := LoadReceipt(file)
			if e != nil || r.State != "uncertain" {
				t.Fatal("uncertainty not retained")
			}
			c.SubmitOnce(context.Background(), workflow(), file)
			if calls.Load() != 1 {
				t.Fatal("replayed uncertain job")
			}
		})
	}
}
func TestPromptValidationRejected(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":"private prompt text"}`))
	}))
	defer s.Close()
	c, _ := New(s.URL)
	r, e := c.SubmitOnce(context.Background(), workflow(), filepath.Join(t.TempDir(), "r"))
	if e == nil || errors.Is(e, ErrUncertain) || r.State != "rejected" {
		t.Fatalf("%+v %v", r, e)
	}
}
func TestHistoryWaitAndFailure(t *testing.T) {
	for _, status := range []string{"success", "error"} {
		t.Run(status, func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/history/job-1" {
					t.Error("bad history route")
				}
				if calls.Add(1) == 1 {
					w.Write([]byte(`{}`))
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"job-1": map[string]any{"status": map[string]any{"status_str": status, "completed": true}, "outputs": map[string]any{}}})
			}))
			defer s.Close()
			c, _ := New(s.URL)
			h, e := c.Wait(context.Background(), "job-1", 100*time.Millisecond)
			if h == nil || (status == "success" && e != nil) || (status == "error" && e == nil) {
				t.Fatalf("%+v %v", h, e)
			}
		})
	}
}
func TestWaitDeadlineDoesNotCancel(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("wait mutated queue")
		}
		calls.Add(1)
		w.Write([]byte(`{}`))
	}))
	defer s.Close()
	c, _ := New(s.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, e := c.Wait(ctx, "job-1", time.Second)
	if !errors.Is(e, context.DeadlineExceeded) || calls.Load() != 1 {
		t.Fatal(e)
	}
}
func TestDeleteOnlySpecificPendingID(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/queue" || r.Method != "POST" {
			t.Error("wrong route")
		}
		b := map[string]any{}
		json.NewDecoder(r.Body).Decode(&b)
		if len(b) != 1 || b["delete"].([]any)[0] != "job-1" {
			t.Error("global queue mutation")
		}
		w.Write([]byte(`{}`))
	}))
	defer s.Close()
	c, _ := New(s.URL)
	if e := c.DeletePending(context.Background(), "job-1"); e != nil {
		t.Fatal(e)
	}
}
func TestRejectUnsafeReferencesAndOrigins(t *testing.T) {
	for _, u := range []string{"http://user:pass@localhost:8188", "file:///tmp/comfy", "http://localhost:8188/path", "http://localhost:8188?token=secret"} {
		if _, e := New(u); e == nil {
			t.Error(u)
		}
	}
	c, _ := New("http://127.0.0.1:1")
	for _, i := range []Image{{Filename: "../secret", Type: "output"}, {Filename: "a.png", Subfolder: "../secret", Type: "output"}, {Filename: "a.png", Type: "input"}} {
		if _, e := c.Image(context.Background(), i); e == nil {
			t.Error(i)
		}
	}
	if _, e := c.History(context.Background(), "../queue"); e == nil {
		t.Fatal("unsafe ID")
	}
}
func TestRejectUIWorkflowAndOversizedResponse(t *testing.T) {
	if _, e := ParseWorkflow([]byte(`{"nodes":[],"links":[]}`)); e == nil {
		t.Fatal("UI workflow accepted")
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(make([]byte, MaxJSON+1)) }))
	defer s.Close()
	c, _ := New(s.URL)
	if _, e := c.Read(context.Background(), "info"); e == nil {
		t.Fatal("oversized response")
	}
}

func TestConcurrentSubmitReceiptHasOneOwner(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"prompt_id":"job-1","number":0}`))
	}))
	defer s.Close()
	c, _ := New(s.URL)
	file := filepath.Join(t.TempDir(), "r")
	done := make(chan error, 8)
	for range 8 {
		go func() { _, e := c.SubmitOnce(context.Background(), workflow(), file); done <- e }()
	}
	successes := 0
	for range 8 {
		if <-done == nil {
			successes++
		}
	}
	if successes != 1 || calls.Load() != 1 {
		t.Fatalf("successes=%d calls=%d", successes, calls.Load())
	}
}
