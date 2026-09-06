package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func comparisonAPIFixture(t *testing.T) (skills.ComparisonRequest, skills.ComparisonReport) {
	t.Helper()
	r := skills.ComparisonRequest{Version: 1, ModelID: "configured", Domain: "creative", Profile: "default", Name: "lookup", BaselineVersion: strings.Repeat("a", 32), CandidateVersion: strings.Repeat("b", 32), Source: evaluation.UserFeedback, MinSamples: 20, MinDrop: .1}
	p := skills.ComparisonPolicy{Key: skills.Key{Scope: "project", Name: r.Name}, BaselineVersion: r.BaselineVersion, CandidateVersion: r.CandidateVersion, Execution: routing.Key{Model: "model:tag", Provider: "provider", Domain: r.Domain, Profile: r.Profile}, Source: r.Source, MinSamples: r.MinSamples, MinDrop: r.MinDrop}
	var outcomes []skills.TaskOutcome
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("task-%02d", i)
		r.Tasks = append(r.Tasks, id)
		version, digest := r.BaselineVersion, strings.Repeat("c", 64)
		if i >= 20 {
			version, digest = r.CandidateVersion, strings.Repeat("d", 64)
		}
		outcomes = append(outcomes, skills.TaskOutcome{Version: 1, TaskID: id, SessionID: "session-" + id, State: "completed", Sequence: 4, AttemptID: "attempt", Key: &p.Execution, SkillContext: &runtime.SkillContextUse{Version: 1, Complete: true, References: []runtime.SkillReference{{Scope: p.Key.Scope, Name: r.Name, Version: version, Digest: digest}}}, EvaluationID: "evaluation", EvaluationDigest: strings.Repeat("e", 64), Quality: &evaluation.Outcome{Source: r.Source, Accepted: i < 20, References: []string{"user"}}, OutputChecks: []skills.TaskOutputCheck{}})
	}
	report, err := skills.CompareTaskOutcomes(outcomes, p)
	if err != nil {
		t.Fatal(err)
	}
	report.ConfiguredModelID = r.ModelID
	return r, report
}

func TestSkillComparisonHTTPAdvisoryAggregate(t *testing.T) {
	input, want := comparisonAPIFixture(t)
	body, _ := json.Marshal(input)
	calls := 0
	s := services()
	s.CompareSkillOutcomes = func(ctx context.Context, got skills.ComparisonRequest) (skills.ComparisonReport, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second || time.Until(deadline) <= 0 || !reflect.DeepEqual(got, input) {
			t.Error("wrong/unbounded input")
		}
		return want, nil
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("POST", "/v1/skills/comparison", string(body)))
	var got skills.ComparisonReport
	if w.Code != 200 || calls != 1 || w.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(w.Code, w.Body.String(), calls)
	}
	if !got.AdvisoryOnly || got.Status != "regression_signal" || strings.Contains(w.Body.String(), `"references"`) || strings.Contains(w.Body.String(), "task-00") {
		t.Fatal("unsafe aggregate projection")
	}
}

func TestSkillComparisonHTTPStrictAdmission(t *testing.T) {
	input, _ := comparisonAPIFixture(t)
	base, _ := json.Marshal(input)
	for _, mode := range []string{"auth", "origin", "method", "query", "bare-query", "media", "nil-body", "declared-oversize", "actual-oversize", "transfer", "unknown-length", "empty", "duplicate", "escaped-duplicate", "case", "unknown", "null", "missing", "trailing", "utf8", "surrogate", "duplicate-task", "zero-min", "judge", "missing-hook", "capacity", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			s := services()
			s.CompareSkillOutcomes = func(context.Context, skills.ComparisonRequest) (skills.ComparisonReport, error) {
				calls++
				return skills.ComparisonReport{}, nil
			}
			if mode == "missing-hook" {
				s.CompareSkillOutcomes = nil
			}
			h, _ := New(token, 1, s)
			body := string(base)
			want := 400
			switch mode {
			case "empty":
				body = "{}"
			case "duplicate":
				body = strings.Replace(body, `"version":1`, `"version":1,"version":1`, 1)
			case "escaped-duplicate":
				body = strings.Replace(body, `"version":1`, `"version":1,"\u0076ersion":1`, 1)
			case "case":
				body = strings.Replace(body, `"version"`, `"Version"`, 1)
			case "unknown":
				body = strings.Replace(body, `"version":1`, `"version":1,"scope":"foreign"`, 1)
			case "null":
				body = strings.Replace(body, `"min_samples":20`, `"min_samples":null`, 1)
			case "missing":
				body = strings.Replace(body, `"min_samples":20,`, "", 1)
			case "trailing":
				body += " {}"
			case "utf8":
				body += string([]byte{255})
			case "surrogate":
				body = strings.Replace(body, `"domain":"creative"`, `"domain":"\ud800"`, 1)
			case "duplicate-task":
				body = strings.Replace(body, `"task-01"`, `"task-00"`, 1)
			case "zero-min":
				body = strings.Replace(body, `"min_samples":20`, `"min_samples":0`, 1)
			case "judge":
				body = strings.Replace(body, `"user_feedback"`, `"llm_judge"`, 1)
			case "actual-oversize":
				body = strings.Repeat(" ", 65537)
			}
			r := request("POST", "/v1/skills/comparison", body)
			switch mode {
			case "auth":
				r.Header.Del("Authorization")
				want = 401
			case "origin":
				r.Header.Set("Origin", "https://example.com")
				want = 403
			case "method":
				r.Method = "GET"
				want = 405
			case "query":
				r.URL.RawQuery = "scope=other"
			case "bare-query":
				r.URL.ForceQuery = true
			case "media":
				r.Header.Set("Content-Type", "text/plain")
				want = 415
			case "nil-body":
				r.Body = nil
				want = 413
			case "declared-oversize":
				r.ContentLength = 65537
				want = 413
			case "actual-oversize":
				r.ContentLength = 0
			case "transfer":
				r.TransferEncoding = []string{"chunked"}
			case "unknown-length":
				r.ContentLength = -1
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
				t.Fatal(mode, w.Code, w.Body.String(), calls)
			}
		})
	}
}

func TestSkillComparisonHTTPBackendFailures(t *testing.T) {
	for _, mode := range []string{"error", "panic", "canceled", "deadline", "foreign-model-id", "missing-model-id", "foreign-name", "foreign-domain", "foreign-profile", "foreign-source", "foreign-version", "foreign-threshold", "sampled", "rate", "authority", "unknown-exclusion"} {
		t.Run(mode, func(t *testing.T) {
			input, report := comparisonAPIFixture(t)
			body, _ := json.Marshal(input)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 20*time.Millisecond)
				defer stop()
			}
			s := services()
			s.CompareSkillOutcomes = func(c context.Context, _ skills.ComparisonRequest) (skills.ComparisonReport, error) {
				switch mode {
				case "error":
					return report, errors.New("PRIVATE_SKILL_BODY")
				case "panic":
					panic("PRIVATE_MODEL_OUTPUT")
				case "canceled":
					cancel()
				case "deadline":
					<-c.Done()
				case "foreign-model-id":
					report.ConfiguredModelID = "another-configured-model"
				case "missing-model-id":
					report.ConfiguredModelID = ""
				case "foreign-name":
					report.Policy.Key.Name = "other"
				case "foreign-domain":
					report.Policy.Execution.Domain = "other"
				case "foreign-profile":
					report.Policy.Execution.Profile = "other"
				case "foreign-source":
					report.Policy.Source = evaluation.LLMJudge
				case "foreign-version":
					report.Policy.CandidateVersion = strings.Repeat("f", 32)
					report.Candidate.Version = report.Policy.CandidateVersion
				case "foreign-threshold":
					report.Policy.MinSamples = 21
					report.Status = "insufficient_evidence"
				case "sampled":
					report.Sampled++
				case "rate":
					report.Baseline.Rate = .9
				case "authority":
					report.AdvisoryOnly = false
				case "unknown-exclusion":
					report.Excluded["PRIVATE_MODEL_OUTPUT"] = 1
				}
				return report, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("POST", "/v1/skills/comparison", string(body)).WithContext(ctx))
			want := 500
			if mode == "error" || mode == "panic" || mode == "canceled" || mode == "deadline" {
				want = 503
			}
			if w.Code != want || strings.Contains(w.Body.String(), "PRIVATE") || strings.Contains(w.Body.String(), "baseline") || len(h.controls) != 0 {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

type comparisonBrokenBody struct{}

func (comparisonBrokenBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (comparisonBrokenBody) Close() error             { return nil }
func TestSkillComparisonHTTPBrokenBody(t *testing.T) {
	calls := 0
	s := services()
	s.CompareSkillOutcomes = func(context.Context, skills.ComparisonRequest) (skills.ComparisonReport, error) {
		calls++
		return skills.ComparisonReport{}, nil
	}
	h, _ := New(token, 1, s)
	r := request("POST", "/v1/skills/comparison", "")
	r.Body = comparisonBrokenBody{}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 || calls != 0 || len(h.controls) != 0 {
		t.Fatal(w.Code, calls)
	}
}
