package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func TestAuxiliaryAllocationMatchesWorkingTier(t *testing.T) {
	for _, operation := range []string{"audit", "summary", "prepared-summary", "skill"} {
		for _, over := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/overflow=%v", operation, over), func(t *testing.T) {
				svc, tasks := skillGenerationAppFixture(t)
				for i := range svc.settings.Models {
					if svc.settings.Models[i].ID == "z" {
						svc.settings.Models[i].ContextTokens = 131072
						svc.settings.Models[i].DefaultContextTokens = 16384
					}
				}
				var calls atomic.Int32
				estimates := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					var wire struct {
						Options struct {
							Context int64 `json:"num_ctx"`
						} `json:"options"`
					}
					if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
						t.Error(err)
					}
					if wire.Options.Context != 16384 {
						t.Errorf("provider allocated %d; host reserved 16384", wire.Options.Context)
					}
					output := `{"version":1,"summary":{"decisions":["preserve source"]}}`
					if operation == "audit" {
						output = `{"version":1,"evaluator_id":"z","rubric_version":"darwin-review-v2","domain":"creative","verdict":"abstain","confidence":0,"findings":[]}`
					}
					if operation == "skill" {
						output = `{"version":1,"description":"Follow the requested workflow","tags":["creative"],"steps":["Inspect the requirements"],"required_tools":[],"configuration":"","risks":[],"validation_cases":["Check requested constraints"]}`
					}
					fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", output)
				}))
				defer server.Close()
				svc.settings.Providers[0].Endpoint = server.URL
				svc.contextEstimator = auxiliaryContextEstimator(func(_ context.Context, r providers.Request) (int, error) {
					estimates++
					if r.ContextTokens != 16384 {
						t.Errorf("estimator received allocation %d; want 16384", r.ContextTokens)
					}
					if over {
						return 20000, nil
					}
					return 0, nil
				})
				ctx := context.Background()
				var err error
				failed := false
				switch operation {
				case "audit":
					_, err = svc.AuditTask(ctx, tasks[0], "z", 0)
				case "summary":
					_, err = svc.SummarizeTask(ctx, tasks[0], "z", 1, 0)
				case "prepared-summary":
					result, prepareErr := svc.PrepareSummary(ctx, "auxiliary-allocation-preparation", PrepareSummaryRequest{Version: 1, TaskID: tasks[0], ModelID: "z", Keep: 1})
					err = prepareErr
					failed = result.TerminalAttempt != nil && result.TerminalAttempt.Status == "failed"
				case "skill":
					_, err = svc.GenerateSkillDraft(ctx, "allocation-generation", "z", skills.Key{Scope: "project", Name: "allocation-workflow"}, tasks, 0)
				}
				if over {
					if err == nil && !failed || calls.Load() != 0 {
						t.Fatalf("overflow reached provider: err=%v failed=%v calls=%d", err, failed, calls.Load())
					}
				} else if err != nil || failed || calls.Load() != 1 {
					t.Fatalf("working allocation failed: err=%v failed=%v calls=%d", err, failed, calls.Load())
				}
				if estimates != 1 {
					t.Fatalf("expected one estimation, got %d", estimates)
				}
			})
		}
	}
}
