package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"darwinrouter/internal/app"
	"darwinrouter/submissions"
)

func recoveryAPIFixture() submissions.Recovery {
	return submissions.Recovery{Version: 1, ID: "recovery", SubmissionID: "submission", Time: time.Unix(100, 0).UTC(), Action: "queued", Reason: "lease_expired_no_task"}
}

func TestSubmissionRecoveriesReturnsBoundedAuditWithoutMutation(t *testing.T) {
	for _, count := range []int{0, 1, 4} {
		records := make([]submissions.Recovery, count)
		for i := range records {
			records[i] = recoveryAPIFixture()
			records[i].ID += strings.Repeat("x", i)
		}
		s := services()
		calls := 0
		s.SubmissionRecoveries = func(_ context.Context, id string) ([]submissions.Recovery, error) {
			calls++
			if id != "submission" {
				t.Error(id)
			}
			return records, nil
		}
		s.Run = func(context.Context, app.Request) (app.Result, error) {
			t.Error("recovery history executed task")
			return app.Result{}, nil
		}
		s.CancelSubmission = func(context.Context, string) (submissions.Status, error) {
			t.Error("recovery history canceled work")
			return submissions.Status{}, nil
		}
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("GET", "/v1/submissions/submission/recoveries", ""))
		var got []submissions.Recovery
		if w.Code != 200 || calls != 1 || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, records) {
			t.Fatal(w.Code, calls, w.Body.String())
		}
		for _, field := range []string{`"token"`, `"request"`, `"result"`, `"key"`} {
			if strings.Contains(w.Body.String(), field) {
				t.Fatal("private field exposed", field)
			}
		}
	}
}

func TestSubmissionRecoveriesAdmissionCapacityAndNoMutationRoute(t *testing.T) {
	for _, condition := range []string{"auth", "origin", "query", "missing", "control-capacity", "task-capacity", "post"} {
		s := services()
		calls := 0
		s.SubmissionRecoveries = func(context.Context, string) ([]submissions.Recovery, error) { calls++; return nil, nil }
		if condition == "missing" {
			s.SubmissionRecoveries = nil
		}
		h, _ := New(token, 1, s)
		r := request("GET", "/v1/submissions/submission/recoveries", "")
		want := 200
		switch condition {
		case "auth":
			r.Header.Del("Authorization")
			want = 401
		case "origin":
			r.Header.Set("Origin", "https://example.com")
			want = 403
		case "query":
			r.URL.RawQuery = "after=private-cursor"
			want = 400
		case "missing":
			want = 503
		case "control-capacity":
			h.controls <- struct{}{}
			h.controls <- struct{}{}
			want = 503
		case "task-capacity":
			h.slots <- struct{}{}
		case "post":
			r.Method = "POST"
			want = 404
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want || want != 200 && calls != 0 || strings.Contains(w.Body.String(), "private-cursor") {
			t.Fatal(condition, w.Code, calls, w.Body.String())
		}
		if condition == "control-capacity" && w.Header().Get("Retry-After") != "1" {
			t.Fatal("missing capacity backoff")
		}
		if want == 200 && (calls != 1 || strings.TrimSpace(w.Body.String()) != "[]") {
			t.Fatal(condition, calls, w.Body.String())
		}
	}
	for _, id := range []string{"", "id/other", "id:other", "id space", "id\nother", "id\xff", strings.Repeat("x", 129)} {
		s := services()
		s.SubmissionRecoveries = func(context.Context, string) ([]submissions.Recovery, error) {
			t.Error("invalid ID reached history")
			return nil, nil
		}
		h, _ := New(token, 1, s)
		r := request("GET", "/v1/submissions/submission/recoveries", "")
		r.URL.Path = "/v1/submissions/" + id + "/recoveries"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal(id, w.Code, w.Body.String())
		}
	}
}

func TestSubmissionRecoveriesRejectsInvalidAuditAndGenericErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
	}{{sql.ErrNoRows, 404}, {errors.New("private-storage-token"), 500}} {
		s := services()
		s.SubmissionRecoveries = func(context.Context, string) ([]submissions.Recovery, error) { return nil, tc.err }
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("GET", "/v1/submissions/submission/recoveries", ""))
		if w.Code != tc.code || strings.Contains(w.Body.String(), "private-storage-token") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, kind := range []string{"version", "binding", "id", "time", "action", "reason", "duplicate", "oversize"} {
		r := recoveryAPIFixture()
		records := []submissions.Recovery{r}
		switch kind {
		case "version":
			records[0].Version = 2
		case "binding":
			records[0].SubmissionID = "other"
		case "id":
			records[0].ID = "private invalid id"
		case "time":
			records[0].Time = time.Time{}
		case "action":
			records[0].Action = "execute-private"
		case "reason":
			records[0].Reason = "private-token"
		case "duplicate":
			records = append(records, r)
		case "oversize":
			for i := 1; i < 5; i++ {
				copy := r
				copy.ID += strings.Repeat("x", i)
				records = append(records, copy)
			}
		}
		s := services()
		s.SubmissionRecoveries = func(context.Context, string) ([]submissions.Recovery, error) { return records, nil }
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("GET", "/v1/submissions/submission/recoveries", ""))
		if w.Code != 500 || strings.Contains(w.Body.String(), "private") {
			t.Fatal(kind, w.Code, w.Body.String())
		}
	}
}

func TestSubmissionStatusAllowsExhaustedRecovery(t *testing.T) {
	status := submissionFixtureStatus("failed")
	status.ErrorCode = "recovery_exhausted"
	s := services()
	s.Submission = func(context.Context, string) (submissions.Status, error) { return status, nil }
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("GET", "/v1/submissions/submission", ""))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "recovery_exhausted") {
		t.Fatal(w.Code, w.Body.String())
	}
}
