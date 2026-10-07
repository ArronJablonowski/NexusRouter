package webuiapp

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestModelUseRequiresAuthority(t *testing.T) {
	calls := 0
	h := mutationHandlerFixture(t, MutationServices{ModelUse: func(_ context.Context, host, model string, enabled *bool) (any, error) {
		calls++
		return config.ModelUsePolicy{Version: 1, Disabled: map[string]bool{}}, nil
	}})
	body := `{"version":1,"host":"local","model":"m","enabled":false}`
	w := httptest.NewRecorder()
	h.ServeHTTP(w, browserRequest(http.MethodPost, "/app/api/v1/model-use", body))
	if w.Code != 401 || calls != 0 {
		t.Fatal(w.Code, calls)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, authorizedMutationRequest(t, h, "/app/api/v1/model-use", body))
	if w.Code != 200 || calls != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
}
