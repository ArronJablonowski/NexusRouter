package webuiapp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/submissions"
)

type browserRemoteController struct {
	reads, cancels    int
	state, submission string
	fail              bool
}

func (c *browserRemoteController) Status(context.Context, string, string) (submissions.Status, error) {
	c.reads++
	return submissions.Status{Version: 1, ID: c.submission, State: c.state, ConfigDigest: "not-for-browser", Result: &submissions.Result{Text: "output <text>"}}, nil
}
func (c *browserRemoteController) Cancel(context.Context, string, string) (submissions.Status, error) {
	c.cancels++
	if c.fail {
		return submissions.Status{}, errors.New("uncertain")
	}
	return submissions.Status{Version: 1, ID: c.submission, State: "running", CancelRequested: true}, nil
}
func TestRemoteTaskControlAuthorityFencesAndUncertainCancellation(t *testing.T) {
	h := mutationHandlerFixture(t, MutationServices{})
	c := &browserRemoteController{state: "running", submission: "submission-a"}
	h.remoteTaskController = c
	body := `{"version":1,"instance":"node-a","request_id":"request-existing-0001","action":"cancel","expected_submission_id":"submission-a"}`
	call := func(r *http.Request, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%d want %d: %s", w.Code, want, w.Body.String())
		}
		return w
	}
	request := func(body string) *http.Request {
		return authorizedMutationRequest(t, h, "/app/api/v1/remote-task-control", body)
	}
	call(browserRequest(http.MethodPost, "/app/api/v1/remote-task-control", body), 401)
	r := request(body)
	r.Header.Del("X-Darwin-CSRF")
	call(r, 403)
	call(request(strings.Replace(body, "request-existing-0001", "../malformed", 1)), 400)
	call(request(strings.Replace(body, `"expected_submission_id":"submission-a"`, `"extra":"x"`, 1)), 400)
	if c.reads != 0 || c.cancels != 0 {
		t.Fatal("unauthorized remote call")
	}
	call(request(strings.Replace(body, "submission-a", "stale-submission", 1)), 409)
	c.state = "succeeded"
	call(request(body), 409)
	if c.cancels != 0 {
		t.Fatal("stale or terminal cancellation")
	}
	w := call(request(`{"version":1,"instance":"node-a","request_id":"request-existing-0001","action":"status"}`), 200)
	if !strings.Contains(w.Body.String(), "output") || strings.Contains(w.Body.String(), "not-for-browser") {
		t.Fatal("incorrect projection", w.Body.String())
	}
	c.state = "running"
	w = call(request(body), 200)
	if c.cancels != 1 || !strings.Contains(w.Body.String(), `"cancel_requested":true`) {
		t.Fatal(w.Body.String())
	}
	c.fail = true
	call(request(body), 503)
	if c.cancels != 2 {
		t.Fatal("cancellation replayed")
	}
	h.remoteTaskController = nil
	call(request(body), 503)
	if c.cancels != 2 {
		t.Fatal("disabled control")
	}
}
