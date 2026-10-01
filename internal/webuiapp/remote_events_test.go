package webuiapp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

type browserEventController struct {
	browserRemoteController
	events int
	denied bool
	page   sessions.EventPage
}

func (c *browserEventController) Status(ctx context.Context, instance, request string) (submissions.Status, error) {
	c.reads++
	if c.denied {
		return submissions.Status{}, errors.New("revoked")
	}
	return submissions.Status{Version: 1, ID: "submission-a", State: "running", TaskIDs: []string{"task-1"}}, nil
}
func (c *browserEventController) Events(context.Context, string, string, string, int64) (sessions.EventPage, error) {
	c.events++
	return c.page, nil
}
func browserEventPage() sessions.EventPage {
	return sessions.EventPage{Version: 1, TaskID: "task-1", SessionID: "session-1", State: "running", NextSequence: 1, HeadSequence: 1, Events: []runtime.Event{{Version: 1, ID: "event-1", TaskID: "task-1", SessionID: "session-1", CorrelationID: "task-1", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted, Data: runtime.Data{Text: "private prompt", ConfigID: strings.Repeat("a", 64)}}}}
}
func TestRemoteEventsAuthorityOwnershipValidationAndProjection(t *testing.T) {
	h := mutationHandlerFixture(t, MutationServices{})
	c := &browserEventController{page: browserEventPage()}
	h.remoteTaskController = c
	body := `{"version":1,"instance":"node-a","request_id":"request-existing-0001","task_id":"task-1","after":0}`
	call := func(r *http.Request, want int) string {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%d want %d: %s", w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	request := func(b string) *http.Request {
		return authorizedMutationRequest(t, h, "/app/api/v1/remote-task-events", b)
	}
	call(browserRequest(http.MethodPost, "/app/api/v1/remote-task-events", body), 401)
	r := request(body)
	r.Header.Del("X-Darwin-CSRF")
	call(r, 403)
	call(request(strings.Replace(body, `"after":0`, `"after":-1`, 1)), 400)
	call(request(strings.Replace(body, `"after":0`, `"after":10001`, 1)), 400)
	call(request(strings.Replace(body, `"after":0`, `"unexpected":0`, 1)), 400)
	if c.reads != 0 || c.events != 0 {
		t.Fatal("unauthorized request contacted peer")
	}
	call(request(strings.Replace(body, "task-1", "unowned-task", 1)), 503)
	if c.events != 0 {
		t.Fatal("unowned events fetched")
	}
	out := call(request(body), 200)
	if !strings.Contains(out, "task.started") || strings.Contains(out, "private") || strings.Contains(out, "session-1") || strings.Contains(out, strings.Repeat("a", 64)) {
		t.Fatal("unsafe or missing projection", out)
	}
	c.page.FromSequence = 1
	call(request(body), 503)
	c.page = browserEventPage()
	c.page.Events[0].Sequence = 2
	call(request(body), 503)
	c.page = browserEventPage()
	c.denied = true
	before := c.events
	call(request(body), 503)
	if c.events != before {
		t.Fatal("revoked events fetched")
	}
	h.remoteTaskController = nil
	call(request(body), 503)
}
