package webuiapp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/maintenance"
)

func TestCleanupAuthorityAndConflict(t *testing.T) {
	calls := 0
	handler := mutationHandlerFixture(t, MutationServices{Cleanup: func(_ context.Context, action, token string) (maintenance.Report, error) {
		calls++
		if action == "run" {
			return maintenance.Report{}, maintenance.ErrChanged
		}
		return maintenance.Report{Version: 1}, nil
	}})
	request := browserRequest(http.MethodGet, "/app/api/v1/cleanup", "")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, request)
	if w.Code != 401 || calls != 0 {
		t.Fatal(w.Code, calls)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, authorizedMutationRequest(t, handler, "/app/api/v1/cleanup", `{"version":1,"action":"preview","token":""}`))
	if w.Code != 200 || calls != 1 {
		t.Fatal(w.Code, w.Body.String(), calls)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, authorizedMutationRequest(t, handler, "/app/api/v1/cleanup", `{"version":1,"action":"run","token":"`+strings.Repeat("a", 64)+`"}`))
	if w.Code != 409 || calls != 2 {
		t.Fatal(w.Code, w.Body.String(), calls)
	}
	for _, body := range []string{`{"version":1,"action":"run"}`, `{"version":1,"action":"delete-all"}`, `{"version":1,"action":"preview","path":"/tmp"}`, `{"version":1,"action":"preview","token":"abc"}`} {
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, authorizedMutationRequest(t, handler, "/app/api/v1/cleanup", body))
		if w.Code != 400 {
			t.Fatal(body, w.Code)
		}
	}
	if calls != 2 {
		t.Fatal("invalid request reached cleanup", calls)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, browserRequest(http.MethodPost, "/app/api/v1/cleanup", `{"version":1,"action":"preview"}`))
	if w.Code == 200 || calls != 2 {
		t.Fatal("missing authority")
	}
}

func TestCleanupStatusAndFailedAuthorityNeverDispatch(t *testing.T) {
	calls := 0
	handler := mutationHandlerFixture(t, MutationServices{Cleanup: func(_ context.Context, action, token string) (maintenance.Report, error) {
		calls++
		if action != "status" {
			t.Fatal(action)
		}
		return maintenance.Report{Version: 1}, nil
	}})
	request := authorizedMutationRequest(t, handler, "/app/api/v1/cleanup", `{"version":1}`)
	request.Method = http.MethodGet
	request.Body = http.NoBody
	request.ContentLength = 0
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, request)
	if w.Code != 200 || calls != 1 {
		t.Fatal(w.Code, calls)
	}
	request.URL.RawQuery = "path=/tmp"
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, request)
	if w.Code != 400 || calls != 1 {
		t.Fatal(w.Code, calls)
	}
	for _, header := range []string{"X-Darwin-CSRF", "Origin"} {
		request := authorizedMutationRequest(t, handler, "/app/api/v1/cleanup", `{"version":1,"action":"preview"}`)
		request.Header.Del(header)
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, request)
		if w.Code != 403 || calls != 1 {
			t.Fatal(header, w.Code, calls)
		}
	}
}

func TestCleanupRunSurvivesBrowserDisconnect(t *testing.T) {
	called := false
	handler := mutationHandlerFixture(t, MutationServices{Cleanup: func(ctx context.Context, action, token string) (maintenance.Report, error) {
		called = true
		if ctx.Err() != nil {
			t.Fatal("cleanup inherited browser cancellation")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("cleanup lost its bound")
		}
		return maintenance.Report{Version: 1}, nil
	}})
	request := authorizedMutationRequest(t, handler, "/app/api/v1/cleanup", `{"version":1,"action":"run","token":"`+strings.Repeat("a", 64)+`"}`)
	ctx, cancel := context.WithCancel(request.Context())
	cancel()
	request = request.WithContext(ctx)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, request)
	if w.Code != 200 || !called {
		t.Fatal(w.Code, called)
	}
}
