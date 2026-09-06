package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func appOutcomeRollbackFixture(t *testing.T, count int) (*Service, skills.ActivationState, skills.ComparisonSelectionRequest, []string) {
	t.Helper()
	svc, first, second, expected := appRegressionFixture(t)
	svc.settings.Skills.OutcomeRollback = true
	request := skills.ComparisonSelectionRequest{Version: 1, ModelID: "a", Domain: "creative", Profile: "default", Name: expected.Key.Name, BaselineVersion: first.ID, CandidateVersion: second.ID, Source: evaluation.UserFeedback, MinSamples: 20, MinDrop: .1, Privacy: "local_only", TasksPerVersion: 20}
	catalog, err := skills.OpenReadOnly(svc.settings.Skills.Root, []string{expected.Key.Scope})
	if err != nil {
		t.Fatal(err)
	}
	history, err := catalog.History(context.Background(), expected.Key)
	catalog.Close()
	if err != nil {
		t.Fatal(err)
	}
	digests := map[string]string{}
	for _, m := range history.Versions {
		digests[m.Version] = m.Digest
	}
	if digests[first.ID] == "" || digests[second.ID] == "" {
		t.Fatal("missing canonical digest")
	}
	db, err := telemetry.Open(context.Background(), svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var tasks []string
	model := svc.settings.Models[0]
	for i := 0; i < count; i++ {
		task := fmt.Sprintf("outcome-%03d", i)
		tasks = append(tasks, task)
		version, passed := first.ID, true
		if i >= count/2 {
			version, passed = second.ID, false
		}
		for j, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted} {
			e := runtime.Event{Version: 1, ID: task + "-" + string(kind), TaskID: task, SessionID: task, CorrelationID: task, Sequence: int64(j + 1), Time: time.Now().UTC(), Kind: kind}
			if j > 0 {
				e.TurnID = task + "-turn"
				e.AttemptID = task + "-attempt"
			}
			switch kind {
			case runtime.TaskStarted:
				e.Data = runtime.Data{Domain: "creative", Profile: "default", Privacy: "local_only", SkillContext: &runtime.SkillContextUse{Version: 1, Complete: true, References: []runtime.SkillReference{{Scope: expected.Key.Scope, Name: expected.Key.Name, Version: version, Digest: digests[version]}}}}
			case runtime.TurnStarted:
				e.Data = runtime.Data{ModelID: model.Model, ProviderID: model.Provider}
			case runtime.TurnCompleted:
				e.Data = runtime.Data{Text: "private subjective output", FinishReason: "stop"}
			}
			if err := db.Append(context.Background(), int64(j), e); err != nil {
				t.Fatal(err)
			}
		}
		record := evaluation.Record{Version: 1, ID: task + "-feedback", TaskID: task, AttemptID: task + "-attempt", Key: routing.Key{Model: model.Model, Provider: model.Provider, Domain: "creative", Profile: "default"}, Checks: []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "user", Passed: passed}}, ExecutionSucceeded: true, Time: time.Now().UTC()}
		if err := db.RecordEvaluation(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	return svc, expected, request, tasks
}

func TestOutcomeRollbackOnceActualEvidenceAndHistoricalRetry(t *testing.T) {
	svc, expected, request, tasks := appOutcomeRollbackFixture(t, 40)
	ctx := context.Background()
	db, err := telemetry.OpenReadOnly(ctx, svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.SkillTaskOutcomes(ctx, tasks)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := svc.OutcomeRollbackOnce(ctx, "outcome-operation", expected, request)
	if err != nil || receipt.Validate() != nil || receipt.Decision != "rolled_back" || receipt.After.Active != request.BaselineVersion || receipt.Expected != expected || receipt.Selection.Comparison == nil || !receipt.Selection.Comparison.AdvisoryOnly || receipt.Selection.Comparison.Policy.Source != evaluation.UserFeedback {
		t.Fatal(receipt, err)
	}
	catalog, err := skills.OpenReadOnly(svc.settings.Skills.Root, []string{expected.Key.Scope})
	if err != nil {
		t.Fatal(err)
	}
	history, err := catalog.History(ctx, expected.Key)
	catalog.Close()
	if err != nil || len(history.Activations) != 3 {
		t.Fatal(history, err)
	}
	last := history.Activations[2]
	if !last.Rollback || last.Regression != nil || last.Evidence != nil || last.OutcomeOperationID != "outcome-operation" {
		t.Fatal("observational evidence relabeled deterministic", last)
	}
	db, err = telemetry.OpenReadOnly(ctx, svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	after, err := db.SkillTaskOutcomes(ctx, tasks)
	db.Close()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("rollback changed observed evidence", err)
	}
	// Move only the owned closed fixture DB. Historical acknowledgement must not
	// reopen or recreate it; an operation receipt is the authoritative result.
	if err := os.Rename(svc.settings.Telemetry.Database, svc.settings.Telemetry.Database+".saved"); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewService(svc.settings, svc.secret)
	if err != nil {
		t.Fatal(err)
	}
	again, err := reopened.OutcomeRollbackOnce(ctx, "outcome-operation", expected, request)
	if err != nil || !reflect.DeepEqual(receipt, again) {
		t.Fatal("historical retry reread/recomputed", again, err)
	}
	if _, err := os.Stat(svc.settings.Telemetry.Database); !os.IsNotExist(err) {
		t.Fatal("retry recreated database", err)
	}
	reopened.settings.Skills.OutcomeRollback = false
	reopened.settings.Skills.Rollback = false
	inspected, err := reopened.OutcomeRollbackOperation(ctx, expected.Key, "outcome-operation")
	if err != nil || !reflect.DeepEqual(inspected, receipt) {
		t.Fatal("read-only receipt unavailable with kill switch", err)
	}
	if _, err := reopened.OutcomeRollbackOnce(ctx, "outcome-operation", expected, request); err == nil {
		t.Fatal("disabled mutation acknowledged through action")
	}
}

func TestOutcomeRollbackOnceNoActionConsumesRevision(t *testing.T) {
	for _, count := range []int{0, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			svc, expected, r, _ := appOutcomeRollbackFixture(t, count)
			receipt, err := svc.OutcomeRollbackOnce(context.Background(), "once", expected, r)
			if err != nil || receipt.Decision != "no_action" || receipt.After != expected || receipt.Validate() != nil {
				t.Fatal(receipt, err)
			}
			again, err := svc.OutcomeRollbackOnce(context.Background(), "once", expected, r)
			if err != nil || !reflect.DeepEqual(again, receipt) {
				t.Fatal(again, err)
			}
			path := filepath.Join(svc.settings.Skills.Root, "catalog.json")
			before, _ := os.ReadFile(path)
			if _, err := svc.OutcomeRollbackOnce(context.Background(), "another-look", expected, r); err == nil {
				t.Fatal("second decision per revision accepted")
			}
			changed := r
			changed.MinDrop = .2
			if _, err := svc.OutcomeRollbackOnce(context.Background(), "once", expected, changed); err == nil {
				t.Fatal("policy rebound")
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("rejected look mutated receipt")
			}
		})
	}
}

func TestOutcomeRollbackOnceNoSignalConsumesRevision(t *testing.T) {
	svc, expected, r, tasks := appOutcomeRollbackFixture(t, 40)
	for _, task := range tasks[20:] {
		if err := ReviseFeedback(context.Background(), svc.settings.Telemetry.Database, task, task+"-feedback", true); err != nil {
			t.Fatal(err)
		}
	}
	receipt, err := svc.OutcomeRollbackOnce(context.Background(), "no-signal", expected, r)
	if err != nil || receipt.Decision != "no_action" || receipt.After != expected || receipt.Selection.Comparison == nil || receipt.Selection.Comparison.Status != "no_regression_signal" {
		t.Fatal(receipt, err)
	}
	if _, err := svc.OutcomeRollbackOnce(context.Background(), "repeat-look", expected, r); err == nil {
		t.Fatal("no-signal decision did not consume revision")
	}
}

func TestOutcomeRollbackOnceHistoricalRetryRechecksHostPolicy(t *testing.T) {
	for _, mode := range []string{"kill-switch", "model-binding", "catalog-binding", "database-binding"} {
		t.Run(mode, func(t *testing.T) {
			svc, expected, r, _ := appOutcomeRollbackFixture(t, 0)
			if _, err := svc.OutcomeRollbackOnce(context.Background(), "historical", expected, r); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(svc.settings.Skills.Root, "catalog.json")
			before, _ := os.ReadFile(path)
			alternate := filepath.Join(t.TempDir(), "alternate")
			svc.secret = func(string) string {
				switch mode {
				case "kill-switch":
					svc.settings.Skills.OutcomeRollback = false
				case "model-binding":
					svc.settings.Models[0].Model = "different-model"
				case "catalog-binding":
					svc.settings.Skills.Root = alternate
				case "database-binding":
					svc.settings.Telemetry.Database = alternate
				}
				return ""
			}
			if out, err := svc.OutcomeRollbackOnce(context.Background(), "historical", expected, r); err == nil || out.Version != 0 {
				t.Fatal("historical action ignored rotated host policy", out, err)
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("denied historical action mutated receipt")
			}
		})
	}
}

func TestOutcomeRollbackOnceAppGuardsNoMutation(t *testing.T) {
	for _, mode := range []string{"opt-in", "disabled", "rollback-disabled", "unknown-model", "wrong-candidate", "wrong-baseline", "scope", "canceled", "nil-context", "secret-policy", "secret-version", "panic-secret", "invalid-source"} {
		t.Run(mode, func(t *testing.T) {
			svc, expected, r, _ := appOutcomeRollbackFixture(t, 0)
			ctx := context.Background()
			switch mode {
			case "opt-in":
				svc.settings.Skills.OutcomeRollback = false
			case "disabled":
				svc.settings.Skills.Enabled = false
			case "rollback-disabled":
				svc.settings.Skills.Rollback = false
			case "unknown-model":
				r.ModelID = "missing"
			case "wrong-candidate":
				r.CandidateVersion = strings.Repeat("a", 32)
			case "wrong-baseline":
				r.BaselineVersion = strings.Repeat("b", 32)
			case "scope":
				expected.Key.Scope = "foreign"
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "nil-context":
				ctx = nil
			case "secret-policy":
				svc.secret = func(string) string { return r.Name }
			case "secret-version":
				svc.secret = func(string) string { return expected.Active }
			case "panic-secret":
				svc.secret = func(string) string { panic("PRIVATE_ERROR") }
			case "invalid-source":
				r.Source = evaluation.LLMJudge
			}
			path := filepath.Join(svc.settings.Skills.Root, "catalog.json")
			before, _ := os.ReadFile(path)
			out, err := svc.OutcomeRollbackOnce(ctx, "rejected", expected, r)
			if err == nil || out.Version != 0 || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("unsafe admission result", out, err)
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("admission changed catalog")
			}
		})
	}
}

func TestOutcomeRollbackOnceRejectsCatalogDigestMismatch(t *testing.T) {
	svc, expected, r, _ := appOutcomeRollbackFixture(t, 40)
	// Catalog/version identity mismatch cannot be converted into statistical
	// authorization, even though the separately indexed historical data is valid.
	path := filepath.Join(svc.settings.Skills.Root, "catalog.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c map[string]any
	if json.Unmarshal(raw, &c) != nil {
		t.Fatal("fixture catalog")
	}
	entries := c["skills"].(map[string]any)
	entry := entries[expected.Key.Scope+"/"+expected.Key.Name].(map[string]any)
	for _, v := range entry["versions"].([]any) {
		m := v.(map[string]any)
		if m["version"] == r.CandidateVersion {
			m["digest"] = strings.Repeat("e", 64)
		}
	}
	altered, _ := json.Marshal(c)
	if err := os.WriteFile(path, altered, 0600); err != nil {
		t.Fatal(err)
	}
	out, err := svc.OutcomeRollbackOnce(context.Background(), "bad-digest", expected, r)
	if err == nil || out.Version != 0 {
		t.Fatal("mismatched digest accepted", out, err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(altered) {
		t.Fatal("digest failure mutated catalog")
	}
}
