package webuiapp

import (
	"context"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCollaborationInspectionAuthorityAndQuery(t *testing.T) {
	h := mutationHandlerFixture(t, MutationServices{})
	calls := 0
	h.inspections.Collaboration = func(_ context.Context, o contract.CollaborationOptions) (contract.CollaborationPage, error) {
		calls++
		if o.Before != 3 || o.Topic != "parser" {
			t.Fatal(o)
		}
		return contract.CollaborationPage{Version: 1, Enabled: true, Messages: []contract.CollaborationMessage{}}, nil
	}
	target := "/app/api/v1/collaboration?topic=parser&before=3"
	r := httptest.NewRecorder()
	h.ServeHTTP(r, browserGET(target, nil))
	if r.Code != 401 || calls != 0 {
		t.Fatal(r.Code, calls)
	}
	cookie, _ := authenticateBrowser(t, h)
	r = httptest.NewRecorder()
	h.ServeHTTP(r, browserGET(target, cookie))
	if r.Code != 200 || calls != 1 {
		t.Fatal(r.Code, r.Body.String(), calls)
	}
	for _, bad := range []string{"/app/api/v1/collaboration?before=-1", "/app/api/v1/collaboration?topic=x&topic=y", "/app/api/v1/collaboration?unknown=x"} {
		r = httptest.NewRecorder()
		h.ServeHTTP(r, browserGET(bad, cookie))
		if r.Code == 200 || calls != 1 {
			t.Fatal(bad, r.Code, calls)
		}
	}
	r = httptest.NewRecorder()
	req := browserGET(target, cookie)
	req.Method = http.MethodPost
	h.ServeHTTP(r, req)
	if r.Code == 200 || calls != 1 {
		t.Fatal("mutation accepted", r.Code)
	}
}
