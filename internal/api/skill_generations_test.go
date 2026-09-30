package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestSkillGenerationHTTPAdmission(t *testing.T) {
	for _, path := range []string{
		"/v1/skills/generations", "/v1/skills/generations?scope=p&scope=p", "/v1/skills/generations?scope=p&unknown=x",
		"/v1/skills/generations?scope=p&limit=0", "/v1/skills/generations?scope=p&limit=101", "/v1/skills/generations?scope=p&limit=2&limit=2",
		"/v1/skills/generations?scope=p&after=bad%2Fid", "/v1/skills/generations?scope=p&after=x&after=y", "/v1/skills/generations?scope=p&bad=%ZZ",
		"/v1/skills/generations/a?scope=p&limit=1", "/v1/skills/generations/a/b?scope=p", "/v1/skills/generations/?scope=p",
		"/v1/skills/generations?scope=" + strings.Repeat("a", 65),
		"/v1/skills/generations?scope=project&after=" + strings.Repeat("a", 1024),
	} {
		t.Run(path, func(t *testing.T) {
			s := services()
			calls := 0
			s.SkillGenerations = func(context.Context, string, string, int) ([]skills.GenerationSummary, error) {
				calls++
				return nil, nil
			}
			s.SkillGeneration = func(context.Context, string, string) (skills.GenerationAttempt, error) {
				calls++
				return skills.GenerationAttempt{}, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("GET", path, ""))
			if w.Code != 400 || calls != 0 {
				t.Fatal(w.Code, w.Body.String(), calls)
			}
		})
	}
	for _, mode := range []string{"auth", "origin", "capacity", "missing-service", "error", "post"} {
		t.Run(mode, func(t *testing.T) {
			s := services()
			calls := 0
			s.SkillGenerations = func(ctx context.Context, scope, after string, limit int) ([]skills.GenerationSummary, error) {
				calls++
				if _, ok := ctx.Deadline(); !ok {
					t.Error("missing deadline")
				}
				return nil, errors.New("private-database-path")
			}
			if mode == "missing-service" {
				s.SkillGenerations = nil
			}
			h, _ := New(token, 1, s)
			r := request("GET", "/v1/skills/generations?scope=p", "")
			want := 404
			switch mode {
			case "auth":
				r.Header.Del("Authorization")
				want = 401
			case "origin":
				r.Header.Set("Origin", "https://example.com")
				want = 403
			case "capacity":
				h.slots <- struct{}{}
				want = 503
			case "missing-service":
				want = 503
			case "post":
				r.Method = "POST"
				r.URL.RawQuery = ""
				want = 503 // Mutation adapter is deliberately absent in this fixture.
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want || strings.Contains(w.Body.String(), "private-database-path") {
				t.Fatal(w.Code, w.Body.String())
			}
			if mode != "error" && calls != 0 {
				t.Fatal("unexpected callback")
			}
		})
	}
}

func TestSkillGenerationHTTPCanceledCallbackResultIsDiscarded(t *testing.T) {
	for _, list := range []bool{true, false} {
		ctx, cancel := context.WithCancel(context.Background())
		item := skills.GenerationAttempt{Version: 1, ID: "attempt", Key: skills.Key{Scope: "project", Name: "workflow"}, Model: "model", Provider: "local", InputDigest: strings.Repeat("a", 64), SourceSessions: []string{"session-a", "session-b"}, SourceEvidence: []string{"evidence"}, Status: "started", StartedAt: time.Now().UTC()}
		s := services()
		calls := 0
		s.SkillGeneration = func(context.Context, string, string) (skills.GenerationAttempt, error) {
			calls++
			cancel()
			return item, nil
		}
		s.SkillGenerations = func(context.Context, string, string, int) ([]skills.GenerationSummary, error) {
			calls++
			cancel()
			return []skills.GenerationSummary{item.Summary()}, nil
		}
		path := "/v1/skills/generations/attempt?scope=project"
		if list {
			path = "/v1/skills/generations?scope=project"
		}
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("GET", path, "").WithContext(ctx))
		cancel()
		if calls != 1 || w.Code != 404 || strings.Contains(w.Body.String(), "InputDigest") {
			t.Fatal("expired callback result escaped", calls, w.Code, w.Body.String())
		}
	}
}

func TestSkillGenerationHTTPReadOnlyStore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "generation.db")
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	a := skills.GenerationAttempt{Version: 1, ID: "attempt", Key: skills.Key{Scope: "project", Name: "workflow"}, Model: "model", Provider: "local", InputDigest: strings.Repeat("a", 64), SourceSessions: []string{"session-a", "session-b"}, SourceEvidence: []string{"evidence"}, Status: "started", StartedAt: time.Now().UTC()}
	if err := db.BeginSkillGeneration(ctx, a); err != nil {
		t.Fatal(err)
	}
	a.Status = "drafted"
	a.FinishedAt = a.StartedAt.Add(time.Second)
	a.Result = &skills.ModelDraftResult{Model: a.Model, Draft: skills.Draft{Key: a.Key, Description: "sensitive-workflow-text", SourceSessions: a.SourceSessions, SourceEvidence: a.SourceEvidence, Steps: []string{"sensitive-workflow-step"}, ValidationCases: []string{"case"}}}
	if err := db.FinishSkillGeneration(ctx, a); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := services()
	s.SkillGenerations = func(ctx context.Context, scope, after string, limit int) ([]skills.GenerationSummary, error) {
		return app.ListSkillGenerations(ctx, path, scope, after, limit)
	}
	s.SkillGeneration = func(ctx context.Context, scope, id string) (skills.GenerationAttempt, error) {
		return app.InspectSkillGeneration(ctx, path, scope, id)
	}
	h, _ := New(token, 1, s)
	for _, test := range []struct {
		path   string
		status int
		full   bool
	}{
		{"/v1/skills/generations?scope=project&limit=1", 200, false},
		{"/v1/skills/generations?scope=project&after=attempt", 200, false},
		{"/v1/skills/generations/attempt?scope=project", 200, true},
		{"/v1/skills/generations/attempt?scope=other", 404, false},
		{"/v1/skills/generations/missing?scope=project", 404, false},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("GET", test.path, ""))
		if w.Code != test.status {
			t.Fatal(test.path, w.Code, w.Body.String())
		}
		if test.full {
			var got skills.GenerationAttempt
			if json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, a) {
				t.Fatal("detail mismatch")
			}
		} else if strings.Contains(w.Body.String(), "sensitive-workflow") || strings.Contains(w.Body.String(), "session-a") {
			t.Fatal("list leaked workflow")
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cache enabled")
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("inspection mutated database", err)
	}
	path = filepath.Join(t.TempDir(), "missing.db")
	for _, route := range []string{"/v1/skills/generations?scope=project", "/v1/skills/generations/attempt?scope=project"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("GET", route, ""))
		if w.Code != 404 {
			t.Fatal(w.Code)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("inspection created database", err)
	}
}

func TestSkillGenerationHTTPRejectsUnboundCallbackPayloads(t *testing.T) {
	base := skills.GenerationAttempt{Version: 1, ID: "attempt", Key: skills.Key{Scope: "project", Name: "workflow"}, Model: "model", Provider: "local", InputDigest: strings.Repeat("a", 64), SourceSessions: []string{"session-a", "session-b"}, SourceEvidence: []string{"evidence"}, Status: "started", StartedAt: time.Now().UTC()}
	for _, mode := range []string{"nil-list", "wrong-scope", "duplicate", "over-limit", "invalid-summary", "wrong-detail-id", "wrong-detail-scope", "invalid-detail"} {
		t.Run(mode, func(t *testing.T) {
			s := services()
			s.SkillGenerations = func(context.Context, string, string, int) ([]skills.GenerationSummary, error) {
				item := base.Summary()
				switch mode {
				case "nil-list":
					return nil, nil
				case "wrong-scope":
					item.Key.Scope = "other"
				case "duplicate", "over-limit":
					return []skills.GenerationSummary{item, item}, nil
				case "invalid-summary":
					item.Version = 99
				}
				return []skills.GenerationSummary{item}, nil
			}
			s.SkillGeneration = func(context.Context, string, string) (skills.GenerationAttempt, error) {
				item := base
				switch mode {
				case "wrong-detail-id":
					item.ID = "other"
				case "wrong-detail-scope":
					item.Key.Scope = "other"
				case "invalid-detail":
					item.Version = 99
				}
				return item, nil
			}
			path := "/v1/skills/generations?scope=project"
			if mode == "over-limit" {
				path += "&limit=1"
			}
			if strings.Contains(mode, "detail") {
				path = "/v1/skills/generations/attempt?scope=project"
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("GET", path, ""))
			if w.Code != 404 || strings.Contains(w.Body.String(), "InputDigest") {
				t.Fatal("invalid result escaped", w.Code, w.Body.String())
			}
		})
	}
}
