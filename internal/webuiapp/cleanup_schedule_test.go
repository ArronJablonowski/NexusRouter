package webuiapp

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/internal/maintenance"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCleanupScheduleAuthorityValidationAndConflict(t *testing.T) {
	calls := 0
	handler := mutationHandlerFixture(t, MutationServices{CleanupSchedule: func(ctx context.Context, input *maintenance.ScheduleUpdate) (maintenance.ScheduleReport, error) {
		calls++
		if input != nil {
			return maintenance.ScheduleReport{}, maintenance.ErrScheduleChanged
		}
		return maintenance.ScheduleReport{Version: 1}, nil
	}})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, browserRequest(http.MethodGet, "/app/api/v1/cleanup-schedule", ""))
	if w.Code != 401 || calls != 0 {
		t.Fatal(w.Code, calls)
	}
	body := `{"version":1,"expected_digest":"` + strings.Repeat("a", 64) + `","settings":{"enabled":false,"hour":3,"minute":30,"weekday":null}}`
	for _, header := range []string{"Origin", "X-Darwin-CSRF"} {
		r := authorizedMutationRequest(t, handler, "/app/api/v1/cleanup-schedule", body)
		r.Header.Del(header)
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 403 || calls != 0 {
			t.Fatal(header, w.Code, calls)
		}
	}
	for _, bad := range []string{strings.Replace(body, `"hour":3`, `"hour":24`, 1), strings.Replace(body, `"weekday":null`, `"weekday":7`, 1), strings.Replace(body, `"enabled":false,`, ``, 1), strings.Replace(body, `"minute":30`, `"minute":null`, 1), strings.Replace(body, `"enabled":false`, `"enabled":null`, 1), strings.Replace(body, `"settings":{`, `"path":"/tmp","settings":{`, 1)} {
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, authorizedMutationRequest(t, handler, "/app/api/v1/cleanup-schedule", bad))
		if w.Code != 400 || calls != 0 {
			t.Fatal(bad, w.Code, calls)
		}
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, authorizedMutationRequest(t, handler, "/app/api/v1/cleanup-schedule", body))
	if w.Code != 409 || calls != 1 {
		t.Fatal(w.Code, calls)
	}
	r := authorizedMutationRequest(t, handler, "/app/api/v1/cleanup-schedule", `{"version":1}`)
	r.Method = http.MethodGet
	r.Body = http.NoBody
	r.ContentLength = 0
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 || calls != 2 {
		t.Fatal(w.Code, calls)
	}
	r.URL.RawQuery = "path=/tmp"
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 400 || calls != 2 {
		t.Fatal(w.Code, calls)
	}
}

func TestCleanupScheduleSaveSurvivesDisconnectWithDeadline(t *testing.T) {
	called := false
	handler := mutationHandlerFixture(t, MutationServices{CleanupSchedule: func(ctx context.Context, input *maintenance.ScheduleUpdate) (maintenance.ScheduleReport, error) {
		called = true
		if ctx.Err() != nil {
			t.Fatal("browser disconnect canceled schedule transaction")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded update")
		}
		return maintenance.ScheduleReport{Version: 1}, nil
	}})
	r := authorizedMutationRequest(t, handler, "/app/api/v1/cleanup-schedule", `{"version":1,"expected_digest":"`+strings.Repeat("a", 64)+`","settings":{"enabled":true,"hour":3,"minute":30}}`)
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 || !called {
		t.Fatal(w.Code, called)
	}
}
