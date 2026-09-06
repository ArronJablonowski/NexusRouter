package app

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
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

func TestOutcomeCheckpointRetryRejectsChangedFeedbackAndMissingDatabase(t *testing.T) {
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
	path := filepath.Join(svc.settings.Skills.Root, "catalog.json")
	before, _ := os.ReadFile(path)
	if out, err := svc.OutcomeRollbackOnce(ctx, "saved-selection", expected, request); err == nil || out.Version != 0 {
		t.Fatal("changed feedback authorized old signal", out, err)
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
	if err == nil || receipt.Version != 0 {
		t.Fatal("missing evidence authorized checkpoint", receipt, err)
	}
	if _, err := os.Stat(database); !os.IsNotExist(err) {
		t.Fatal("checkpoint retry created database", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("denied freshness check rewrote catalog")
	}
	state, err := restarted.SkillActivationState(ctx, expected.Key)
	if err != nil || state != expected {
		t.Fatal("denied freshness changed activation", state, err)
	}
	if out, err := restarted.OutcomeRollbackOperation(ctx, expected.Key, "saved-selection"); err == nil || out.Version != 0 {
		t.Fatal("denied freshness fabricated receipt")
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

func TestOutcomeCheckpointFreshRetryChecksFixedSourcesWithoutLatestSelection(t *testing.T) {
	svc, expected, request, checkpoint, tasks := appOutcomeCheckpointFixture(t)
	if checkpoint.Report.Sources == nil || checkpoint.Report.Sources.Version != 1 || !reflect.DeepEqual(checkpoint.Report.Sources.Tasks, tasks) {
		t.Fatal("checkpoint lacks exact sorted selected source identities")
	}
	var queried atomic.Int32
	function := fmt.Sprintf("app_checkpoint_no_reselection_%d", appOutcomeCheckpointProbe.Add(1))
	if err := sqlite.RegisterScalarFunction(function, 1, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
		queried.Add(1)
		return nil, errors.New("fixture latest-window trap")
	}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE skill_exposures RENAME TO freshness_exposure_rows; CREATE VIEW skill_exposures AS SELECT task_id,scope,name,version,` + function + `(digest) AS digest,ordinal,privacy FROM freshness_exposure_rows`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	// First qualify the trap against the automatic latest-window selector.
	if _, err := svc.SelectSkillComparison(context.Background(), request); err == nil || queried.Load() == 0 {
		t.Fatal("latest-window trap was not reached", err)
	}
	queried.Store(0)
	receipt, err := svc.OutcomeRollbackOnce(context.Background(), "saved-selection", expected, request)
	if err != nil || receipt.Decision != "rolled_back" || !reflect.DeepEqual(receipt.Selection, checkpoint.Report) || queried.Load() != 0 {
		t.Fatal("fixed-source retry reran selection or changed report", receipt, err, queried.Load())
	}
	if err := os.Rename(svc.settings.Telemetry.Database, svc.settings.Telemetry.Database+".saved"); err != nil {
		t.Fatal(err)
	}
	again, err := svc.OutcomeRollbackOnce(context.Background(), "saved-selection", expected, request)
	if err != nil || !reflect.DeepEqual(again, receipt) {
		t.Fatal("completed receipt unnecessarily rechecked unavailable sources", again, err)
	}
}

func TestOutcomeCheckpointLegacySourcelessReportInspectableButNotResumable(t *testing.T) {
	svc, expected, request, checkpoint, _ := appOutcomeCheckpointFixture(t)
	path := filepath.Join(svc.settings.Skills.Root, "catalog.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var catalog map[string]json.RawMessage
	if json.Unmarshal(body, &catalog) != nil {
		t.Fatal("fixture catalog")
	}
	var checkpoints map[string]skills.OutcomeSelectionCheckpoint
	if json.Unmarshal(catalog["outcome_selections"], &checkpoints) != nil {
		t.Fatal("fixture checkpoints")
	}
	checkpoint.Report.Sources = nil
	checkpoints["saved-selection"] = checkpoint
	catalog["outcome_selections"], err = json.Marshal(checkpoints)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	inspected, err := svc.OutcomeSelectionCheckpoint(context.Background(), expected.Key, "saved-selection")
	if err != nil || inspected.Report.Sources != nil || inspected.Validate() != nil {
		t.Fatal("legacy source-less checkpoint unavailable for audit", inspected, err)
	}
	if out, err := svc.OutcomeRollbackOnce(context.Background(), "saved-selection", expected, request); err == nil || out.Version != 0 {
		t.Fatal("legacy source-less checkpoint resumed", out, err)
	}
	after, _ := os.ReadFile(path)
	if string(legacy) != string(after) {
		t.Fatal("source-less rejection mutated catalog")
	}
}

func TestOutcomeCheckpointRetryPreservesCurrentGuards(t *testing.T) {
	for _, mode := range []string{"stale", "policy", "model-binding", "secret", "disabled", "new-session-sibling", "missing-database"} {
		t.Run(mode, func(t *testing.T) {
			svc, expected, request, checkpoint, tasks := appOutcomeCheckpointFixture(t)
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
			case "missing-database":
				if err := os.Rename(svc.settings.Telemetry.Database, svc.settings.Telemetry.Database+".saved"); err != nil {
					t.Fatal(err)
				}
			case "new-session-sibling":
				db, err := telemetry.Open(context.Background(), svc.settings.Telemetry.Database)
				if err != nil {
					t.Fatal(err)
				}
				e := runtime.Event{Version: 1, ID: "new-sibling-start", TaskID: "new-sibling", SessionID: tasks[0], CorrelationID: "new-sibling", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted}
				err = db.Append(context.Background(), 0, e)
				db.Close()
				if err != nil {
					t.Fatal(err)
				}
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
