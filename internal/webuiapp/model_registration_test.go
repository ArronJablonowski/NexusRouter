package webuiapp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelRegistrationRequiresAuthorityAndDoesNotRetry(t *testing.T) {
	calls := 0
	fail := false
	h := mutationHandlerFixture(t, MutationServices{RegisterModel: func(_ context.Context, r ModelRegistration) (string, error) {
		calls++
		if r.CatalogID != "catalog" || r.TemplateID != "template" || r.ContextTokens != 4096 {
			t.Fatal(r)
		}
		if fail {
			return "", errors.New("private path")
		}
		return strings.Repeat("a", 64), nil
	}})
	body := `{"version":1,"expected_digest":"` + strings.Repeat("b", 64) + `","catalog_id":"catalog","template_id":"template","context_tokens":4096}`
	call := func(r *http.Request, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want || strings.Contains(w.Body.String(), "private path") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	call(browserRequest("POST", "/app/api/v1/model-registration", body), 401)
	request := func() *http.Request { return authorizedMutationRequest(t, h, "/app/api/v1/model-registration", body) }
	r := request()
	r.Header.Del("X-Darwin-CSRF")
	call(r, 403)
	if calls != 0 {
		t.Fatal("unauthorized call")
	}
	call(request(), 200)
	if calls != 1 {
		t.Fatal("repeated")
	}
	fail = true
	call(request(), 409)
	if calls != 2 {
		t.Fatal("failure retried")
	}
}
