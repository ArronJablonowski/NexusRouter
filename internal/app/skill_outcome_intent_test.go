package app

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/skills"
	"modernc.org/sqlite"
)

var appOutcomeIntentProbe atomic.Uint64

func TestOutcomeIntentMissingDatabaseSurvivesRestartAndFencesAllLooks(t *testing.T) {
	svc, expected, request, _ := appOutcomeRollbackFixture(t, 40)
	// A later configuration renames this model while preserving its execution
	// identity. Simultaneous duplicate provider/model routes are prohibited.
	const aliasID = "same-execution-alias"
	if err := svc.settings.Validate(); err != nil {
		t.Fatal("invalid initial fixture settings", err)
	}
	database := svc.settings.Telemetry.Database
	saved := database + ".saved"
	if err := os.Rename(database, saved); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if out, err := svc.OutcomeRollbackOnce(ctx, "claimed-before-selection", expected, request); err == nil || out.Version != 0 {
		t.Fatal("missing SQLite generated a receipt", out, err)
	}
	if _, err := os.Stat(database); !os.IsNotExist(err) {
		t.Fatal("selector created missing database", err)
	}
	intent, err := svc.OutcomeRollbackIntent(ctx, expected.Key, "claimed-before-selection")
	if err != nil || intent.Validate() != nil || intent.Expected != expected || intent.OperationID != "claimed-before-selection" || intent.ConfiguredModelID != request.ModelID || intent.ActivationCount != 2 {
		t.Fatal("missing durable preselection claim", intent, err)
	}
	if receipt, err := svc.OutcomeRollbackOperation(ctx, expected.Key, "claimed-before-selection"); err == nil || receipt.Version != 0 {
		t.Fatal("failed selection fabricated completed receipt", receipt, err)
	}
	current, err := svc.SkillActivationState(ctx, expected.Key)
	if err != nil || current != expected {
		t.Fatal("failed selection changed activation", current, err)
	}
	if err := os.Rename(saved, database); err != nil {
		t.Fatal(err)
	}

	// A test-only scalar in a view observes actual exposure selection. Register
	// before app opens its next connection; there is no production hook.
	var selected atomic.Int32
	function := fmt.Sprintf("app_outcome_intent_probe_%d", appOutcomeIntentProbe.Add(1))
	if err := sqlite.RegisterScalarFunction(function, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		selected.Add(1)
		return args[0], nil
	}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE skill_exposures RENAME TO intent_exposure_rows; CREATE VIEW skill_exposures AS SELECT task_id,scope,name,version,` + function + `(digest) AS digest,ordinal,privacy FROM intent_exposure_rows`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	restarted, err := NewService(svc.settings, svc.secret)
	if err != nil {
		t.Fatal(err)
	}
	qualification, err := restarted.SelectSkillComparison(ctx, request)
	if err != nil || qualification.Comparison == nil || qualification.Comparison.Status != "regression_signal" || selected.Load() == 0 {
		t.Fatal("restored fixture/probe not qualified", qualification, err, selected.Load())
	}
	selected.Store(0)
	catalog := filepath.Join(svc.settings.Skills.Root, "catalog.json")
	before, err := os.ReadFile(catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"same-id", "new-id", "policy", "model-alias"} {
		t.Run(mode, func(t *testing.T) {
			r := request
			id := "claimed-before-selection"
			caller := restarted
			switch mode {
			case "new-id":
				id = "alternate-look"
			case "policy":
				r.MinDrop = .2
			case "model-alias":
				r.ModelID = aliasID
				settings := restarted.settings
				settings.Models = append(settings.Models[:0:0], settings.Models...)
				settings.Models[0].ID = aliasID
				caller, err = NewService(settings, restarted.secret)
				if err != nil {
					t.Fatal(err)
				}
			}
			out, err := caller.OutcomeRollbackOnce(ctx, id, expected, r)
			if err == nil || out.Version != 0 {
				t.Fatal("incomplete intent was reselected", out, err)
			}
			if selected.Load() != 0 {
				t.Fatal("pending operation reread restored SQLite", selected.Load())
			}
		})
	}
	after, err := os.ReadFile(catalog)
	if err != nil || string(before) != string(after) {
		t.Fatal("blocked retries changed intent", err)
	}
	current, err = restarted.SkillActivationState(ctx, expected.Key)
	if err != nil || current != expected {
		t.Fatal("blocked retries changed active version", current, err)
	}
	reopenedIntent, err := restarted.OutcomeRollbackIntent(ctx, expected.Key, intent.OperationID)
	if err != nil || !reflect.DeepEqual(intent, reopenedIntent) {
		t.Fatal("restart rebound intent", reopenedIntent, err)
	}
	restarted.settings.Skills.OutcomeRollback = false
	inspected, err := restarted.OutcomeRollbackIntent(ctx, expected.Key, intent.OperationID)
	if err != nil || !reflect.DeepEqual(inspected, intent) {
		t.Fatal("kill switch blocked read-only intent audit", err)
	}
}

func TestOutcomeIntentInspectionDoesNotCreateOrAcceptInvalidIdentity(t *testing.T) {
	svc, expected, _, _ := appOutcomeRollbackFixture(t, 0)
	for _, id := range []string{"missing", "bad/id", ""} {
		out, err := svc.OutcomeRollbackIntent(context.Background(), expected.Key, id)
		if err == nil || out.Version != 0 {
			t.Fatal("invalid/missing intent accepted", out, err)
		}
	}
	if out, err := svc.OutcomeRollbackIntent(nil, expected.Key, "missing"); err == nil || out.Version != 0 {
		t.Fatal("nil context accepted")
	}
	path := filepath.Join(svc.settings.Skills.Root, "catalog.json")
	before, _ := os.ReadFile(path)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, err := svc.OutcomeRollbackIntent(ctx, skills.Key{Scope: expected.Key.Scope, Name: expected.Key.Name}, "missing"); err == nil || out.Version != 0 {
		t.Fatal("canceled inspection accepted")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("inspection mutated catalog")
	}
}

func TestOutcomeIntentAdmissionGuardDeniesRotatedMetadataBeforeClaim(t *testing.T) {
	for _, mode := range []string{"secret-policy", "secret-model", "model-binding", "kill-switch"} {
		t.Run(mode, func(t *testing.T) {
			svc, expected, request, _ := appOutcomeRollbackFixture(t, 0)
			path := filepath.Join(svc.settings.Skills.Root, "catalog.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			svc.secret = func(name string) string {
				if name != "DARWIN_API_TOKEN" {
					return ""
				}
				calls++
				if calls < 2 {
					return ""
				}
				switch mode {
				case "secret-policy":
					return request.Domain
				case "secret-model":
					return request.ModelID
				case "model-binding":
					svc.settings.Models[0].Model = "changed-model"
				case "kill-switch":
					svc.settings.Skills.OutcomeRollback = false
				}
				return ""
			}
			if out, err := svc.OutcomeRollbackOnce(context.Background(), "guarded-claim", expected, request); err == nil || out.Version != 0 {
				t.Fatal("rotated metadata admitted", out, err)
			}
			if calls < 2 {
				t.Fatal("fixture did not reach intent guard")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(before) != string(after) {
				t.Fatal("denied intent persisted metadata", err)
			}
			store, err := skills.OpenReadOnly(svc.settings.Skills.Root, []string{expected.Key.Scope})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if intent, err := store.OutcomeRollbackIntent(context.Background(), expected.Key, "guarded-claim"); err == nil || intent.Version != 0 {
				t.Fatal("denied claim exists", intent, err)
			}
		})
	}
}

func TestOutcomeIntentGetterRejectsSensitivePendingMetadataAndWrongScope(t *testing.T) {
	svc, expected, request, _ := appOutcomeRollbackFixture(t, 0)
	if err := os.Rename(svc.settings.Telemetry.Database, svc.settings.Telemetry.Database+".saved"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.OutcomeRollbackOnce(context.Background(), "pending-private", expected, request); err == nil {
		t.Fatal("missing evidence accepted")
	}
	if _, err := svc.OutcomeRollbackIntent(context.Background(), expected.Key, "pending-private"); err != nil {
		t.Fatal("pending fixture missing", err)
	}
	path := filepath.Join(svc.settings.Skills.Root, "catalog.json")
	before, _ := os.ReadFile(path)
	svc.secret = func(string) string { return request.Domain }
	if out, err := svc.OutcomeRollbackIntent(context.Background(), expected.Key, "pending-private"); err == nil || out.Version != 0 {
		t.Fatal("sensitive pending policy exported", out, err)
	}
	svc.secret = nil
	foreign := expected.Key
	foreign.Scope = "other-project"
	if out, err := svc.OutcomeRollbackIntent(context.Background(), foreign, "pending-private"); err == nil || out.Version != 0 {
		t.Fatal("foreign pending intent exported", out, err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("pending inspection mutated catalog")
	}
}
