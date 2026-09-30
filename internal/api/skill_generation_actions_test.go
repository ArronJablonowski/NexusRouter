package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

const skillGenerationActionBody = `{"version":1,"id":"attempt","model_id":"model","name":"workflow","task_ids":["task-a","task-b"]}`

func skillGenerationActionFixture() skills.GenerationAttempt {
	now := time.Now().UTC()
	a := skills.GenerationAttempt{Version: 1, ID: "attempt", Key: skills.Key{Scope: "project", Name: "workflow"}, Model: "model", Provider: "local", InputDigest: strings.Repeat("a", 64), SourceSessions: []string{"session-a", "session-b"}, SourceEvidence: []string{"evidence"}, Status: "drafted", StartedAt: now, FinishedAt: now.Add(time.Second)}
	a.Result = &skills.ModelDraftResult{Model: a.Model, Draft: skills.Draft{Key: a.Key, Description: "A workflow", SourceSessions: a.SourceSessions, SourceEvidence: a.SourceEvidence, Steps: []string{"Run checks"}, ValidationCases: []string{"Fixture"}}}
	return a
}

func TestSkillGenerationActionHTTPSuccessAndCallbackFailures(t *testing.T) {
	for _, publish := range []bool{false, true} {
		for _, mode := range []string{"success", "error", "panic", "cancel", "invalid", "wrong-binding"} {
			t.Run(map[bool]string{false: "generate", true: "publish"}[publish]+"-"+mode, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				s := services()
				calls := 0
				check := func(ctx context.Context) {
					calls++
					deadline, ok := ctx.Deadline()
					bound := 30 * time.Second
					if publish {
						bound = 10 * time.Second
					}
					if !ok || time.Until(deadline) > bound {
						t.Error("unbounded action")
					}
					if mode == "panic" {
						panic("private-provider-error")
					}
					if mode == "cancel" {
						cancel()
					}
				}
				s.GenerateSkillDraft = func(ctx context.Context, id, model, name string, tasks []string, cost float64) (skills.GenerationAttempt, error) {
					check(ctx)
					if id != "attempt" || model != "model" || name != "workflow" || strings.Join(tasks, ",") != "task-a,task-b" || cost != 0 {
						t.Error("incorrect callback arguments")
					}
					a := skillGenerationActionFixture()
					if mode == "error" {
						return a, errors.New("private-provider-error")
					}
					if mode == "invalid" {
						a.Version = 99
					}
					if mode == "wrong-binding" {
						a.ID = "other"
					}
					return a, nil
				}
				s.PublishSkillGeneration = func(ctx context.Context, id string) (skills.Version, error) {
					check(ctx)
					if id != "attempt" {
						t.Error(id)
					}
					v := skills.Version{ID: strings.Repeat("a", 32), CreatedAt: time.Now().UTC(), Draft: skillGenerationActionFixture().Result.Draft}
					if mode == "error" {
						return v, errors.New("private-provider-error")
					}
					if mode == "invalid" || mode == "wrong-binding" {
						v.ID = "invalid"
					}
					return v, nil
				}
				path, body := "/v1/skills/generations", skillGenerationActionBody
				if publish {
					path += "/attempt/publish"
					body = `{"version":1}`
				}
				h, _ := New(token, 1, s)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, request("POST", path, body).WithContext(ctx))
				want := 200
				switch mode {
				case "error", "cancel", "panic":
					want = 422
				case "invalid", "wrong-binding":
					want = 500
				}
				if w.Code != want || calls != 1 || strings.Contains(w.Body.String(), "private-provider-error") {
					t.Fatal(w.Code, w.Body.String(), calls)
				}
				if mode != "success" && strings.Contains(w.Body.String(), "Run checks") {
					t.Fatal("failed action exposed result")
				}
				if len(h.slots) != 0 || w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("capacity leak or cache enabled")
				}
			})
		}
	}
}

func TestSkillGenerationActionHTTPRejectsBodyAndAdmission(t *testing.T) {
	for _, publish := range []bool{false, true} {
		path, valid := "/v1/skills/generations", skillGenerationActionBody
		if publish {
			path += "/attempt/publish"
			valid = `{"version":1}`
		}
		cases := []struct {
			body   string
			status int
		}{
			{`{}`, 400}, {`null`, 400}, {`[]`, 400}, {`{"version":null}`, 400}, {`{"version":2}`, 400}, {`{"version":1,"version":1}`, 400}, {`{"Version":1}`, 400}, {valid + `{}`, 400}, {strings.Replace(valid, `"version":1`, `"version":1,"unknown":"private-secret"`, 1), 400}, {strings.Repeat(" ", 8193), 413}, {string([]byte{'{', 0xff, '}'}), 400},
		}
		if !publish {
			for _, replacement := range []string{`null`, `[]`, `["task-a"]`, `["task-a","task-a"]`, `["task-a",null]`, `["task-a",2]`, `["task-a","../bad"]`} {
				cases = append(cases, struct {
					body   string
					status int
				}{strings.Replace(valid, `["task-a","task-b"]`, replacement, 1), 400})
			}
			for _, field := range []string{`"max_cost":-1`, `"max_cost":null`, `"max_cost":"0"`, `"max_cost":1e999`, `"scope":"project"`, `"id":"other"`, `"task_ids":["x","y"]`} {
				cases = append(cases, struct {
					body   string
					status int
				}{strings.TrimSuffix(valid, "}") + "," + field + "}", 400})
			}
			cases = append(cases, struct {
				body   string
				status int
			}{strings.Replace(valid, `"name":"workflow"`, `"name":"`+strings.Repeat("a", 65)+`"`, 1), 400})
		}
		for _, test := range cases {
			s := services()
			calls := 0
			s.GenerateSkillDraft = func(context.Context, string, string, string, []string, float64) (skills.GenerationAttempt, error) {
				calls++
				return skills.GenerationAttempt{}, nil
			}
			s.PublishSkillGeneration = func(context.Context, string) (skills.Version, error) { calls++; return skills.Version{}, nil }
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("POST", path, test.body))
			if w.Code != test.status || calls != 0 || strings.Contains(w.Body.String(), "private-secret") {
				t.Fatal("unsafe body admission", test.body, w.Code, w.Body.String(), calls)
			}
		}
		for _, mode := range []string{"auth", "origin", "query", "bare-query", "media", "capacity", "missing-service"} {
			s := services()
			calls := 0
			s.GenerateSkillDraft = func(context.Context, string, string, string, []string, float64) (skills.GenerationAttempt, error) {
				calls++
				return skills.GenerationAttempt{}, nil
			}
			s.PublishSkillGeneration = func(context.Context, string) (skills.Version, error) { calls++; return skills.Version{}, nil }
			if mode == "missing-service" {
				s.GenerateSkillDraft = nil
				s.PublishSkillGeneration = nil
			}
			h, _ := New(token, 1, s)
			r := request("POST", path, valid)
			want := 400
			switch mode {
			case "auth":
				r.Header.Del("Authorization")
				want = 401
			case "origin":
				r.Header.Set("Origin", "https://example.com")
				want = 403
			case "query":
				r.URL.RawQuery = "scope=project"
			case "bare-query":
				r.URL.ForceQuery = true
			case "media":
				r.Header.Set("Content-Type", "text/plain")
				want = 415
			case "capacity":
				h.slots <- struct{}{}
				want = 503
			case "missing-service":
				want = 503
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want || calls != 0 {
				t.Fatal(mode, w.Code, w.Body.String(), calls)
			}
		}
	}
}
