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
	if err != nil || digest != "d376d62bd6b7f770a37467634d0909e67434df4345c9c3b15fc2e796d089825e" || ShellAssetVersion != "v1" {
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

func TestEmbeddedChatPresentationIsBoundedReadOnlyAndXSSSafe(t *testing.T) {
	script, err := embeddedShellAssets.ReadFile("assets/v1/app.js")
	if err != nil {
		t.Fatal(err)
	}
	body := string(script)
	for _, forbidden := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "localStorage", "sessionStorage"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("unsafe browser rendering primitive %q is present", forbidden)
		}
	}
	for _, forbidden := range []string{"message.content", "tool_calls", "tool_call_id", `role !== "system"`, `role !== "tool"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("browser consumed non-presentation transcript field %q", forbidden)
		}
	}
	for _, required := range []string{
		"document.createElement", "node.textContent = value", "const pageLimit = 25", "const maxChats = 100",
		"const historyPageLimit = 100", "const maxMessages = 500", "new URLSearchParams", "page.items.length > pageLimit",
		"new EventSource(base +", "source.onerror", "clearAllProvisional()", `payload.durability === "provisional"`,
		`payload.durability !== "committed"`, "payload.revision <= eventRevision", "payload.subject !== selectedChat", "encodeURIComponent(chatID)",
		"source.close()", "source = null", "body.next_cursor", "messageIDs", "lastMessageRevision", `loadHistory(selectedChat, "", true, false)`,
		"body.messages.length > remaining", "body.head_revision !== historyHead", "message.revision > afterRevision",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("chat client lost bounded presentation/reconnect guard %q", required)
		}
	}
	index, err := embeddedShellAssets.ReadFile("assets/v1/index.html")
	if err != nil {
		t.Fatal(err)
	}
	markup := strings.ToLower(string(index))
	for _, forbidden := range []string{"<form", "<textarea", "contenteditable", "send message", "cancel task", "steer"} {
		if strings.Contains(markup, forbidden) {
			t.Fatalf("read-only chat shell exposes action %q", forbidden)
		}
	}
	for _, required := range []string{`id="chat-list-state"`, `id="load-more"`, `id="transcript-state"`, `id="load-more-messages"`, `id="provisional"`} {
		if !strings.Contains(markup, required) {
			t.Fatalf("chat shell lost presentation state %q", required)
		}
	}
}

func TestEmbeddedChatKeepsConcurrentProvisionalTasksIsolated(t *testing.T) {
	script, err := embeddedShellAssets.ReadFile("assets/v1/app.js")
	if err != nil {
		t.Fatal(err)
	}
	body := string(script)
	for _, required := range []string{
		"const maxProvisionalTasks = 16", "const maxProvisionalTaskText = 256 << 10", "const maxProvisionalText = 1 << 20",
		"const provisionalTasks = new Map()", "provisionalTasks.get(taskID)", "provisionalTasks.set(taskID, item)",
		"maxProvisionalTaskText - item.size", "maxProvisionalText - provisionalTextSize", "node.textContent = value",
		"appendProvisional(payload.data.task_id", "clearProvisionalTask(payload.data.task_id)",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("concurrent provisional isolation guard missing %q", required)
		}
	}
	eventStart := strings.Index(body, "function applyPresentationEvent")
	eventEnd := strings.Index(body[eventStart:], "function connect")
	if eventStart < 0 || eventEnd < 0 {
		t.Fatal("event application boundary missing")
	}
	events := body[eventStart : eventStart+eventEnd]
	if strings.Count(events, "clearProvisionalTask(payload.data.task_id)") != 2 || !strings.Contains(events, `payload.data.state === "completed"`) {
		t.Fatal("final and out-of-order task terminals do not clear only their matching provisional buffers")
	}
	historyStart := strings.Index(body, "function applyHistoryPage")
	historyEnd := strings.Index(body[historyStart:], "function setTaskState")
	if historyStart < 0 || historyEnd < 0 {
		t.Fatal("history reconciliation boundary missing")
	}
	history := body[historyStart : historyStart+historyEnd]
	if strings.Contains(history, "clearProvisionalTask") || strings.Contains(history, "clearAllProvisional") || strings.Contains(history, "provisionalTasks.clear") {
		t.Fatal("history reconciliation erases another active task's provisional buffer")
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
