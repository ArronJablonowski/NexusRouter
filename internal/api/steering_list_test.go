package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"darwinrouter/runtime"
)

func TestSteeringListMetadataAndValidation(t *testing.T) {
	for _, mode := range []string{"valid", "empty", "duplicate", "wrongtask", "invalid", "too_many", "missing", "error"} {
		t.Run(mode, func(t *testing.T) {
			s := services()
			s.SteeringList = func(context.Context, string) ([]runtime.SteeringMessage, error) {
				m := steeringStatusFixture()
				switch mode {
				case "empty":
					return nil, nil
				case "duplicate":
					return []runtime.SteeringMessage{m, m}, nil
				case "wrongtask":
					m.TaskID = "other"
				case "invalid":
					m.State = "unknown"
				case "too_many":
					return make([]runtime.SteeringMessage, 33), nil
				case "missing":
					return nil, sql.ErrNoRows
				case "error":
					return nil, errors.New("private database error")
				}
				return []runtime.SteeringMessage{m}, nil
			}
			h, _ := New(token, 1, s)
			h.slots <- struct{}{}
			r := httptest.NewRequest("GET", "/v1/tasks/task/steering", nil)
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := 500
			if mode == "valid" || mode == "empty" {
				want = 200
			}
			if mode == "missing" {
				want = 404
			}
			if w.Code != want || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), `"text"`) {
				t.Fatal(w.Code, w.Body.String())
			}
			if mode == "empty" && !strings.Contains(w.Body.String(), `"messages":[]`) {
				t.Fatal(w.Body.String())
			}
		})
	}
}

func TestSteeringListRejectsBodyAndQuery(t *testing.T) {
	for _, path := range []string{"/v1/tasks/task/steering", "/v1/tasks/task/steering?state=pending"} {
		s := services()
		s.SteeringList = func(context.Context, string) ([]runtime.SteeringMessage, error) {
			t.Fatal("invalidrequest reachedstore")
			return nil, nil
		}
		h, _ := New(token, 1, s)
		r := httptest.NewRequest(http.MethodGet, path, strings.NewReader("{}"))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
}
