package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func selectionAPIFixture(t *testing.T) (skills.ComparisonSelectionRequest, skills.ComparisonSelectionReport) {
	r, report := comparisonAPIFixture(t)
	input := skills.ComparisonSelectionRequest{Version: 1, ModelID: r.ModelID, Domain: r.Domain, Profile: r.Profile, Name: r.Name, BaselineVersion: r.BaselineVersion, CandidateVersion: r.CandidateVersion, Source: r.Source, MinSamples: r.MinSamples, MinDrop: r.MinDrop, Privacy: "local_only", TasksPerVersion: 20}
	out := skills.ComparisonSelectionReport{Version: 1, ConfiguredModelID: r.ModelID, Policy: skills.ComparisonSelectionPolicy{Version: 1, Comparison: report.Policy, Privacy: input.Privacy, TasksPerVersion: input.TasksPerVersion}}
	if input.Validate() != nil || out.Validate() != nil {
		t.Fatal("invalid fixture")
	}
	return input, out
}

func TestSkillComparisonSelectionHTTP(t *testing.T) {
	input, want := selectionAPIFixture(t)
	body, _ := json.Marshal(input)
	for _, mode := range []string{"success", "identity", "privacy", "count", "panic", "nil", "capacity", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			s := services()
			calls := 0
			s.SelectSkillComparison = func(ctx context.Context, got skills.ComparisonSelectionRequest) (skills.ComparisonSelectionReport, error) {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 5*time.Second || got != input {
					t.Error("unbounded or changed request")
				}
				out := want
				switch mode {
				case "identity":
					out.ConfiguredModelID = "wrong"
				case "privacy":
					out.Policy.Privacy = "cloud_allowed"
				case "count":
					out.Policy.TasksPerVersion = 21
				case "panic":
					panic("private-callback")
				}
				return out, nil
			}
			if mode == "nil" {
				s.SelectSkillComparison = nil
			}
			h, _ := New(token, 1, s)
			if mode == "capacity" {
				for i := 0; i < cap(h.controls); i++ {
					h.controls <- struct{}{}
				}
			}
			r := request("POST", "/v1/skills/comparison/select", string(body))
			if mode == "canceled" {
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			status := 200
			switch mode {
			case "identity", "privacy", "count":
				status = 500
			case "panic", "nil", "capacity", "canceled":
				status = 503
			}
			if w.Code != status || strings.Contains(w.Body.String(), "private-callback") {
				t.Fatal(w.Code, w.Body.String())
			}
			if mode == "success" {
				var got skills.ComparisonSelectionReport
				if json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Validate() != nil || got.Comparison != nil || calls != 1 {
					t.Fatal("invalid empty report")
				}
			}
		})
	}
}

func TestSkillComparisonSelectionHTTPStrictInput(t *testing.T) {
	input, _ := selectionAPIFixture(t)
	body, _ := json.Marshal(input)
	valid := string(body)
	s := services()
	s.SelectSkillComparison = func(context.Context, skills.ComparisonSelectionRequest) (skills.ComparisonSelectionReport, error) {
		t.Error("invalid request reached service")
		return skills.ComparisonSelectionReport{}, nil
	}
	h, _ := New(token, 1, s)
	for _, bad := range []string{`null`, valid + ` {}`, `{"version":1,` + valid[1:], strings.Replace(valid, `"version":1`, `"Version":1`, 1), strings.Replace(valid, `"version":1`, `"version":null`, 1), strings.Replace(valid, `"privacy":"local_only"`, `"privacy":"invalid"`, 1), strings.Replace(valid, `"domain":"creative"`, `"domain":"\ud800"`, 1), strings.Replace(valid, `"tasks_per_version":20`, `"tasks_per_version":19`, 1), strings.Replace(valid, `"tasks_per_version":20`, `"tasks_per_version":20,"tasks":[]`, 1), valid + strings.Repeat(" ", 64<<10)} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("POST", "/v1/skills/comparison/select", bad))
		if w.Code != 400 && w.Code != 413 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, test := range []struct {
		method, path, header, value string
		status                      int
	}{{"GET", "/v1/skills/comparison/select", "", "", 405}, {"POST", "/v1/skills/comparison/select?", "", "", 400}, {"POST", "/v1/skills/comparison/select?x=y", "", "", 400}, {"POST", "/v1/skills/comparison/select", "Authorization", "", 401}, {"POST", "/v1/skills/comparison/select", "Origin", "https://example.test", 403}, {"POST", "/v1/skills/comparison/select", "Content-Type", "text/plain", 415}} {
		r := request(test.method, test.path, valid)
		if test.header != "" {
			r.Header.Set(test.header, test.value)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatal(test, w.Code, w.Body.String())
		}
	}
}
