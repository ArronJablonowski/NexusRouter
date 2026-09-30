package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestSkillGenerationBoundaryFailuresAreDurable(t *testing.T) {
	for _, mode := range []string{"invalid-output", "canceled", "secret-tag", "estimate-high", "estimate-error"} {
		t.Run(mode, func(t *testing.T) {
			svc, tasks := skillGenerationAppFixture(t)
			base := context.Background()
			ctx, cancel := context.WithCancel(base)
			defer cancel()
			db, err := telemetry.OpenReadOnly(base, svc.settings.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			before, err := db.SkillWorkflowSources(base, tasks)
			if err != nil {
				t.Fatal(err)
			}
			var calls, estimates atomic.Int32
			checkStarted := func() {
				a, err := db.SkillGenerationAttempt(base, "boundary")
				if err != nil || a.Status != "started" {
					t.Error("work before durable start", a, err)
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				checkStarted()
				if mode == "canceled" {
					cancel()
					return
				}
				draft := `{"version":1,"description":"Workflow","tags":["runtime-token"],"steps":["Inspect requirements"],"required_tools":[],"configuration":"","risks":[],"validation_cases":["Check requirements"]}`
				if mode == "invalid-output" {
					draft = "private-invalid-generated-output"
				}
				fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", draft)
			}))
			defer server.Close()
			svc.settings.Providers[0].Endpoint = server.URL
			svc.secret = func(name string) string {
				if name == "DARWIN_API_TOKEN" {
					return "runtime-token"
				}
				return ""
			}
			if strings.HasPrefix(mode, "estimate-") {
				svc.contextEstimator = auxiliaryContextEstimator(func(_ context.Context, request providers.Request) (int, error) {
					estimates.Add(1)
					checkStarted()
					body, _ := json.Marshal(request)
					if request.Model != "a" || len(request.Tools) != 0 || strings.Contains(string(body), "runtime-token") || !strings.Contains(string(body), "[REDACTED]") {
						t.Error("unsafe estimator input")
					}
					if mode == "estimate-error" {
						return 0, errors.New("private-estimator-error")
					}
					return 8193, nil
				})
			}
			a, err := svc.GenerateSkillDraft(ctx, "boundary", "a", skills.Key{Scope: "project", Name: "workflow"}, tasks, 0)
			if err == nil || a.Status != "failed" || a.Result != nil {
				t.Fatal("failure not recorded", a, err)
			}
			wantCode := "generation_failed"
			if mode == "canceled" {
				wantCode = "canceled"
			}
			if a.Code != wantCode {
				t.Fatal(a.Code)
			}
			saved, readErr := db.SkillGenerationAttempt(base, "boundary")
			if readErr != nil || !reflect.DeepEqual(a, saved) {
				t.Fatal("failed attempt not durable", saved, readErr)
			}
			body, _ := json.Marshal(saved)
			for _, private := range []string{"runtime-token", "private-estimator-error", "private-invalid-generated-output"} {
				if strings.Contains(string(body), private) || strings.Contains(err.Error(), private) {
					t.Fatal("private failure content escaped")
				}
			}
			if strings.HasPrefix(mode, "estimate-") {
				if estimates.Load() != 1 || calls.Load() != 0 {
					t.Fatal("estimator did not prevent dispatch", estimates.Load(), calls.Load())
				}
			} else if calls.Load() != 1 {
				t.Fatal("unexpected provider dispatches", calls.Load())
			}
			after, err := db.SkillWorkflowSources(base, tasks)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("source history changed", err)
			}
		})
	}
}

func TestSkillGenerationBoundaryAdmissionLeavesNoAttempt(t *testing.T) {
	for _, mode := range []string{"secret-attempt", "secret-key", "secret-source-domain", "private-source-cloud"} {
		t.Run(mode, func(t *testing.T) {
			svc, tasks := skillGenerationAppFixture(t)
			ctx := context.Background()
			id := "boundary"
			key := skills.Key{Scope: "project", Name: "workflow"}
			secret := "runtime-token"
			switch mode {
			case "secret-attempt":
				id = secret
			case "secret-key":
				key.Name = secret
			case "secret-source-domain":
				secret = "creative"
			case "private-source-cloud":
				svc.settings.Mode = "hybrid"
				svc.settings.Skills.LocalOnly = false
				svc.settings.Models[0].Locality = "cloud"
			}
			svc.secret = func(name string) string {
				if name == "DARWIN_API_TOKEN" {
					return secret
				}
				return ""
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "unexpected", 500) }))
			defer server.Close()
			svc.settings.Providers[0].Endpoint = server.URL
			if _, err := svc.GenerateSkillDraft(ctx, id, "a", key, tasks, 0); !errors.Is(err, ErrAdmission) {
				t.Fatal("expected admission denial", err)
			}
			if calls.Load() != 0 {
				t.Fatal("denied generation dispatched")
			}
			db, err := telemetry.OpenReadOnly(ctx, svc.settings.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			attempts, err := db.ListSkillGenerationAttempts(ctx, "project", "", 100)
			if err != nil || len(attempts) != 0 {
				t.Fatal("denied generation persisted attempt", attempts, err)
			}
		})
	}
}
