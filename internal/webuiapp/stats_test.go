package webuiapp

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/internal/usagestats"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStatsResetRequiresSessionCSRFAndConfirmation(t *testing.T) {
	h := mutationHandlerFixture(t, MutationServices{})
	calls := 0
	h.inspections.Stats = func(_ context.Context, r *usagestats.Reset) (usagestats.Snapshot, error) {
		calls++
		if r.Revision == 9 {
			return usagestats.Snapshot{}, usagestats.ErrConflict
		}
		return usagestats.Snapshot{Version: 1}, nil
	}
	body := `{"version":1,"locality":"cloud","revision":0,"confirm":true}`
	w := httptest.NewRecorder()
	h.ServeHTTP(w, browserRequest(http.MethodPost, "/app/api/v1/stats", body))
	if w.Code < 400 || calls != 0 {
		t.Fatal("unauthorized reset", w.Code, calls)
	}
	r := authorizedMutationRequest(t, h, "/app/api/v1/stats", body)
	r.Header.Del("X-Darwin-CSRF")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code < 400 || calls != 0 {
		t.Fatal("missing csrf accepted")
	}
	for _, bad := range []string{`{"version":1,"locality":"cloud","revision":0,"confirm":false}`, `{"version":1,"locality":"other","revision":0,"confirm":true}`} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, authorizedMutationRequest(t, h, "/app/api/v1/stats", bad))
		if w.Code != 400 || calls != 0 {
			t.Fatal("bad reset accepted", w.Code, calls)
		}
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, authorizedMutationRequest(t, h, "/app/api/v1/stats", body))
	if w.Code != 200 || calls != 1 {
		t.Fatal(w.Code, w.Body.String(), calls)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, authorizedMutationRequest(t, h, "/app/api/v1/stats", `{"version":1,"locality":"local","revision":9,"confirm":true}`))
	if w.Code != 409 {
		t.Fatal("conflict missing", w.Code)
	}
}
