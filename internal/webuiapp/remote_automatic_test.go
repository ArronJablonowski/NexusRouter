package webuiapp

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/submissions"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type automaticBrowserFake struct {
	dispatches, reads int
	failed            bool
	request           remote.AutomaticRequest
}

func (f *automaticBrowserFake) result(key string) (remote.RecordedRequestStatus, error) {
	if f.failed {
		return remote.RecordedRequestStatus{}, errors.New("private diagnostic")
	}
	return remote.RecordedRequestStatus{Version: 1, RequestID: key, Destination: "node-a", Status: submissions.Status{Version: 1, ID: "submission-a", State: "queued", ConfigDigest: "secret-config"}}, nil
}
func (f *automaticBrowserFake) DispatchAutomatic(_ context.Context, key string, r remote.AutomaticRequest) (remote.RecordedRequestStatus, error) {
	f.dispatches++
	f.request = r
	return f.result(key)
}
func (f *automaticBrowserFake) InspectRecorded(_ context.Context, key string) (remote.RecordedRequestStatus, error) {
	f.reads++
	return f.result(key)
}
func TestAutomaticBrowserAuthoritySelectionInputAndRecovery(t *testing.T) {
	h := mutationHandlerFixture(t, MutationServices{})
	f := &automaticBrowserFake{}
	h.remoteAutomatic = f
	body := `{"version":1,"request_id":"automatic-browser-01","prompt":"solve task","domain":"coding","profile":"default","difficulty":"hard","context_tokens":32768,"max_cost":0,"private":true,"capabilities":["tools"]}`
	call := func(path, body string, want int, authority bool) string {
		t.Helper()
		r := browserRequest(http.MethodPost, "/app/api/v1/"+path, body)
		if authority {
			r = authorizedMutationRequest(t, h, "/app/api/v1/"+path, body)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%d want %d: %s", w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	call("remote-auto-dispatch", body, 401, false)
	for _, bad := range []string{strings.Replace(body, `"hard"`, `"invented"`, 1), strings.Replace(body, `32768`, `4096`, 1), strings.Replace(body, `"max_cost":0`, `"max_cost":-1`, 1), strings.Replace(body, `"prompt":"solve task"`, `"prompt":""`, 1), strings.Replace(body, `"private":true`, `"allow_exploration":true`, 1), strings.Replace(body, `"private":true`, `"destination":"node-attacker"`, 1)} {
		call("remote-auto-dispatch", bad, 400, true)
	}
	if f.dispatches != 0 {
		t.Fatal("invalid input dispatched")
	}
	r := authorizedMutationRequest(t, h, "/app/api/v1/remote-auto-dispatch", body)
	r.Header.Del("X-Darwin-CSRF")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 || f.dispatches != 0 {
		t.Fatal("CSRF bypass")
	}
	out := call("remote-auto-dispatch", body, 200, true)
	var page remoteTaskControlPage
	if err := json.Unmarshal([]byte(out), &page); err != nil || page.Instance != "node-a" || strings.Contains(out, "secret-config") {
		t.Fatal(out, err)
	}
	if f.request.Routing.AllowExploration || !f.request.Routing.LocalRequired || f.request.Routing.Task.Difficulty != "hard" || f.request.Routing.ContextTokens != 32768 || f.dispatches != 1 {
		t.Fatal(f.request)
	}
	status := `{"version":1,"request_id":"automatic-browser-01"}`
	call("remote-recorded-status", status, 200, true)
	if f.dispatches != 1 || f.reads != 1 {
		t.Fatal("status dispatched")
	}
	call("remote-recorded-status", strings.Replace(status, `"version":1`, `"version":1,"prompt":"forbidden"`, 1), 400, true)
	f.failed = true
	out = call("remote-auto-dispatch", body, 503, true)
	if strings.Contains(out, "private diagnostic") || !strings.Contains(out, "dispatch_unconfirmed") || f.dispatches != 2 {
		t.Fatal(out)
	}
	call("remote-recorded-status", status, 503, true)
	if f.dispatches != 2 || f.reads != 2 {
		t.Fatal("uncertain status retried")
	}
	h.remoteAutomatic = nil
	call("remote-auto-dispatch", body, 503, true)
	call("remote-recorded-status", status, 503, true)
}
