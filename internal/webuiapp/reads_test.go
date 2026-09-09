package webuiapp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
	contract "github.com/ArronJablonowski/DarwinRouter/webui"
)

func browserGET(target string, cookie *http.Cookie) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7788"+target, nil)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	return request
}

func TestBrowserChatReadsRequireSessionAndEnforceQueryBounds(t *testing.T) {
	handler := handlerFixture(t)
	handler.reads.Chats = func(_ context.Context, options sessions.ChatListOptions) (sessions.ChatPage, error) {
		return sessions.ChatPage{Version: 1, Items: []sessions.ChatSummary{{Version: 1, ChatID: "chat", LatestTaskID: "task", State: "completed", Revision: 2, StartedAt: time.Unix(100, 0).UTC()}}}, nil
	}
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, browserGET("/app/api/v1/chats?limit=1", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatal("unauthenticated chat list accepted", unauthorized.Code)
	}
	cookie, _ := authenticateBrowser(t, handler)
	for _, target := range []string{"/app/api/v1/chats?limit=1", "/app/api/v1/chats?limit=101", "/app/api/v1/chats?unknown=x", "/app/api/v1/chats?limit=01", "/app/api/v1/chats?limit=1&limit=1", "/app/api/v1/chats?"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, browserGET(target, cookie))
		if (target == "/app/api/v1/chats?limit=1") != (response.Code == http.StatusOK) {
			t.Fatal("unexpected bounded query outcome", target, response.Code, response.Body.String())
		}
	}
}

func TestBrowserHistoryUsesAllowlistedContractAndStrictGET(t *testing.T) {
	handler := handlerFixture(t)
	handler.reads.History = func(_ context.Context, chat string, options contract.HistoryOptions) (contract.HistoryPage, error) {
		return contract.HistoryPage{Version: 1, ChatID: chat, TaskID: "task", HeadRevision: 2, Messages: []contract.HistoryMessage{{ID: "msg_1", Role: "user", Text: "hello", Revision: 1, SourceRevision: 1}}}, nil
	}
	cookie, _ := authenticateBrowser(t, handler)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, browserGET("/app/api/v1/chats/chat/messages?limit=1", cookie))
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "tool_calls") || strings.Contains(response.Body.String(), "system") {
		t.Fatal("safe authenticated history unavailable", response.Code, response.Body.String())
	}
	for name, mutate := range map[string]func(*http.Request){
		"force query": func(r *http.Request) { r.URL.ForceQuery = true },
		"zero body":   func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("")) },
		"transfer":    func(r *http.Request) { r.TransferEncoding = []string{"chunked"} },
		"method":      func(r *http.Request) { r.Method = http.MethodHead },
	} {
		t.Run(name, func(t *testing.T) {
			request := browserGET("/app/api/v1/chats/chat/messages", cookie)
			mutate(request)
			denied := httptest.NewRecorder()
			handler.ServeHTTP(denied, request)
			if denied.Code < 400 {
				t.Fatal("non-canonical GET accepted", denied.Code)
			}
		})
	}
	for _, target := range []string{"/app/api/v1/chats/chat.bad/messages", "/app/api/v1/chats/chat/messages?after=a&after=b", "/app/api/v1/chats/chat/messages?limit=101"} {
		denied := httptest.NewRecorder()
		handler.ServeHTTP(denied, browserGET(target, cookie))
		if denied.Code < 400 {
			t.Fatal("invalid route/query accepted", target)
		}
	}
}
