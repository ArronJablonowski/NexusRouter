package webuiapp

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/submissions"
	"net/http/httptest"
	"testing"
	"time"
)

func TestActiveJobsAuthenticatedFilteredAndFailClosed(t *testing.T) {
	h := handlerFixture(t)
	calls := 0
	h.reads.Tasks = func(_ context.Context, o sessions.TaskListOptions) (sessions.TaskPage, error) {
		calls++
		if o.State != "running" || o.Limit != 100 {
			t.Fatal(o)
		}
		return sessions.TaskPage{Version: 1, Items: []sessions.TaskSummary{{Version: 1, TaskID: "task", SessionID: "chat", State: "running", Sequence: 1, StartedAt: time.Now()}}}, nil
	}
	out := httptest.NewRecorder()
	h.ServeHTTP(out, browserGET("/app/api/v1/jobs", nil))
	if out.Code != 401 || calls != 0 {
		t.Fatal(out.Code, calls)
	}
	cookie, _ := authenticateBrowser(t, h)
	for _, q := range []string{"", "?state=completed", "?limit=1000", "?after=x", "?kind=queued&kind=running"} {
		out = httptest.NewRecorder()
		h.ServeHTTP(out, browserGET("/app/api/v1/jobs"+q, cookie))
		if (q == "") != (out.Code == 200) {
			t.Fatal(q, out.Code)
		}
	}
	h.reads.Tasks = func(context.Context, sessions.TaskListOptions) (sessions.TaskPage, error) { panic("private error") }
	out = httptest.NewRecorder()
	h.ServeHTTP(out, browserGET("/app/api/v1/jobs", cookie))
	if out.Code != 503 {
		t.Fatal(out.Code)
	}
	h.reads.Submissions = func(_ context.Context, o submissions.ListOptions) (submissions.Page, error) {
		if o.State != "queued" {
			t.Fatal(o)
		}
		return submissions.Page{Version: 1, Items: []submissions.Summary{}}, nil
	}
	out = httptest.NewRecorder()
	h.ServeHTTP(out, browserGET("/app/api/v1/jobs?kind=queued", cookie))
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
}
