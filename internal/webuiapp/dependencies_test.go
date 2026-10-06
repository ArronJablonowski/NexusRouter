package webuiapp

import (
	"context"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDependenciesRequiresAuthenticationAndReadOnlyGET(t *testing.T) {
	h := mutationHandlerFixture(t, MutationServices{})
	calls := 0
	h.inspections.Dependencies = func(context.Context) (contract.DependencyInventory, error) {
		calls++
		return contract.DependencyInventory{Version: 1, ObservedAt: time.Now().UTC(), Items: []contract.Dependency{}}, nil
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, browserRequest(http.MethodGet, "/app/api/v1/dependencies", ""))
	if w.Code != 401 || calls != 0 {
		t.Fatal("unauthorized metadata", w.Code)
	}
	cookie, _ := authenticateBrowser(t, h)
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		r := browserRequest(method, "/app/api/v1/dependencies", "")
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
