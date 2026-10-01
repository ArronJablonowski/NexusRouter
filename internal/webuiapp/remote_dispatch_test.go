package webuiapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

type browserDispatchFake struct {
	calls int
	fail  bool
}

func (d *browserDispatchFake) Dispatch(context.Context, string, string, remote.Task) (submissions.Status, error) {
	d.calls++
	if d.fail {
		return submissions.Status{}, errors.New("secret path")
	}
	return submissions.Status{Version: 1, ID: "submission-a", State: "queued", ConfigDigest: "private-config"}, nil
}
func browserDispatchBody(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(remoteDispatchRequest{Version: 1, Instance: "node-a", RequestID: "request-browser-0001", Task: remote.Task{Version: 1, ModelID: "model-a", Prompt: "test task", Domain: "coding", Profile: "default", ContextTokens: 8192, Private: true}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func TestRemoteBrowserDispatchAuthorityAndUncertainDelivery(t *testing.T) {
	h := mutationHandlerFixture(t, MutationServices{})
	d := &browserDispatchFake{}
	h.remoteDispatcher = d
	body := browserDispatchBody(t)
	request := func(b string) *http.Request { return authorizedMutationRequest(t, h, "/app/api/v1/remote-dispatch", b) }
	call := func(r *http.Request, want int) string {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%d want %d: %s", w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	call(browserRequest(http.MethodPost, "/app/api/v1/remote-dispatch", body), 401)
	r := request(body)
	r.Header.Del("X-Darwin-CSRF")
	call(r, 403)
	call(request(strings.Replace(body, "request-browser-0001", "bad", 1)), 400)
	call(request(strings.Replace(body, `"prompt":"test task"`, `"prompt":""`, 1)), 400)
	call(request(strings.Replace(body, `"model_id"`, `"extra"`, 1)), 400)
	if d.calls != 0 {
		t.Fatal("unauthorized dispatch")
	}
	out := call(request(body), 200)
	if !strings.Contains(out, "submission-a") || strings.Contains(out, "private-config") {
		t.Fatal(out)
	}
	d.fail = true
	out = call(request(body), 503)
	if d.calls != 2 || strings.Contains(out, "secret") || !strings.Contains(out, "dispatch_unconfirmed") {
		t.Fatal("uncertain result leaked or replayed", out, d.calls)
	}
	h.remoteDispatcher = nil
	call(request(body), 503)
	if d.calls != 2 {
		t.Fatal("disabled dispatch")
	}
}
