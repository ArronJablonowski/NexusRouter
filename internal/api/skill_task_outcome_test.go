package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func taskSkillOutcomeFixture() skills.TaskOutcome {
	return skills.TaskOutcome{Version: 1, TaskID: "task", SessionID: "session", State: "completed", Privacy: "local_only", Sequence: 5, AttemptID: "attempt", Key: &routing.Key{Model: "model", Provider: "provider", Domain: "creative", Profile: "default"}, SkillContext: &runtime.SkillContextUse{Version: 1, Complete: true, References: []runtime.SkillReference{{Scope: "project", Name: "lookup", Version: strings.Repeat("a", 32), Digest: strings.Repeat("b", 64)}}}, OutputChecks: []skills.TaskOutputCheck{{EventID: "mechanical", Code: "deterministic.nonempty_text.v1", Passed: true}}}
}

func taskSkillOutcomeRequest() *http.Request {
	r := request("GET", "/v1/tasks/task/skill-outcome", "")
	r.Body = http.NoBody
	return r
}

func TestSkillTaskOutcomeHTTPProjection(t *testing.T) {
	want := taskSkillOutcomeFixture()
	calls := 0
	s := services()
	s.SkillTaskOutcome = func(ctx context.Context, task string) (skills.TaskOutcome, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if task != "task" || !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 5*time.Second {
			t.Error("wrong or unbounded request")
		}
		return want, nil
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, taskSkillOutcomeRequest())
	var got skills.TaskOutcome
	if w.Code != 200 || calls != 1 || w.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(w.Code, w.Body.String(), calls)
	}
	if got.Quality != nil || strings.Contains(w.Body.String(), `"quality"`) {
		t.Fatal("nonempty output was promoted to quality")
	}
}

func TestSkillTaskOutcomeHTTPAdmission(t *testing.T) {
	for _, mode := range []string{"auth", "origin", "method", "query", "bare-query", "body", "hidden-body", "transfer", "unknown-length", "invalid-id", "empty-id", "missing-hook", "capacity", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			s := services()
			s.SkillTaskOutcome = func(context.Context, string) (skills.TaskOutcome, error) {
				calls++
				return taskSkillOutcomeFixture(), nil
			}
			if mode == "missing-hook" {
				s.SkillTaskOutcome = nil
			}
			h, _ := New(token, 1, s)
			r := taskSkillOutcomeRequest()
			want := 400
			switch mode {
			case "auth":
				r.Header.Del("Authorization")
				want = 401
			case "origin":
				r.Header.Set("Origin", "https://example.com")
				want = 403
			case "method":
				r.Method = "POST"
				want = 405
			case "query":
				r.URL.RawQuery = "scope=other"
			case "bare-query":
				r.URL.ForceQuery = true
			case "body":
				r.Body = io.NopCloser(strings.NewReader("private"))
				r.ContentLength = 7
			case "hidden-body":
				r.Body = io.NopCloser(strings.NewReader("private"))
				r.ContentLength = 0
			case "transfer":
				r.TransferEncoding = []string{"chunked"}
			case "unknown-length":
				r.ContentLength = -1
			case "invalid-id":
				r.URL.Path = "/v1/tasks/a/b/skill-outcome"
			case "empty-id":
				r.URL.Path = "/v1/tasks//skill-outcome"
			case "missing-hook":
				want = 503
			case "capacity":
				h.controls <- struct{}{}
				h.controls <- struct{}{}
				want = 503
			case "canceled":
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
				want = 503
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want || calls != 0 {
				t.Fatal(w.Code, w.Body.String(), calls)
			}
			if mode == "method" && w.Header().Get("Allow") != http.MethodGet {
				t.Fatal("missing method guidance")
			}
			if mode == "capacity" && w.Header().Get("Retry-After") != "1" {
				t.Fatal("missing retry guidance")
			}
		})
	}
}

func TestSkillTaskOutcomeHTTPBackendBoundary(t *testing.T) {
	for _, mode := range []string{"panic", "error", "foreign-task", "schema", "invalid-reference", "nil-checks", "canceled", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 20*time.Millisecond)
				defer stop()
			}
			s := services()
			s.SkillTaskOutcome = func(c context.Context, _ string) (skills.TaskOutcome, error) {
				out := taskSkillOutcomeFixture()
				switch mode {
				case "panic":
					panic("PRIVATE_MODEL_SKILL_BODY")
				case "error":
					return out, errors.New("PRIVATE_MODEL_SKILL_BODY")
				case "foreign-task":
					out.TaskID = "foreign"
				case "schema":
					out.Version = 2
				case "invalid-reference":
					out.SkillContext.References[0].Version = "PRIVATE_MODEL_SKILL_BODY"
				case "nil-checks":
					out.OutputChecks = nil
				case "canceled":
					cancel()
				case "deadline":
					<-c.Done()
				}
				return out, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, taskSkillOutcomeRequest().WithContext(ctx))
			want := 500
			if mode == "panic" || mode == "error" || mode == "canceled" || mode == "deadline" {
				want = 503
			}
			if w.Code != want || strings.Contains(w.Body.String(), "PRIVATE") || strings.Contains(w.Body.String(), "references") || len(h.controls) != 0 {
				t.Fatal(w.Code, w.Body.String(), len(h.controls))
			}
		})
	}
}
