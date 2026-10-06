package webuiapp

import (
	"context"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLoggingRequiresAuthenticationAndReadOnlyGET(t *testing.T) {
	h := mutationHandlerFixture(t, MutationServices{})
	calls := 0
	h.inspections.Logging = func(context.Context) (contract.LoggingPage, error) {
		calls++
		return contract.LoggingPage{Version: 1, ObservedAt: time.Now().UTC(), Items: []contract.LogLocation{}}, nil
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, browserRequest(http.MethodGet, "/app/api/v1/logging", ""))
	if w.Code != 401 || calls != 0 {
		t.Fatal("unauthorized metadata", w.Code)
	}
	cookie, _ := authenticateBrowser(t, h)
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		r := browserRequest(method, "/app/api/v1/logging", "")
		r.Body = http.NoBody
		r.AddCookie(cookie)
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if method == http.MethodGet {
			if w.Code != 200 || calls != 1 {
				t.Fatal("metadata unavailable", w.Code)
			}
		} else if w.Code < 400 || calls != 0 {
			t.Fatal("mutation accepted")
		}
	}
}
