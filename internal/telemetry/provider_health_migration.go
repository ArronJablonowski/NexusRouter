package telemetry

import (
	"context"
	"database/sql"
	"errors"
)

// Schema 53 adds bounded durable history for daemon-owned provider/model health
// probes. Bodies are validated health.Report JSON; normalized rows support
// identity-specific inspection without persisting endpoint or error text.
func migrateProviderHealthHistory(ctx context.Context, conn *sql.Conn) error {
	var retained int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name IN(
		'provider_health_reports','provider_health_checks','provider_health_reports_time',
		'provider_health_checks_identity','provider_health_report_immutable_update',
		'provider_health_check_immutable_update','provider_health_check_binding')`).Scan(&retained); err != nil {
		return err
	}
	if retained != 0 {
		return errors.New("provider health history schema exists before schema 53")
	}
	if _, err := conn.ExecContext(ctx, `CREATE TABLE provider_health_reports(
		id TEXT PRIMARY KEY CHECK(length(id)=64 AND id NOT GLOB '*[^0-9a-f]*'),
		version INTEGER NOT NULL CHECK(version=1),
		checked_at INTEGER NOT NULL CHECK(checked_at>0),
		status TEXT NOT NULL CHECK(status IN('healthy','degraded','unavailable')),
		ready INTEGER NOT NULL CHECK(ready IN(0,1)),
		body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 131072));
	CREATE TABLE provider_health_checks(
		report_id TEXT NOT NULL REFERENCES provider_health_reports(id) ON DELETE CASCADE,
		ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 511),
		component TEXT NOT NULL CHECK(component IN('provider','model')),
		check_id TEXT NOT NULL CHECK(length(CAST(check_id AS BLOB)) BETWEEN 1 AND 128),
		status TEXT NOT NULL CHECK(status IN('healthy','degraded','unavailable','disabled','unknown')),
		code TEXT NOT NULL CHECK(code IN('available','unavailable','disabled_by_policy','credentials_missing',
			'discovery_failed','model_missing','model_metadata_missing','capacity_exhausted','metrics_unknown')),
		PRIMARY KEY(report_id,ordinal));
	CREATE INDEX provider_health_reports_time ON provider_health_reports(checked_at DESC,id DESC);
	CREATE INDEX provider_health_checks_identity ON provider_health_checks(component,check_id,report_id);
	CREATE TRIGGER provider_health_report_immutable_update BEFORE UPDATE ON provider_health_reports
		BEGIN SELECT RAISE(ABORT,'provider health report immutable'); END;
	CREATE TRIGGER provider_health_check_immutable_update BEFORE UPDATE ON provider_health_checks
		BEGIN SELECT RAISE(ABORT,'provider health check immutable'); END;
	CREATE TRIGGER provider_health_check_binding BEFORE INSERT ON provider_health_checks
		WHEN NOT EXISTS(SELECT 1 FROM provider_health_reports report WHERE report.id=NEW.report_id
			AND json_extract(report.body,'$.checks['||NEW.ordinal||'].component') IS NEW.component
			AND json_extract(report.body,'$.checks['||NEW.ordinal||'].id') IS NEW.check_id
			AND json_extract(report.body,'$.checks['||NEW.ordinal||'].status') IS NEW.status
			AND json_extract(report.body,'$.checks['||NEW.ordinal||'].code') IS NEW.code)
		BEGIN SELECT RAISE(ABORT,'provider health check binding'); END;
	PRAGMA user_version=53;`); err != nil {
		return err
	}
	// The caller has already validated or migrated schema 52 in this same
	// serialized transaction. Repeating its recursive chain here materially
	// penalizes every fresh database without adding an independent boundary.
	return validateProviderHealthHistoryObjects(ctx, conn)
}

func discardEmptyFutureProviderHealthHistory(ctx context.Context, conn *sql.Conn) error {
	var tables, indexes, triggers int
	if err := conn.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN('provider_health_reports','provider_health_checks')),
		(SELECT count(*) FROM sqlite_master WHERE type='index' AND name IN('provider_health_reports_time','provider_health_checks_identity')),
		(SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name IN('provider_health_report_immutable_update','provider_health_check_immutable_update','provider_health_check_binding'))`).
		Scan(&tables, &indexes, &triggers); err != nil {
		return err
	}
	if tables == 0 && indexes == 0 && triggers == 0 {
		return nil
	}
	if tables != 2 || indexes != 2 || triggers != 3 {
		return errors.New("incomplete provider health history schema before schema 53")
	}
	var reports, checks int
	if err := conn.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM provider_health_reports),
		(SELECT count(*) FROM provider_health_checks)`).Scan(&reports, &checks); err != nil {
		return err
	}
	if reports != 0 || checks != 0 {
		return errors.New("provider health history exists before schema 53")
	}
	_, err := conn.ExecContext(ctx, `DROP TRIGGER provider_health_check_binding;
		DROP TRIGGER provider_health_check_immutable_update;
		DROP TRIGGER provider_health_report_immutable_update;
		DROP INDEX provider_health_checks_identity;
		DROP INDEX provider_health_reports_time;
		DROP TABLE provider_health_checks;
		DROP TABLE provider_health_reports;`)
	return err
}

func validateProviderHealthHistorySchema(ctx context.Context, conn *sql.Conn) error {
	if err := validateContextCompactionDelegationSchema(ctx, conn); err != nil {
		return err
	}
	return validateProviderHealthHistoryObjects(ctx, conn)
}

func validateProviderHealthHistoryObjects(ctx context.Context, conn *sql.Conn) error {
	if !browserTableShape(ctx, conn, "provider_health_reports", "id:TEXT:0:1,version:INTEGER:1:0,checked_at:INTEGER:1:0,status:TEXT:1:0,ready:INTEGER:1:0,body:BLOB:1:0") ||
		!browserTableRules(ctx, conn, "provider_health_reports", []string{
			"idtextprimarykeycheck(length(id)=64andidnotglob'*[^0-9a-f]*')",
			"versionintegernotnullcheck(version=1)",
			"checked_atintegernotnullcheck(checked_at>0)",
			"statustextnotnullcheck(statusin('healthy','degraded','unavailable'))",
			"readyintegernotnullcheck(readyin(0,1))",
			"bodyblobnotnullcheck(length(body)between1and131072)",
		}) ||
		!browserTableShape(ctx, conn, "provider_health_checks", "report_id:TEXT:1:1,ordinal:INTEGER:1:2,component:TEXT:1:0,check_id:TEXT:1:0,status:TEXT:1:0,code:TEXT:1:0") ||
		!browserTableRules(ctx, conn, "provider_health_checks", []string{
			"report_idtextnotnullreferencesprovider_health_reports(id)ondeletecascade",
			"ordinalintegernotnullcheck(ordinalbetween0and511)",
			"componenttextnotnullcheck(componentin('provider','model'))",
			"check_idtextnotnullcheck(length(cast(check_idasblob))between1and128)",
			"statustextnotnullcheck(statusin('healthy','degraded','unavailable','disabled','unknown'))",
			"codetextnotnullcheck(codein('available','unavailable','disabled_by_policy','credentials_missing','discovery_failed','model_missing','model_metadata_missing','capacity_exhausted','metrics_unknown'))",
			"primarykey(report_id,ordinal)",
		}) ||
		!workboardObjectRules(ctx, conn, "index", "provider_health_reports_time", []string{"indexprovider_health_reports_timeonprovider_health_reports(checked_atdesc,iddesc)"}) ||
		!workboardObjectRules(ctx, conn, "index", "provider_health_checks_identity", []string{"indexprovider_health_checks_identityonprovider_health_checks(component,check_id,report_id)"}) ||
		!workboardObjectRules(ctx, conn, "trigger", "provider_health_report_immutable_update", []string{"beforeupdateonprovider_health_reports", "providerhealthreportimmutable"}) ||
		!workboardObjectRules(ctx, conn, "trigger", "provider_health_check_immutable_update", []string{"beforeupdateonprovider_health_checks", "providerhealthcheckimmutable"}) ||
		!workboardObjectRules(ctx, conn, "trigger", "provider_health_check_binding", []string{
			"beforeinsertonprovider_health_checks", "providerhealthcheckbinding",
			"json_extract(report.body,'$.checks['||new.ordinal||'].component')isnew.component",
			"json_extract(report.body,'$.checks['||new.ordinal||'].id')isnew.check_id",
			"json_extract(report.body,'$.checks['||new.ordinal||'].status')isnew.status",
			"json_extract(report.body,'$.checks['||new.ordinal||'].code')isnew.code",
		}) {
		return errors.New("invalid provider health history schema")
	}
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		if err = rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	return nil
}
