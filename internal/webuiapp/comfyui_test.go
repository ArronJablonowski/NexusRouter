package webuiapp

import (
	"bytes"
	"context"
	"github.com/ArronJablonowski/NexusRouter/connectors/comfyui"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestComfyBrowserAuthorityAndOwnedReplay(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"prompt_id":"job-1","number":0}`))
	}))
	defer s.Close()
	root := t.TempDir()
	os.Chmod(root, 0700)
	svc, e := comfyui.OpenService(s.URL, root)
	if e != nil {
		t.Fatal(e)
	}
	h := handlerFixture(t)
	h.comfy = svc
	cookie, csrf := authenticateBrowser(t, h)
	body := `{"key":"web-job","workflow":{"1":{"class_type":"EmptyImage","inputs":{}}}}`
	for _, mode := range []string{"anonymous", "csrf", "cross-origin", "good", "replay"} {
		r := browserRequest("POST", "/app/api/v1/comfy/jobs", body)
		if mode != "anonymous" {
			r.AddCookie(cookie)
		}
		r.Header.Set("X-Darwin-CSRF", csrf)
		if mode == "csrf" {
			r.Header.Del("X-Darwin-CSRF")
		}
		if mode == "cross-origin" {
			r.Header.Set("Origin", "http://other.test")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if mode == "good" || mode == "replay" {
			if w.Code != 200 {
				t.Fatal(mode, w.Code, w.Body.String())
			}
		} else if w.Code == 200 {
			t.Fatal("unauthorized dispatch", mode)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate or unauthorized remote calls", calls.Load())
	}
	r := browserRequest(http.MethodGet, "/app/api/v1/comfy", "")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("owned jobs unreadable")
	}
	r = browserRequest(http.MethodGet, "/app/api/v1/comfy/jobs/unknown", "")
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatal("unowned job accepted")
	}
}

// Explicit live opt-in: all jobs use a private test receipt directory and an
// isolated output prefix. The endpoint must be supplied by the operator.
func TestComfyBrowserSparkLive(t *testing.T) {
	endpoint := os.Getenv("NEXUS_COMFY_LIVE_ENDPOINT")
	if endpoint == "" {
		t.Skip("explicit ComfyUI live endpoint required")
	}
	root := t.TempDir()
	os.Chmod(root, 0700)
	svc, e := comfyui.OpenService(endpoint, root)
	if e != nil {
		t.Fatal(e)
	}
	h := handlerFixture(t)
	h.comfy = svc
	cookie, csrf := authenticateBrowser(t, h)
	body := `{"key":"live-web","workflow":{"1":{"class_type":"EmptyImage","inputs":{"width":48,"height":24,"batch_size":1,"color":16711935}},"2":{"class_type":"SaveImage","inputs":{"images":["1",0],"filename_prefix":"NexusRouterTests/chat-web-live"}}}}`
	r := browserRequest("POST", "/app/api/v1/comfy/jobs", body)
	r.AddCookie(cookie)
	r.Header.Set("X-Darwin-CSRF", csrf)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		job, e := svc.Status(ctx, "live-web")
		if e != nil {
			t.Fatal(e)
		}
		if job.State == "success" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	r = browserRequest("GET", "/app/api/v1/comfy/jobs/live-web/image?node=2&image=0", "")
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	im, e := png.Decode(bytes.NewReader(w.Body.Bytes()))
	if e != nil || im.Bounds().Dx() != 48 || im.Bounds().Dy() != 24 {
		t.Fatal("invalid image", e)
	}
	r = browserRequest("POST", "/app/api/v1/comfy/jobs", body)
	r.AddCookie(cookie)
	r.Header.Set("X-Darwin-CSRF", csrf)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatal("live replay protection", w.Code, w.Body.String())
	}
}

func TestComfyBrowserSparkRejectedAndOwnedCancelLive(t *testing.T) {
	endpoint := os.Getenv("NEXUS_COMFY_LIVE_ENDPOINT")
	if endpoint == "" {
		t.Skip("explicit live endpoint required")
	}
	root := t.TempDir()
	os.Chmod(root, 0700)
	svc, err := comfyui.OpenService(endpoint, root)
	if err != nil {
		t.Fatal(err)
	}
	job, err := svc.Submit(context.Background(), "qa-invalid", comfyui.Workflow{"1": {ClassType: "NexusRouterNonexistentQANode", Inputs: map[string]any{}}})
	if err == nil || job.State != "rejected" {
		t.Fatal("validation rejection not retained", job, err)
	}
	replay, err := svc.Submit(context.Background(), "qa-invalid", comfyui.Workflow{"1": {ClassType: "NexusRouterNonexistentQANode", Inputs: map[string]any{}}})
	if err != nil || !replay.Replayed || replay.State != "rejected" {
		t.Fatal("rejection replay", replay, err)
	}
	w := comfyui.Workflow{"1": {ClassType: "EmptyImage", Inputs: map[string]any{"width": 8, "height": 8, "batch_size": 1, "color": 0}}, "2": {ClassType: "SaveImage", Inputs: map[string]any{"images": []any{"1", 0}, "filename_prefix": "NexusRouterTests/qa-cancel"}}}
	job, err = svc.Submit(context.Background(), "qa-own", w)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err = svc.Client.Wait(ctx, job.PromptID, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	// Exact completed test ID exercises the real queue acknowledgement without
	// interrupting any running job or clearing shared queue entries.
	if err = svc.CancelPending(ctx, "qa-own"); err != nil {
		t.Fatal("real cancellation acknowledgement", err)
	}
}
