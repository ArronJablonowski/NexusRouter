package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/health"
	"github.com/ArronJablonowski/DarwinRouter/internal/stateschema"
)

func providerHealthFixture(at time.Time, providerStatus, providerCode string) health.Report {
	report := health.Report{Version: 1, CheckedAt: at.UTC(), Checks: []health.Check{
		{Component: "daemon", Status: "healthy", Code: "serving"},
		{Component: "database", Status: "healthy", Code: "available"},
		{Component: "supervisor", Status: "healthy", Code: "supervisor_ok"},
		{Component: "resources", ID: "host", Status: "healthy", Code: "capacity_available"},
		{Component: "provider", ID: "ollama", Status: providerStatus, Code: providerCode},
		{Component: "model", ID: "worker", Status: "healthy", Code: "available"},
	}}
	report.Status, report.Ready = health.Outcome(report.Checks)
	return report
}

func TestProviderHealthHistoryRecordReadRetryAndRetention(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "health.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := time.Unix(1000, 0).UTC()
	first := providerHealthFixture(base, "healthy", "available")
	if err = store.RecordProviderHealth(ctx, first, 2); err != nil {
		t.Fatal(err)
	}
	if err = store.RecordProviderHealth(ctx, first, 2); err != nil {
		t.Fatal("exact retry failed", err)
	}
	if err = store.RecordProviderHealth(ctx, providerHealthFixture(base.Add(time.Second), "unavailable", "discovery_failed"), 2); err != nil {
		t.Fatal(err)
	}
	if err = store.RecordProviderHealth(ctx, providerHealthFixture(base.Add(2*time.Second), "healthy", "available"), 2); err != nil {
		t.Fatal(err)
	}
	history, err := store.ProviderHealthHistory(ctx, "provider", "ollama", 10)
	if err != nil || len(history) != 2 || !history[0].CheckedAt.Equal(base.Add(2*time.Second)) || !history[1].CheckedAt.Equal(base.Add(time.Second)) {
		t.Fatal("unexpected retained history", len(history), err)
	}
	models, err := store.ProviderHealthHistory(ctx, "model", "worker", 1)
	if err != nil || len(models) != 1 || !models[0].CheckedAt.Equal(base.Add(2*time.Second)) {
		t.Fatal("unexpected model history", len(models), err)
	}
	empty, err := store.ProviderHealthHistory(ctx, "provider", "missing", 10)
	if err != nil || len(empty) != 0 {
		t.Fatal("unexpected missing-provider history", empty, err)
	}
	var reports, checks int
	if err = store.db.QueryRow(`SELECT (SELECT count(*) FROM provider_health_reports),(SELECT count(*) FROM provider_health_checks)`).Scan(&reports, &checks); err != nil || reports != 2 || checks != 4 {
		t.Fatal("retention did not cascade", reports, checks, err)
	}
}

func TestProviderHealthHistoryRejectsInvalidAndCorruptRecords(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "health.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	report := providerHealthFixture(time.Unix(1000, 0), "healthy", "available")
	report.Checks[4].ID = "secret provider"
	if !errors.Is(store.RecordProviderHealth(ctx, report, 10), ErrProviderHealth) {
		t.Fatal("invalid health report accepted")
	}
	valid := providerHealthFixture(time.Unix(1001, 0), "healthy", "available")
	if err = store.RecordProviderHealth(ctx, valid, 10); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER provider_health_check_immutable_update;
		UPDATE provider_health_checks SET status='unavailable' WHERE component='provider'`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ProviderHealthHistory(ctx, "provider", "ollama", 10); !errors.Is(err, ErrProviderHealth) {
		t.Fatal("corrupt normalized history accepted", err)
	}
}

func TestProviderHealthHistoryRejectsCorruptReportMetadata(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "health.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	valid := providerHealthFixture(time.Unix(1001, 0), "healthy", "available")
	if err = store.RecordProviderHealth(ctx, valid, 10); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER provider_health_report_immutable_update;
		UPDATE provider_health_reports SET checked_at=checked_at+1`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ProviderHealthHistory(ctx, "provider", "ollama", 10); !errors.Is(err, ErrProviderHealth) {
		t.Fatal("corrupt report metadata accepted", err)
	}
}

func TestProviderHealthHistoryNoConfiguredProviderIsNoop(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "health.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	report := health.Report{Version: 1, CheckedAt: time.Unix(1000, 0).UTC(), Checks: []health.Check{
		{Component: "daemon", Status: "healthy", Code: "serving"},
		{Component: "database", Status: "healthy", Code: "available"},
		{Component: "supervisor", Status: "healthy", Code: "supervisor_ok"},
		{Component: "resources", ID: "host", Status: "healthy", Code: "capacity_available"},
	}}
	report.Status, report.Ready = health.Outcome(report.Checks)
	if err = store.RecordProviderHealth(ctx, report, 10); err != nil {
		t.Fatal("provider-free health report should be a no-op", err)
	}
	var reports int
	if err = store.db.QueryRow(`SELECT count(*) FROM provider_health_reports`).Scan(&reports); err != nil || reports != 0 {
		t.Fatal("provider-free report was persisted", reports, err)
	}
}

func dropProviderHealth53(db *sql.DB) error {
	_, err := db.Exec(`DROP TRIGGER provider_health_check_binding;
		DROP TRIGGER provider_health_check_immutable_update;
		DROP TRIGGER provider_health_report_immutable_update;
		DROP INDEX provider_health_checks_identity;
		DROP INDEX provider_health_reports_time;
		DROP TABLE provider_health_checks;
		DROP TABLE provider_health_reports;
		PRAGMA user_version=52;`)
	return err
}

func TestProviderHealthHistoryMigrationFresh52AndSchemaTamper(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "migration.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`CREATE TABLE provider_health_sentinel(value TEXT); INSERT INTO provider_health_sentinel VALUES('kept')`); err != nil {
		t.Fatal(err)
	}
	if err = dropProviderHealth53(store.db); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var version, sentinel int
	if err = store.db.QueryRow(`SELECT (SELECT user_version FROM pragma_user_version),(SELECT count(*) FROM provider_health_sentinel WHERE value='kept')`).Scan(&version, &sentinel); err != nil || version != stateschema.Current || sentinel != 1 {
		t.Fatal("migration did not preserve prior data", version, sentinel, err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER provider_health_report_immutable_update;
		CREATE TRIGGER provider_health_report_immutable_update BEFORE UPDATE ON provider_health_reports BEGIN SELECT 1; END;`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("tampered provider health schema reopened")
	}
}

func TestProviderHealthHistoryRejectsRetainedFutureData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "future.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RecordProviderHealth(ctx, providerHealthFixture(time.Unix(1000, 0), "healthy", "available"), 10); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`PRAGMA user_version=52`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("retained future provider health data accepted")
	}
}
