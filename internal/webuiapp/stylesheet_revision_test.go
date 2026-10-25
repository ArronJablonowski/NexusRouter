package webuiapp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserDigestStylesheetPreservesAssetBoundary(t *testing.T) {
	handler := handlerFixture(t)
	cookie, _ := authenticateBrowser(t, handler)
	read := func(method, path string, authenticated bool) *httptest.ResponseRecorder {
		request := browserRequest(method, path, "")
		if authenticated {
			request.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	canonical := read("GET", "/app/assets/v1/app.css", true)
	if canonical.Code != http.StatusOK {
		t.Fatal("canonical stylesheet unavailable", canonical.Code)
	}
	digest := strings.Trim(canonical.Header().Get("ETag"), "\"")
	if len(digest) != 64 {
		t.Fatal("invalid stylesheet digest")
	}
	path := "/app/assets/v1/app." + digest + ".css"
	for _, method := range []string{"GET", "HEAD"} {
		response := read(method, path, true)
		if response.Code != http.StatusOK || response.Header().Get("ETag") != canonical.Header().Get("ETag") || response.Header().Get("Content-Security-Policy") != canonical.Header().Get("Content-Security-Policy") || response.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("revision asset boundary changed", method, response.Code)
		}
		if method == "GET" && response.Body.String() != canonical.Body.String() {
			t.Fatal("digest asset bytes differ")
		}
		if method == "HEAD" && response.Body.Len() != 0 {
			t.Fatal("HEAD included stylesheet body")
		}
		if denied := read(method, path, false); denied.Code != http.StatusUnauthorized {
			t.Fatal("digest asset bypassed authentication", method, denied.Code)
		}
	}
	for _, invalid := range []string{"/app/assets/v1/app." + strings.Repeat("0", 64) + ".css", "/app/assets/v1/app.bad.css", "/app/assets/v1/app." + digest + ".js"} {
		if response := read("GET", invalid, true); response.Code != http.StatusNotFound {
			t.Fatal("unknown asset accepted", invalid, response.Code)
		}
	}
	if response := read("GET", "/app/assets/v1/app.css?revision="+digest, true); response.Code != http.StatusBadRequest {
		t.Fatal("strict asset query policy changed", response.Code)
	}
}
