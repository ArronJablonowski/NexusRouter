package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/health"
)

var ErrProviderHealth = errors.New("provider health history unavailable")

type healthCheckRow struct {
	ordinal int
	check   health.Check
}

func providerHealthBody(report health.Report) (string, []byte, []healthCheckRow, error) {
	if report.Validate() != nil || report.CheckedAt.UnixNano() <= 0 {
		return "", nil, nil, ErrProviderHealth
	}
	rows := make([]healthCheckRow, 0, len(report.Checks))
	for i, check := range report.Checks {
		if check.Component == "provider" || check.Component == "model" {
			rows = append(rows, healthCheckRow{ordinal: i, check: check})
		}
	}
	body, err := json.Marshal(report)
	if err != nil || len(body) == 0 || len(body) > 128<<10 {
		return "", nil, nil, ErrProviderHealth
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), body, rows, nil
}

func decodeProviderHealth(id string, body []byte) (health.Report, []healthCheckRow, error) {
	var report health.Report
	if len(body) == 0 || len(body) > 128<<10 || json.Unmarshal(body, &report) != nil {
		return health.Report{}, nil, ErrProviderHealth
	}
	actualID, canonical, checks, err := providerHealthBody(report)
	if err != nil || len(checks) == 0 || actualID != id || !bytes.Equal(body, canonical) {
		return health.Report{}, nil, ErrProviderHealth
	}
	return report, checks, nil
}

// RecordProviderHealth atomically appends one validated report and enforces a
// bounded newest-first retention window. Exact retries are idempotent. Only
// provider/model check enums and safe IDs are normalized for lookup.
func (s *Store) RecordProviderHealth(ctx context.Context, report health.Report, retain int) error {
	if s == nil || ctx == nil || ctx.Err() != nil || retain < 1 || retain > 100000 {
		return ErrProviderHealth
	}
	id, body, checks, err := providerHealthBody(report)
	if err != nil {
		return err
	}
	if len(checks) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrProviderHealth
	}
	defer tx.Rollback()
	var schema int
	if tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema) != nil || schema < 53 || schema > currentStorageSchema {
		return ErrProviderHealth
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO provider_health_reports(id,version,checked_at,status,ready,body)
		VALUES(?,?,?,?,?,?)`, id, report.Version, report.CheckedAt.UnixNano(), report.Status, report.Ready, body)
	if err != nil {
		return ErrProviderHealth
	}
	inserted, err := result.RowsAffected()
	if err != nil || inserted < 0 || inserted > 1 {
		return ErrProviderHealth
	}
	if inserted == 1 {
		for _, row := range checks {
			if _, err = tx.ExecContext(ctx, `INSERT INTO provider_health_checks(report_id,ordinal,component,check_id,status,code)
				VALUES(?,?,?,?,?,?)`, id, row.ordinal, row.check.Component, row.check.ID, row.check.Status, row.check.Code); err != nil {
				return ErrProviderHealth
			}
		}
	}
	if err = verifyProviderHealthRows(ctx, tx, id, body, checks); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM provider_health_reports WHERE id IN (
		SELECT id FROM provider_health_reports ORDER BY checked_at DESC,id DESC LIMIT -1 OFFSET ?)`, retain); err != nil {
		return ErrProviderHealth
	}
	if err = tx.Commit(); err != nil {
		return ErrProviderHealth
	}
	return nil
}

type providerHealthQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func verifyProviderHealthRows(ctx context.Context, q providerHealthQuery, id string, body []byte, expected []healthCheckRow) error {
	var stored []byte
	var version int
	var checkedAt int64
	var status string
	var ready bool
	if err := q.QueryRowContext(ctx, `SELECT version,checked_at,status,ready,
		CASE WHEN length(CAST(body AS BLOB)) BETWEEN 1 AND 131072 THEN body END
		FROM provider_health_reports WHERE id=?`, id).Scan(&version, &checkedAt, &status, &ready, &stored); err != nil || !bytes.Equal(stored, body) {
		return ErrProviderHealth
	}
	report, _, err := decodeProviderHealth(id, stored)
	if err != nil || version != report.Version || checkedAt != report.CheckedAt.UnixNano() || status != report.Status || ready != report.Ready {
		return ErrProviderHealth
	}
	rows, err := q.QueryContext(ctx, `SELECT ordinal,component,check_id,status,code
		FROM provider_health_checks WHERE report_id=? ORDER BY ordinal`, id)
	if err != nil {
		return ErrProviderHealth
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		var ordinal int
		var check health.Check
		if rows.Scan(&ordinal, &check.Component, &check.ID, &check.Status, &check.Code) != nil || index >= len(expected) ||
			ordinal != expected[index].ordinal || check != expected[index].check {
			return ErrProviderHealth
		}
		index++
	}
	if rows.Err() != nil || rows.Close() != nil || index != len(expected) {
		return ErrProviderHealth
	}
	return nil
}

// ProviderHealthHistory returns the newest validated reports containing the
// requested provider or model identity. It never creates or migrates storage.
func (s *Store) ProviderHealthHistory(ctx context.Context, component, id string, limit int) ([]health.Report, error) {
	if s == nil || ctx == nil || ctx.Err() != nil || limit < 1 || limit > 1000 ||
		(health.Check{Component: component, ID: id, Status: "healthy", Code: "available"}).Validate() != nil ||
		(component != "provider" && component != "model") {
		return nil, ErrProviderHealth
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, ErrProviderHealth
	}
	defer tx.Rollback()
	var schema int
	if tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema) != nil || schema < 53 || schema > currentStorageSchema {
		return nil, ErrProviderHealth
	}
	rows, err := tx.QueryContext(ctx, `SELECT report.id,report.body FROM provider_health_reports report
		JOIN provider_health_checks check_row ON check_row.report_id=report.id
		WHERE check_row.component=? AND check_row.check_id=?
		ORDER BY report.checked_at DESC,report.id DESC LIMIT ?`, component, id, limit)
	if err != nil {
		return nil, ErrProviderHealth
	}
	defer rows.Close()
	result := make([]health.Report, 0, limit)
	for rows.Next() {
		var reportID string
		var body []byte
		if rows.Scan(&reportID, &body) != nil {
			return nil, ErrProviderHealth
		}
		report, checks, decodeErr := decodeProviderHealth(reportID, body)
		if decodeErr != nil || verifyProviderHealthRows(ctx, tx, reportID, body, checks) != nil {
			return nil, ErrProviderHealth
		}
		result = append(result, report)
	}
	if rows.Err() != nil || rows.Close() != nil || tx.Commit() != nil {
		return nil, ErrProviderHealth
	}
	return result, nil
}
