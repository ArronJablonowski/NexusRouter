package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNativeBrowserChallengeApprovalRemainsBearerOnly(t *testing.T) {
	id := strings.Repeat("a", 24)
	called := 0
	s := services()
	s.ApproveBrowserChallenge = func(_ context.Context, gotID, code string) error {
		called++
		if gotID != id || code != "12345678" {
			t.Fatal("approval input drift", gotID, code)
		}
		return nil
	}
	handler, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/browser-session/challenges/" + id + "/approve"
	for name, test := range map[string]struct {
		mutate func(*http.Request)
		want   int
	}{
		"valid":          {func(*http.Request) {}, http.StatusNoContent},
		"no bearer":      {func(r *http.Request) { r.Header.Del("Authorization") }, http.StatusUnauthorized},
		"browser origin": {func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:7788") }, http.StatusForbidden},
		"duplicate": {func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(`{"version":1,"version":1,"display_code":"12345678"}`))
		}, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			request := request(http.MethodPost, path, `{"version":1,"display_code":"12345678"}`)
			test.mutate(request)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatal(response.Code, response.Body.String())
			}
		})
	}
	if called != 1 {
		t.Fatal("denied request dispatched", called)
	}
}
