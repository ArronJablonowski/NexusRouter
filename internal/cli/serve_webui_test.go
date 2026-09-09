package cli

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBrowserMountOwnsOnlyExactConfiguredSubtree(t *testing.T) {
	browserCalls, nativeCalls := 0, 0
	mount := browserMount{
		basePath: "/app",
		browser: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			browserCalls++
			writer.WriteHeader(http.StatusNoContent)
		}),
		native: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			nativeCalls++
			writer.WriteHeader(http.StatusNotFound)
		}),
	}
	for _, test := range []struct {
		path        string
		wantBrowser bool
	}{
		{"/app", true}, {"/app/chats", true}, {"/application", false}, {"/v1/tasks", false}, {"/health", false},
	} {
		response := httptest.NewRecorder()
		mount.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if (response.Code == http.StatusNoContent) != test.wantBrowser {
			t.Fatal("incorrect handler ownership", test.path, response.Code)
		}
	}
	if browserCalls != 2 || nativeCalls != 3 {
		t.Fatal("unexpected route dispatch", browserCalls, nativeCalls)
	}
}

func TestDisabledBrowserUsesUnchangedNativeHandler(t *testing.T) {
	calls := 0
	native := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls++
		writer.WriteHeader(http.StatusTeapot)
	})
	handler := composeServeHandler(native, nil, "/app")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/app", nil))
	if response.Code != http.StatusTeapot || calls != 1 {
		t.Fatal("disabled Web UI changed native routing", response.Code, calls)
	}
}
