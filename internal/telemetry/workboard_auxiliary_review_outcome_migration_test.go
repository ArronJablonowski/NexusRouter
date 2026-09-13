package telemetry

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestSchema44AuxiliaryAdmissionsAreSealedWithoutInventedOutcomes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO task_heads(task_id,session_id,sequence,state) VALUES('task','session',1,'running')`); err != nil {
		t.Fatal(err)
	}
	insertWorkboardFixture(t, store.db)
	admission := auxiliaryReviewAdmission(t)
	if err = insertAuxiliaryReviewAdmission(t, store.db, admission); err != nil {
		t.Fatal(err)
	}
	dropWorkboardAuxiliaryReviewSchema45(t, store.db)
	if _, err = store.db.Exec(`PRAGMA user_version=44`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version, legacy, outcomes int
	var digest string
	if err = store.db.QueryRow(`SELECT
		(SELECT user_version FROM pragma_user_version),
		(SELECT count(*) FROM workboard_auxiliary_review_legacy_outcome_admissions),
		(SELECT admission_digest FROM workboard_auxiliary_review_legacy_outcome_admissions WHERE admission_id=?),
		(SELECT count(*) FROM workboard_auxiliary_review_outcomes)`, admission.AdmissionID).Scan(&version, &legacy, &digest, &outcomes); err != nil ||
		version != currentStorageSchema || legacy != 1 || digest != admission.AdmissionDigest || outcomes != 0 {
		t.Fatalf("schema=%d legacy=%d digest=%q outcomes=%d err=%v", version, legacy, digest, outcomes, err)
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_auxiliary_review_legacy_outcome_admissions(admission_id,admission_digest) VALUES('other',?)`, strings.Repeat("b", 64)); err == nil {
		t.Fatal("migration-sealed legacy marker accepted insert")
	}
	if _, err = store.db.Exec(`UPDATE workboard_auxiliary_review_legacy_outcome_admissions SET admission_digest=? WHERE admission_id=?`, strings.Repeat("b", 64), admission.AdmissionID); err == nil {
		t.Fatal("legacy marker accepted update")
	}
	if _, err = store.db.Exec(`DELETE FROM workboard_auxiliary_review_legacy_outcome_admissions WHERE admission_id=?`, admission.AdmissionID); err == nil {
		t.Fatal("legacy marker accepted delete")
	}
}

func TestSchema44DeclarationDiscardsOnlyEmptyFutureOutcomeTables(t *testing.T) {
	ctx := context.Background()
	t.Run("empty", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.db")
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.db.Exec(`PRAGMA user_version=44`); err != nil {
			t.Fatal(err)
		}
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		var version, outcomes int
		if err = reopened.db.QueryRow(`SELECT (SELECT user_version FROM pragma_user_version),
			(SELECT count(*) FROM workboard_auxiliary_review_outcomes)`).Scan(&version, &outcomes); err != nil || version != currentStorageSchema || outcomes != 0 {
			t.Fatalf("schema=%d outcomes=%d err=%v", version, outcomes, err)
		}
	})

	t.Run("retained provenance", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.db")
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.db.Exec(`INSERT INTO task_heads(task_id,session_id,sequence,state) VALUES('task','session',1,'running')`); err != nil {
			t.Fatal(err)
		}
		insertWorkboardFixture(t, store.db)
		admission := auxiliaryReviewAdmission(t)
		if err = insertAuxiliaryReviewAdmission(t, store.db, admission); err != nil {
			t.Fatal(err)
		}
		if _, err = store.db.Exec(`DROP TRIGGER workboard_auxiliary_review_legacy_outcome_admission_sealed_insert;
			INSERT INTO workboard_auxiliary_review_legacy_outcome_admissions(admission_id,admission_digest) VALUES(?,?);
			PRAGMA user_version=44`, admission.AdmissionID, admission.AdmissionDigest); err != nil {
			t.Fatal(err)
		}
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
		if reopened, openErr := Open(ctx, path); openErr == nil {
			reopened.Close()
			t.Fatal("schema-44 declaration retained schema-45 provenance authority")
		}
		raw, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		var version int
		if err = raw.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 44 {
			t.Fatalf("failed migration changed schema=%d err=%v", version, err)
		}
	})
}

func TestWorkboardAuxiliaryReviewOutcomeSchemaTamperFailsClosed(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER workboard_auxiliary_review_outcome_binding;
		CREATE TRIGGER workboard_auxiliary_review_outcome_binding BEFORE INSERT ON workboard_auxiliary_review_outcomes
		BEGIN SELECT RAISE(ABORT,'forged'); END`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("forged schema-45 outcome trigger accepted")
	}
}

func TestWorkboardAuxiliaryReviewChargeLimitTriggerRequired(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER workboard_auxiliary_review_settlement_charge_limit`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("schema without bounded auxiliary review charges accepted")
	}
}
