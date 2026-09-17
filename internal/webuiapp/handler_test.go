package webuiapp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/browserauth"
	contract "github.com/ArronJablonowski/DarwinRouter/webui"
)

func handlerFixture(t *testing.T) *Handler {
	t.Helper()
	store, err := browserauth.New(browserauth.Options{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Options{BasePath: "/app", AllowedHosts: []string{"127.0.0.1:7788"}, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func browserRequest(method, target, body string) *http.Request {
	request := httptest.NewRequest(method, "http://127.0.0.1:7788"+target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://127.0.0.1:7788")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	return request
}

func authenticateBrowser(t *testing.T, handler *Handler) (*http.Cookie, string) {
	t.Helper()
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, browserRequest(http.MethodPost, "/app/api/v1/session/challenges", `{"version":1}`))
	if created.Code != http.StatusCreated || len(created.Result().Cookies()) != 1 {
		t.Fatal("challenge creation failed", created.Code, created.Body.String())
	}
	var challenge contract.BrowserChallengeResponse
	if json.Unmarshal(created.Body.Bytes(), &challenge) != nil || challenge.Validate() != nil {
		t.Fatal("invalid challenge response")
	}
	if err := handler.ApproveChallenge(challenge.ChallengeID, challenge.DisplayCode); err != nil {
		t.Fatal(err)
	}
	request := browserRequest(http.MethodPost, "/app/api/v1/session", `{"version":1,"challenge_id":"`+challenge.ChallengeID+`"}`)
	request.AddCookie(created.Result().Cookies()[0])
	completed := httptest.NewRecorder()
	handler.ServeHTTP(completed, request)
	if completed.Code != http.StatusCreated {
		t.Fatal("session completion failed", completed.Code, completed.Body.String())
	}
	var session contract.BrowserSessionResponse
	if json.Unmarshal(completed.Body.Bytes(), &session) != nil || session.Validate() != nil {
		t.Fatal("invalid session response")
	}
	var sessionCookie *http.Cookie
	for _, cookie := range completed.Result().Cookies() {
		if cookie.Name == sessionCookieName() && cookie.Value != "" {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteStrictMode || sessionCookie.Path != "/app" {
		t.Fatal("invalid session cookie")
	}
	return sessionCookie, session.CSRFToken
}

func TestBrowserBootstrapShellAndLogout(t *testing.T) {
	handler := handlerFixture(t)
	for _, target := range []string{"/app", "/app/", "/app/chats", "/app/chats/session-1", "/app/workboards", "/app/workboards/board-1"} {
		unauthorized := httptest.NewRecorder()
		handler.ServeHTTP(unauthorized, browserRequest(http.MethodGet, target, ""))
		if unauthorized.Code != http.StatusFound || unauthorized.Header().Get("Location") != "/app/bootstrap" {
			t.Fatal("shell route did not enter bounded bootstrap", target, unauthorized.Code)
		}
	}
	bootstrap := httptest.NewRecorder()
	handler.ServeHTTP(bootstrap, browserRequest(http.MethodGet, "/app/bootstrap", ""))
	if bootstrap.Code != http.StatusOK || !strings.Contains(bootstrap.Body.String(), "Connect this browser") {
		t.Fatal("bootstrap document unavailable", bootstrap.Code)
	}
	cookie, csrf := authenticateBrowser(t, handler)
	shellRequest := browserRequest(http.MethodGet, "/app/workboards", "")
	shellRequest.AddCookie(cookie)
	shell := httptest.NewRecorder()
	handler.ServeHTTP(shell, shellRequest)
	if shell.Code != http.StatusOK || !strings.Contains(shell.Body.String(), "DarwinRouter") {
		t.Fatal("authenticated shell unavailable", shell.Code)
	}
	logoutRequest := browserRequest(http.MethodPost, "/app/api/v1/session/logout", `{"version":1}`)
	logoutRequest.AddCookie(cookie)
	logoutRequest.Header.Set("X-Darwin-CSRF", csrf)
	logout := httptest.NewRecorder()
	handler.ServeHTTP(logout, logoutRequest)
	if logout.Code != http.StatusNoContent {
		t.Fatal("logout failed", logout.Code, logout.Body.String())
	}
	shell = httptest.NewRecorder()
	handler.ServeHTTP(shell, shellRequest)
	if shell.Code != http.StatusFound || shell.Header().Get("Location") != "/app/bootstrap" {
		t.Fatal("revoked session did not re-enter bootstrap", shell.Code)
	}
}

func TestBrowserBootstrapRedirectDoesNotMaskAssetsOrUnknownRoutes(t *testing.T) {
	handler := handlerFixture(t)
	for _, target := range []string{"/app/assets/v1/app.js", "/app/chats/not/one-id", "/app/workboards/not/one-id", "/app/unknown"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, browserRequest(http.MethodGet, target, ""))
		if response.Code == http.StatusFound {
			t.Fatal("non-navigation path entered bootstrap", target)
		}
	}
}

func TestBrowserCSRFCanBeRotatedAfterRefresh(t *testing.T) {
	handler := handlerFixture(t)
	cookie, previous := authenticateBrowser(t, handler)
	request := browserRequest(http.MethodPost, "/app/api/v1/session/csrf", `{"version":1}`)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var session contract.BrowserSessionResponse
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &session) != nil || session.Validate() != nil || session.CSRFToken == previous {
		t.Fatal("CSRF refresh unavailable", response.Code, response.Body.String())
	}
	logout := func(csrf string) int {
		request := browserRequest(http.MethodPost, "/app/api/v1/session/logout", `{"version":1}`)
		request.AddCookie(cookie)
		request.Header.Set("X-Darwin-CSRF", csrf)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code
	}
	if logout(previous) != http.StatusNoContent {
		t.Fatal("CSRF rotation broke an existing browser tab")
	}
}

func TestBrowserBoundaryRejectsOriginCSRFHostForwardingAndBearer(t *testing.T) {
	handler := handlerFixture(t)
	cookie, csrf := authenticateBrowser(t, handler)
	for name, mutate := range map[string]func(*http.Request){
		"origin":    func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") },
		"fetch":     func(r *http.Request) { r.Header.Del("Sec-Fetch-Site") },
		"csrf":      func(r *http.Request) { r.Header.Set("X-Darwin-CSRF", "wrong") },
		"forwarded": func(r *http.Request) { r.Header.Set("X-Forwarded-Host", "evil.example") },
		"host":      func(r *http.Request) { r.Host = "evil.example" },
	} {
		t.Run(name, func(t *testing.T) {
			request := browserRequest(http.MethodPost, "/app/api/v1/unknown", `{}`)
			request.AddCookie(cookie)
			request.Header.Set("X-Darwin-CSRF", csrf)
			mutate(request)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code < 400 || response.Code == http.StatusNotFound {
				t.Fatal("boundary bypass accepted", response.Code)
			}
		})
	}
	bearer := browserRequest(http.MethodGet, "/app", "")
	bearer.Header.Set("Authorization", "Bearer fixture-token-at-least-32-characters")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, bearer)
	if response.Code == http.StatusOK || response.Header().Get("Location") != "/app/bootstrap" {
		t.Fatal("native bearer authenticated browser shell", response.Code)
	}
}

func TestBrowserAPINeverFallsThroughAndErrorsCarrySecurityHeaders(t *testing.T) {
	handler := handlerFixture(t)
	cookie, csrf := authenticateBrowser(t, handler)
	request := browserRequest(http.MethodPost, "/app/api/v1/not-implemented", `{}`)
	request.AddCookie(cookie)
	request.Header.Set("X-Darwin-CSRF", csrf)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "<!doctype") {
		t.Fatal("API fell through to shell", response.Code, response.Body.String())
	}
	for _, header := range []string{"Content-Security-Policy", "Referrer-Policy", "X-Content-Type-Options", "X-Frame-Options", "Cross-Origin-Opener-Policy", "Cache-Control"} {
		if response.Header().Get(header) == "" {
			t.Fatal("missing security header", header)
		}
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("CORS enabled")
	}
}

func TestBrowserMalformedPathsBodiesAndDuplicateCookiesFailClosed(t *testing.T) {
	handler := handlerFixture(t)
	for _, target := range []string{"/app//workboards", "/app/%2e%2e/v1", "/app?token=x", "/outside"} {
		request := browserRequest(http.MethodGet, target, "")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code < 400 {
			t.Fatal("malformed path accepted", target)
		}
	}
	for _, body := range []string{`{}`, `{"version":1,"unknown":true}`, `{"version":1}{}`, `{"version":1,"version":1}`} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, browserRequest(http.MethodPost, "/app/api/v1/session/challenges", body))
		if response.Code != http.StatusBadRequest {
			t.Fatal("invalid bootstrap body accepted", body, response.Code)
		}
	}
	cookie, _ := authenticateBrowser(t, handler)
	request := browserRequest(http.MethodGet, "/app", "")
	request.Header.Set("Cookie", cookie.Name+"="+cookie.Value+"; "+cookie.Name+"="+cookie.Value)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code == http.StatusOK || response.Header().Get("Location") != "/app/bootstrap" {
		t.Fatal("ambiguous cookie accepted", response.Code)
	}
}

func TestBrowserInflightLimitFailsClosed(t *testing.T) {
	handler := handlerFixture(t)
	cookie, _ := authenticateBrowser(t, handler)
	entered := make(chan struct{}, maxBrowserInFlight)
	release := make(chan struct{})
	handler.shell = http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		writer.WriteHeader(http.StatusOK)
	})
	done := make(chan struct{}, maxBrowserInFlight)
	for range maxBrowserInFlight {
		go func() {
			request := browserRequest(http.MethodGet, "/app/chats", "")
			request.AddCookie(cookie)
			handler.ServeHTTP(httptest.NewRecorder(), request)
			done <- struct{}{}
		}()
	}
	for range maxBrowserInFlight {
		<-entered
	}
	request := browserRequest(http.MethodGet, "/app/chats", "")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") == "" {
		t.Fatal("browser concurrency limit bypassed", response.Code)
	}
	close(release)
	for range maxBrowserInFlight {
		<-done
	}
}

func sessionCookieName() string { return sessionCookie }
