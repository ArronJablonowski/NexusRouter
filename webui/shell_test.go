package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func shellFixture(t *testing.T) http.Handler {
	t.Helper()
	handler, err := NewShellHandler(ShellOptions{
		BasePath:      "/console",
		HostAllowed:   func(host string) bool { return host == "darwin.local" },
		Authenticated: func(request *http.Request) bool { return request.Header.Get("X-Test-Session") == "valid" },
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func shellRequest(t *testing.T, handler http.Handler, method, target string, authenticated bool) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "http://darwin.local"+target, nil)
	if authenticated {
		request.Header.Set("X-Test-Session", "valid")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestShellServesEmbeddedAssetsAndClientRoutes(t *testing.T) {
	handler := shellFixture(t)
	for _, test := range []struct{ target, contentType, contains string }{
		{"/console", "text/html", "/console/assets/v1/app.js"},
		{"/console/chats/chat-a", "text/html", "DarwinRouter"},
		{"/console/assets/v1/app.css", "text/css", "color-scheme"},
		{"/console/assets/v1/app.js", "text/javascript", "aria-current"},
	} {
		response := shellRequest(t, handler, http.MethodGet, test.target, true)
		if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), test.contentType) || !strings.Contains(response.Body.String(), test.contains) {
			t.Fatalf("unexpected response for %s: %d %q", test.target, response.Code, response.Body.String())
		}
	}
}

func TestShellRequiresHostAndAuthenticationForEveryResource(t *testing.T) {
	handler := shellFixture(t)
	unauthorized := shellRequest(t, handler, http.MethodGet, "/console/assets/v1/app.js", false)
	if unauthorized.Code != http.StatusUnauthorized || strings.Contains(unauthorized.Body.String(), "app.js") {
		t.Fatal("asset bypassed authentication or leaked path")
	}
	request := httptest.NewRequest(http.MethodGet, "http://evil.example/console", nil)
	request.Header.Set("X-Test-Session", "valid")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatal("untrusted Host accepted", response.Code)
	}
}

func TestShellRejectsTraversalInvalidMethodsAndUnknownAssets(t *testing.T) {
	handler := shellFixture(t)
	for _, target := range []string{"/console/assets/v1/missing.js", "/console/route.json", "/outside", "/console//chats", "/console/" + strings.Repeat("a", MaxShellRequestPath)} {
		if response := shellRequest(t, handler, http.MethodGet, target, true); response.Code != http.StatusNotFound {
			t.Fatalf("unsafe or unknown path accepted: %s -> %d", target, response.Code)
		}
	}
	encoded := httptest.NewRequest(http.MethodGet, "http://darwin.local/console/%2e%2e/secret", nil)
	encoded.Header.Set("X-Test-Session", "valid")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, encoded)
	if response.Code != http.StatusNotFound {
		t.Fatal("encoded traversal accepted", response.Code)
	}
	method := shellRequest(t, handler, http.MethodPost, "/console", true)
	if method.Code != http.StatusMethodNotAllowed || method.Header().Get("Allow") != "GET, HEAD" {
		t.Fatal("invalid method handling", method.Code)
	}
}

func TestShellAppliesSecurityHeadersToSuccessAndFailure(t *testing.T) {
	handler := shellFixture(t)
	for _, response := range []*httptest.ResponseRecorder{
		shellRequest(t, handler, http.MethodGet, "/console", true),
		shellRequest(t, handler, http.MethodGet, "/console", false),
	} {
		for name, want := range map[string]string{
			"Content-Security-Policy":    shellCSP,
			"Referrer-Policy":            "no-referrer",
			"X-Content-Type-Options":     "nosniff",
			"X-Frame-Options":            "DENY",
			"Cross-Origin-Opener-Policy": "same-origin",
			"Cache-Control":              "no-store",
		} {
			if got := response.Header().Get(name); got != want {
				t.Fatalf("%s = %q, want %q", name, got, want)
			}
		}
		if response.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("CORS header emitted")
		}
	}
}

func TestShellHEADAndConfigurationBounds(t *testing.T) {
	handler := shellFixture(t)
	response := shellRequest(t, handler, http.MethodHead, "/console/chats", true)
	if response.Code != http.StatusOK || response.Body.Len() != 0 || response.Header().Get("Content-Length") == "" {
		t.Fatal("invalid HEAD response")
	}
	for _, options := range []ShellOptions{
		{},
		{BasePath: "/bad/", HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }},
		{BasePath: "/" + strings.Repeat("a", MaxShellBasePath), HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }},
	} {
		if _, err := NewShellHandler(options); err == nil {
			t.Fatal("invalid shell configuration accepted")
		}
	}
}

func TestEmbeddedShellHasNoExternalResourcesOrInlineCode(t *testing.T) {
	digest, err := ShellAssetDigest()
	if err != nil || digest != "666f33ee52b72162e1d9029961b803e2549a611607df91079b7e53708800bb10" || ShellAssetVersion != "v1" {
		t.Fatal("embedded shell manifest changed without a versioned review", digest, err)
	}
	for _, name := range []string{"assets/v1/index.html", "assets/v1/app.css", "assets/v1/app.js", "assets/v1/bootstrap.html", "assets/v1/bootstrap.css", "assets/v1/bootstrap.js"} {
		file, err := embeddedShellAssets.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(file)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(body))
		for _, forbidden := range []string{"http://", "https://", "//cdn", "<iframe", "<object", "<embed", "serviceworker", "localstorage", "sessionstorage", "indexeddb"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("external or active resource %q in %s", forbidden, name)
			}
		}
	}
	index, _ := embeddedShellAssets.ReadFile("assets/v1/index.html")
	bootstrap, _ := embeddedShellAssets.ReadFile("assets/v1/bootstrap.html")
	if strings.Contains(string(index), "<script>") || strings.Contains(string(index), " style=") || strings.Contains(string(bootstrap), "<script>") || strings.Contains(string(bootstrap), " style=") {
		t.Fatal("inline code conflicts with shell CSP")
	}
	bootstrapScript, _ := embeddedShellAssets.ReadFile("assets/v1/bootstrap.js")
	for _, required := range []string{"challenge.expires_at", "Date.now() >= expiresAt", "retry.hidden = false"} {
		if !strings.Contains(string(bootstrapScript), required) {
			t.Fatal("bootstrap expiry cannot reach retry state", required)
		}
	}
}

func TestBootstrapHandlerServesOnlyNarrowPublicAssets(t *testing.T) {
	handler, err := NewBootstrapHandler(BootstrapOptions{BasePath: "/console", HostAllowed: func(host string) bool { return host == "darwin.local" }})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/console/bootstrap", "/console/bootstrap/v1/bootstrap.css", "/console/bootstrap/v1/bootstrap.js"} {
		request := httptest.NewRequest(http.MethodGet, "http://darwin.local"+target, nil)
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatal("bootstrap asset unavailable", target, response.Code)
		}
	}
	for _, target := range []string{"/console", "/console/assets/v1/app.js", "/console/bootstrap/unknown"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://darwin.local"+target, nil))
		if response.Code != http.StatusNotFound {
			t.Fatal("bootstrap handler exposed application resource", target, response.Code)
		}
	}
}
