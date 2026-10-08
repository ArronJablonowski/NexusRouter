package webuiapp

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatPreferencesRequireAuthorityAndFenceStaleEdits(t *testing.T) {
	calls := 0
	h := mutationHandlerFixture(t, MutationServices{ChatPreference: func(_ context.Context, r sessions.ChatPreferenceUpdate) (sessions.ChatPreference, error) {
		calls++
		if r.ExpectedRevision != 1 {
			return sessions.ChatPreference{}, config.ErrConfigConflict
		}
		return sessions.ChatPreference{Title: *r.Title, Revision: 2}, nil
	}})
	body := `{"version":1,"chat_id":"chat","expected_revision":1,"title":"New name"}`
	w := httptest.NewRecorder()
	h.ServeHTTP(w, browserRequest(http.MethodPost, "/app/api/v1/chat-preferences", body))
	if w.Code != 401 || calls != 0 {
		t.Fatal(w.Code, calls)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, authorizedMutationRequest(t, h, "/app/api/v1/chat-preferences", body))
	if w.Code != 200 || calls != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, body := range []string{`{"version":1,"chat_id":"chat","expected_revision":1,"title":""}`, `{"version":1,"chat_id":"chat","expected_revision":1,"title":"name","pinned":true}`} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, authorizedMutationRequest(t, h, "/app/api/v1/chat-preferences", body))
		if w.Code != 400 || calls != 1 {
			t.Fatal(w.Code, calls)
		}
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, authorizedMutationRequest(t, h, "/app/api/v1/chat-preferences", `{"version":1,"chat_id":"chat","expected_revision":0,"title":"Name"}`))
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
}

func TestChatPreferenceReadRequiresSessionAndWriteRequiresCSRF(t *testing.T) {
	calls := 0
	h := mutationHandlerFixture(t, MutationServices{ChatPreference: func(context.Context, sessions.ChatPreferenceUpdate) (sessions.ChatPreference, error) {
		calls++
		return sessions.ChatPreference{}, nil
	}})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, browserRequest(http.MethodGet, "/app/api/v1/chat-preferences/chat", ""))
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	request := authorizedMutationRequest(t, h, "/app/api/v1/chat-preferences", `{"version":1,"chat_id":"chat","expected_revision":0,"pinned":true}`)
	request.Header.Del("X-Darwin-CSRF")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request)
	if w.Code != 403 || calls != 0 {
		t.Fatal(w.Code, calls)
	}
}

func TestChatPreferenceMutationCapacityIsBounded(t *testing.T) {
	calls := 0
	h := mutationHandlerFixture(t, MutationServices{ChatPreference: func(context.Context, sessions.ChatPreferenceUpdate) (sessions.ChatPreference, error) {
		calls++
		return sessions.ChatPreference{}, nil
	}})
	for i := 0; i < cap(h.mutationSlots); i++ {
		h.mutationSlots <- struct{}{}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, authorizedMutationRequest(t, h, "/app/api/v1/chat-preferences", `{"version":1,"chat_id":"chat","expected_revision":0,"pinned":true}`))
	if w.Code != 503 || calls != 0 {
		t.Fatal(w.Code, calls)
	}
}
