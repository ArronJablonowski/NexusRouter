package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/browserauth"
)

// Exercise real grant eviction with the embedded Active Jobs UI. All remote
// endpoints are local fixtures; no router or inference endpoint is contacted.
func TestChromeRemoteJobControlsAfterCSRFGrantEviction(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	auth, err := browserauth.New(browserauth.Options{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := auth.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err = auth.Approve(challenge.ID, challenge.DisplayCode); err != nil {
		t.Fatal(err)
	}
	session, err := auth.Consume(challenge.ID, challenge.Cookie)
	if err != nil {
		t.Fatal(err)
	}
	shell, err := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	var cancels, denied atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fixture/evict":
			for range browserauth.MaxCSRFGrantsPerSession {
				if _, err := auth.RotateCSRF(session.Token); err != nil {
					t.Error(err)
				}
			}
			return
		case "/app/api/v1/session/csrf":
			grant, err := auth.RotateCSRF(session.Token)
			if err != nil {
				t.Error(err)
				http.Error(w, "grant unavailable", 401)
				return
			}
			writeChromeJSON(w, map[string]any{"version": 1, "csrf_token": grant.CSRFToken})
			return
		case "/app/api/v1/remote-membership":
			writeChromeJSON(w, map[string]any{"version": 1, "enabled": true, "inspection_enabled": true, "registry": map[string]any{"peers": []any{map[string]any{"id": "node-a", "operations": []string{"inspect", "cancel"}}}}})
			return
		case "/app/api/v1/jobs":
			writeChromeJSON(w, map[string]any{"version": 1, "items": []any{}, "has_more": false, "next_cursor": ""})
			return
		case "/app/api/v1/remote-inspection", "/app/api/v1/remote-task-control":
			if !auth.AuthorizeMutation(session.Token, r.Header.Get("X-Darwin-CSRF")) {
				denied.Add(1)
				http.Error(w, "grant evicted", 403)
				return
			}
			if r.URL.Path == "/app/api/v1/remote-inspection" {
				writeChromeJSON(w, map[string]any{"version": 1, "observed_at": "2026-10-07T12:00:00Z", "tasks": map[string]any{"version": 1, "instance": "node-a", "after": "", "next": "request-existing-0001", "has_more": false, "tasks": []any{map[string]any{"request_id": "request-existing-0001", "state": "running", "description": "Synthetic remote job"}}}})
				return
			}
			var body map[string]any
			if err := decodeBoundedChromeJSON(w, r, 16384, &body); err != nil {
				t.Error(err)
				return
			}
			if body["action"] == "cancel" {
				cancels.Add(1)
			}
			writeChromeJSON(w, map[string]any{"version": 1, "instance": "node-a", "request_id": "request-existing-0001", "submission_id": "submission-a", "state": "running", "cancel_requested": cancels.Load() > 0, "task_ids": []any{}})
			return
		}
		shell.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	port, stop := startChromeForTest(t, ctx, chrome)
	defer stop()
	script := strings.Split(chromeRemoteDiscoveryCDP, "await cdp('Page.navigate'")[0] + `
 await cdp('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:false});
 await cdp('Page.navigate',{url:origin+'/app/active-jobs'});
 await eventually('document.querySelector("#remote-jobs-list button")','remote job not listed');
 await evaluate('document.querySelector("#remote-jobs-list button").click()');
 await eventually('document.querySelector(".job-detail-dialog details section output")?.textContent.includes("Last loaded")','initial status not loaded');
 await evaluate('window.controls=document.querySelector(".job-detail-dialog details section");window.loadStatus=controls.children[1];window.cancelJob=controls.children[2];window.output=controls.children[3];');
 await evaluate('fetch("/fixture/evict")');
 await evaluate('loadStatus.click()');
 await eventually('!cancelJob.disabled&&output.textContent.includes("Last loaded")','status failed after grant eviction');
 await evaluate('cancelJob.click()');
 await evaluate('fetch("/fixture/evict")');
 await evaluate('controls.children[5].children[1].click()');
 await eventually('output.textContent.includes("cancellation requested")','cancellation failed after grant eviction');
 if(!await evaluate('document.querySelector(".job-detail-dialog").open&&controls.isConnected'))throw Error('refresh discarded open controls');
 socket.close();`
	out, err := exec.CommandContext(ctx, node, "-e", script, strconv.Itoa(port), server.URL).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if cancels.Load() != 1 || denied.Load() != 0 {
		t.Fatalf("cancellations=%d denied requests=%d", cancels.Load(), denied.Load())
	}
}
