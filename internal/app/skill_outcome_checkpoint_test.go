package app

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/skills"
	"modernc.org/sqlite"
)

func appOutcomeCheckpointFixture(t *testing.T) (*Service, skills.ActivationState, skills.ComparisonSelectionRequest, skills.OutcomeSelectionCheckpoint, []string) {
	t.Helper()
	svc, expected, request, tasks := appOutcomeRollbackFixture(t, 40)
	originalSecret := svc.secret
	path := filepath.Join(svc.settings.Skills.Root, "catalog.json")
	observed := false
	// This trusted fixture callback rotates a synthetic credential only AFTER
	// the checkpoint is durable. The final commit guards must reject it while
	// preserving the checkpoint and unchanged activation for an exact retry.
	svc.secret = func(name string) string {
		body, err := os.ReadFile(path)
		if err == nil {
			var catalog struct {
				Selections map[string]skills.OutcomeSelectionCheckpoint `json:"outcome_selections"`
			}
			if json.Unmarshal(body, &catalog) == nil {
				if cp, ok := catalog.Selections["saved-selection"]; ok && cp.Version == 1 && cp.OperationID == "saved-selection" {
					observed = true
					return expected.Active
				}
			}
		}
		if originalSecret != nil {
			return originalSecret(name)
		}
		return ""
	}
	out, err := svc.OutcomeRollbackOnce(context.Background(), "saved-selection", expected, request)
	if err == nil || out.Version != 0 || !observed {
		t.Fatal("fixture did not reject after saved selection", out, err, observed)
	}
	svc.secret = originalSecret
	checkpoint, err := svc.OutcomeSelectionCheckpoint(context.Background(), expected.Key, "saved-selection")
	if err != nil || checkpoint.Validate() != nil || checkpoint.Intent.Expected != expected || checkpoint.Report.Comparison == nil || checkpoint.Report.Comparison.Status != "regression_signal" {
		t.Fatal("selection was not checkpointed", checkpoint, err)
	}
	current, err := svc.SkillActivationState(context.Background(), expected.Key)
	if err != nil || current != expected {
		t.Fatal("guard failure changed activation", current, err)
	}
	if receipt, err := svc.OutcomeRollbackOperation(context.Background(), expected.Key, "saved-selection"); err == nil || receipt.Version != 0 {
		t.Fatal("guard failure published completed receipt", receipt, err)
	}
	return svc, expected, request, checkpoint, tasks
}

var appOutcomeCheckpointProbe atomic.Uint64

func TestOutcomeCheckpointSelectionGuardRejectsNewSensitiveMetadata(t *testing.T) {
	svc, expected, request, _ := appOutcomeRollbackFixture(t, 40)
	var selected atomic.Bool
	function := fmt.Sprintf("app_outcome_checkpoint_probe_%d", appOutcomeCheckpointProbe.Add(1))
	if err := sqlite.RegisterScalarFunction(function, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		selected.Store(true)
		return args[0], nil
	}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE skill_exposures RENAME TO checkpoint_exposure_rows; CREATE VIEW skill_exposures AS SELECT task_id,scope,name,version,` + function + `(digest) AS digest,ordinal,privacy FROM checkpoint_exposure_rows`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	lookupsAfterSelection := 0
	svc.secret = func(name string) string {
		if name == "DARWIN_API_TOKEN" && selected.Load() {
			lookupsAfterSelection++
			// The selector performs its two post-read privacy checks; the next
			// check is the separate guard immediately before saving checkpoint.
			if lookupsAfterSelection >= 3 {
				return request.Domain
			}
		}
		return ""
	}
	out, err := svc.OutcomeRollbackOnce(context.Background(), "guard-sensitive-selection", expected, request)
	if err == nil || out.Version != 0 || lookupsAfterSelection < 3 {
		t.Fatal("fixture did not reject checkpoint guard", out, err, lookupsAfterSelection)
	}
	svc.secret = nil
	if checkpoint, err := svc.OutcomeSelectionCheckpoint(context.Background(), expected.Key, "guard-sensitive-selection"); err == nil || checkpoint.Version != 0 {
		t.Fatal("sensitive checkpoint persisted", checkpoint, err)
	}
	if intent, err := svc.OutcomeRollbackIntent(context.Background(), expected.Key, "guard-sensitive-selection"); err != nil || intent.Validate() != nil {
		t.Fatal("preselection claim lost", intent, err)
	}
	current, err := svc.SkillActivationState(context.Background(), expected.Key)
	if err != nil || current != expected {
		t.Fatal("denied checkpoint changed activation", current, err)
	}
	if receipt, err := svc.OutcomeRollbackOperation(context.Background(), expected.Key, "guard-sensitive-selection"); err == nil || receipt.Version != 0 {
		t.Fatal("denied checkpoint fabricated receipt", receipt, err)
	}
}

func TestOutcomeCheckpointRetryUsesFrozenReportAfterFeedbackAndDatabaseChange(t *testing.T) {
	svc, expected, request, checkpoint, tasks := appOutcomeCheckpointFixture(t)
	ctx := context.Background()
	for _, task := range tasks[20:] {
		if err := ReviseFeedback(ctx, svc.settings.Telemetry.Database, task, task+"-feedback", true); err != nil {
			t.Fatal(err)
		}
	}
	current, err := svc.SelectSkillComparison(ctx, request)
	if err != nil || current.Comparison == nil || current.Comparison.Status != "no_regression_signal" || current.Comparison.EvidenceDigest == checkpoint.Report.Comparison.EvidenceDigest {
		t.Fatal("changed evidence fixture not qualified", current, err)
	}
	database := svc.settings.Telemetry.Database
	if err := os.Rename(database, database+".saved"); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewService(svc.settings, svc.secret)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := restarted.OutcomeRollbackOnce(ctx, "saved-selection", expected, request)
	if err != nil || receipt.Validate() != nil || receipt.Decision != "rolled_back" || receipt.After.Active != request.BaselineVersion || !reflect.DeepEqual(receipt.Selection, checkpoint.Report) {
		t.Fatal("retry did not use original frozen observation", receipt, err)
	}
	if _, err := os.Stat(database); !os.IsNotExist(err) {
		t.Fatal("checkpoint retry reopened/created database", err)
	}
	persisted, err := restarted.OutcomeSelectionCheckpoint(ctx, expected.Key, "saved-selection")
	if err != nil || !reflect.DeepEqual(checkpoint, persisted) {
		t.Fatal("retry rewrote checkpoint", persisted, err)
	}
	restarted.settings.Skills.OutcomeRollback = false
	audit, err := restarted.OutcomeSelectionCheckpoint(ctx, expected.Key, "saved-selection")
	if err != nil || !reflect.DeepEqual(checkpoint, audit) {
		t.Fatal("disabled checkpoint audit unavailable", err)
	}
}

func TestOutcomeCheckpointRetryPreservesCurrentGuards(t *testing.T) {
	for _, mode := range []string{"stale", "policy", "model-binding", "secret", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			svc, expected, request, checkpoint, _ := appOutcomeCheckpointFixture(t)
			switch mode {
			case "stale":
				store, err := skills.Open(svc.settings.Skills.Root, []string{expected.Key.Scope})
				if err != nil {
					t.Fatal(err)
				}
				err = store.RollbackAt(context.Background(), expected, false)
				store.Close()
				if err != nil {
					t.Fatal(err)
				}
			case "policy":
				request.MinDrop = .2
			case "model-binding":
				svc.settings.Models[0].Model = "other-model"
			case "secret":
				svc.secret = func(string) string { return checkpoint.Report.Comparison.EvidenceDigest }
			case "disabled":
				svc.settings.Skills.OutcomeRollback = false
			}
			path := filepath.Join(svc.settings.Skills.Root, "catalog.json")
			before, _ := os.ReadFile(path)
			out, err := svc.OutcomeRollbackOnce(context.Background(), "saved-selection", expected, request)
			if err == nil || out.Version != 0 {
				t.Fatal("saved report bypassed current guard", out, err)
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("denied retry mutated checkpoint/activation")
			}
		})
	}
}

func TestOutcomeCheckpointInspectionBoundaries(t *testing.T) {
	svc, expected, _, checkpoint, _ := appOutcomeCheckpointFixture(t)
	path := filepath.Join(svc.settings.Skills.Root, "catalog.json")
	before, _ := os.ReadFile(path)
	for _, id := range []string{"missing", "bad/id", ""} {
		if out, err := svc.OutcomeSelectionCheckpoint(context.Background(), expected.Key, id); err == nil || out.Version != 0 {
			t.Fatal("invalid checkpoint inspection", out, err)
		}
	}
	if out, err := svc.OutcomeSelectionCheckpoint(nil, expected.Key, "saved-selection"); err == nil || out.Version != 0 {
		t.Fatal("nil inspection")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, err := svc.OutcomeSelectionCheckpoint(ctx, expected.Key, "saved-selection"); err == nil || out.Version != 0 {
		t.Fatal("canceled inspection")
	}
	foreign := expected.Key
	foreign.Scope = "foreign"
	if out, err := svc.OutcomeSelectionCheckpoint(context.Background(), foreign, "saved-selection"); err == nil || out.Version != 0 {
		t.Fatal("foreign checkpoint exported")
	}
	svc.secret = func(string) string { return checkpoint.Report.Comparison.EvidenceDigest }
	if out, err := svc.OutcomeSelectionCheckpoint(context.Background(), expected.Key, "saved-selection"); err == nil || out.Version != 0 || strings.Contains(err.Error(), checkpoint.Report.Comparison.EvidenceDigest) {
		t.Fatal("sensitive checkpoint exported", out, err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("inspection changed checkpoint")
	}
}
